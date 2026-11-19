package store

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID                string
	Login             string
	Credits           int
	ChatPlacePayments int
}

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	store := &Store{pool: pool}
	if err := store.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, migrationSQL); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, migrationChannelSQL); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, migrationLoginCaseSQL)
	return err
}

func (s *Store) Seed(ctx context.Context, login, password string, credits int) error {
	login = normalizeLogin(login)
	if login == "" || password == "" {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO users (id, login, password_hash, credits, chatplace_payments)
		VALUES ($1, $2, $3, $4, 0)
		ON CONFLICT (login) DO NOTHING
	`, newID(), login, string(hash), credits)
	return err
}

func (s *Store) Authenticate(ctx context.Context, login, password string) (User, error) {
	var user User
	var hash string
	err := s.pool.QueryRow(ctx, `
		SELECT id, login, password_hash, credits, chatplace_payments
		FROM users
		WHERE lower(login) = lower($1)
	`, normalizeLogin(login)).Scan(&user.ID, &user.Login, &hash, &user.Credits, &user.ChatPlacePayments)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUnauthorized
	}
	if err != nil {
		return User{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, ErrUnauthorized
	}
	return user, nil
}

func (s *Store) CreateSession(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO sessions (token, user_id, expires_at)
		VALUES ($1, $2, $3)
	`, token, userID, time.Now().Add(ttl))
	return token, err
}

func (s *Store) UserBySession(ctx context.Context, token string) (User, error) {
	var user User
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.login, u.credits, u.chatplace_payments
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token = $1 AND s.expires_at > now()
	`, token).Scan(&user.ID, &user.Login, &user.Credits, &user.ChatPlacePayments)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUnauthorized
	}
	return user, err
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token = $1`, token)
	return err
}

func (s *Store) ConsumeCredit(ctx context.Context, userID string) (User, error) {
	var user User
	err := s.pool.QueryRow(ctx, `
		UPDATE users
		SET credits = credits - 1
		WHERE id = $1 AND credits > 0
		RETURNING id, login, credits, chatplace_payments
	`, userID).Scan(&user.ID, &user.Login, &user.Credits, &user.ChatPlacePayments)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNoCredits
	}
	return user, err
}

type PaymentResult struct {
	User     User
	Created  bool
	Password string
}

func (s *Store) ApplyChatPlacePayment(ctx context.Context, externalID, instagram, channel string) (PaymentResult, error) {
	login := normalizeLogin(instagram)
	if login == "" {
		return PaymentResult{}, ErrBadPayment
	}
	externalID = strings.TrimSpace(externalID)
	channel = strings.TrimSpace(channel)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PaymentResult{}, err
	}
	defer tx.Rollback(ctx)

	password, err := randomPassword()
	if err != nil {
		return PaymentResult{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return PaymentResult{}, err
	}

	var user User
	created := false
	err = tx.QueryRow(ctx, `
		SELECT id, login, credits, chatplace_payments
		FROM users
		WHERE lower(login) = lower($1)
		FOR UPDATE
	`, login).Scan(&user.ID, &user.Login, &user.Credits, &user.ChatPlacePayments)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
			INSERT INTO users (id, login, password_hash, credits, chatplace_payments)
			VALUES ($1, $2, $3, 0, 0)
			RETURNING id, login, credits, chatplace_payments
		`, newID(), login, string(hash)).Scan(&user.ID, &user.Login, &user.Credits, &user.ChatPlacePayments)
		if err == nil {
			created = true
		}
	}
	if err != nil {
		return PaymentResult{}, err
	}

	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if externalID == "" {
		externalID = login + ":" + stamp
	} else {
		externalID = externalID + ":" + stamp
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO chatplace_payments (id, external_id, user_id, channel)
		VALUES ($1, $2, $3, $4)
	`, newID(), externalID, user.ID, channel)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return PaymentResult{}, ErrDuplicatePayment
		}
		return PaymentResult{}, err
	}

	err = tx.QueryRow(ctx, `
		UPDATE users
		SET login = $2,
		    password_hash = $3,
		    credits = credits + 1,
		    chatplace_payments = chatplace_payments + 1
		WHERE id = $1
		RETURNING id, login, credits, chatplace_payments
	`, user.ID, login, string(hash)).Scan(&user.ID, &user.Login, &user.Credits, &user.ChatPlacePayments)
	if err != nil {
		return PaymentResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PaymentResult{}, err
	}

	return PaymentResult{User: user, Created: created, Password: password}, nil
}

func (s *Store) UserByLogin(ctx context.Context, login string) (User, error) {
	var user User
	err := s.pool.QueryRow(ctx, `
		SELECT id, login, credits, chatplace_payments
		FROM users
		WHERE lower(login) = lower($1)
	`, normalizeLogin(login)).Scan(&user.ID, &user.Login, &user.Credits, &user.ChatPlacePayments)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUnauthorized
	}
	return user, err
}

func normalizeLogin(login string) string {
	login = strings.TrimSpace(login)
	login = strings.TrimPrefix(login, "@")
	var b strings.Builder
	for _, r := range login {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	hexID := hex.EncodeToString(raw[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexID[0:8], hexID[8:12], hexID[12:16], hexID[16:20], hexID[20:32])
}

func randomToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func randomPassword() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, value := range raw {
		b.WriteByte(alphabet[int(value)%len(alphabet)])
	}
	return b.String(), nil
}

//go:embed migrations/001_init.sql
var migrationSQL string

//go:embed migrations/002_payment_channel.sql
var migrationChannelSQL string

//go:embed migrations/003_login_case.sql
var migrationLoginCaseSQL string

var (
	ErrUnauthorized     = errors.New("unauthorized")
	ErrNoCredits        = errors.New("no credits")
	ErrBadPayment       = errors.New("bad payment")
	ErrDuplicatePayment = errors.New("duplicate payment")
)
