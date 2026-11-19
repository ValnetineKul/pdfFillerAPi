package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"pdffiller/api/internal/store"
)

const sessionCookie = "pdffiller_session"

type MessageSender interface {
	SendMessage(ctx context.Context, chatID, text string) error
	SendInstagram(ctx context.Context, username, text string) error
}

type Config struct {
	WebhookSecret string
	LoginURL      string
	Sender        MessageSender
}

type Server struct {
	store    *store.Store
	secret   string
	secure   bool
	loginURL string
	sender   MessageSender
}

func New(data *store.Store, cfg Config) *Server {
	return &Server{
		store:    data,
		secret:   cfg.WebhookSecret,
		secure:   os.Getenv("COOKIE_SECURE") == "1",
		loginURL: strings.TrimSpace(cfg.LoginURL),
		sender:   cfg.Sender,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/login", s.login)
	mux.HandleFunc("POST /api/v1/logout", s.logout)
	mux.HandleFunc("GET /api/v1/me", s.me)
	mux.HandleFunc("POST /api/v1/documents/consume", s.consume)
	mux.HandleFunc("POST /api/v1/payments/chatplace", s.chatPlace)
	return mux
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Некорректный запрос.")
		return
	}
	user, err := s.store.Authenticate(r.Context(), body.Login, body.Password)
	if errors.Is(err, store.ErrUnauthorized) {
		writeError(w, http.StatusUnauthorized, "Неверный логин или пароль.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось войти.")
		return
	}
	token, err := s.store.CreateSession(r.Context(), user.ID, 14*24*time.Hour)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось войти.")
		return
	}
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, userJSON(user))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.secure,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFromCookie(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, userJSON(user))
}

func (s *Server) consume(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFromCookie(w, r)
	if !ok {
		return
	}
	updated, err := s.store.ConsumeCredit(r.Context(), user.ID)
	if errors.Is(err, store.ErrNoCredits) {
		writeError(w, http.StatusPaymentRequired, "Генерация недоступна. Оплатите следующую анкету в Instagram.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось списать генерацию.")
		return
	}
	writeJSON(w, http.StatusOK, userJSON(updated))
}

func (s *Server) chatPlace(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		log.Printf("chatplace payment rejected: bad webhook secret")
		writeError(w, http.StatusUnauthorized, "Неверный секрет вебхука.")
		return
	}
	payment, err := readPayment(r)
	if err != nil {
		log.Printf("chatplace payment rejected: bad request body")
		writeError(w, http.StatusBadRequest, "Некорректный запрос.")
		return
	}
	result, err := s.store.ApplyChatPlacePayment(r.Context(), payment.PaymentID, payment.Login, payment.Channel)
	if errors.Is(err, store.ErrBadPayment) {
		log.Printf("chatplace payment rejected: missing username")
		writeError(w, http.StatusBadRequest, "Нужен username из Instagram или Telegram.")
		return
	}
	if errors.Is(err, store.ErrDuplicatePayment) {
		user, lookupErr := s.store.UserByLogin(r.Context(), payment.Login)
		if lookupErr != nil {
			writeError(w, http.StatusConflict, "Эта оплата уже учтена.")
			return
		}
		s.writePayment(w, r, user, "", payment, noticeDuplicate)
		return
	}
	if err != nil {
		log.Printf("chatplace payment failed: login=%s err=%v", payment.Login, err)
		writeError(w, http.StatusInternalServerError, "Не удалось учесть оплату.")
		return
	}
	notice := noticeRenewed
	if result.Created {
		notice = noticeCreated
	}
	s.writePayment(w, r, result.User, result.Password, payment, notice)
}

func (s *Server) writePayment(w http.ResponseWriter, r *http.Request, user store.User, password string, payment paymentInput, notice paymentNotice) {
	message := credentialsMessage(user.Login, password, s.loginURL, notice)
	delivered, deliveryError := s.deliver(r.Context(), payment.Login, payment.ChatID, message)
	payload := map[string]any{
		"login":             user.Login,
		"credits":           user.Credits,
		"chatplacePayments": user.ChatPlacePayments,
		"created":           notice == noticeCreated,
		"channel":           payment.Channel,
		"message":           message,
		"delivered":         delivered,
	}
	if password != "" {
		payload["password"] = password
	}
	if deliveryError != "" {
		payload["deliveryError"] = deliveryError
	}
	log.Printf("chatplace payment: login=%s created=%t delivered=%t deliveryError=%s", user.Login, notice == noticeCreated, delivered, deliveryError)
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) deliver(ctx context.Context, username, chatID, message string) (bool, string) {
	if s.sender == nil {
		return false, "CHATPLACE_API_KEY не задан. Без него логин и пароль не уйдут в Instagram."
	}
	if err := s.sender.SendInstagram(ctx, username, message); err != nil {
		if strings.TrimSpace(chatID) != "" {
			if sendErr := s.sender.SendMessage(ctx, chatID, message); sendErr == nil {
				return true, ""
			}
		}
		log.Printf("instagram delivery failed: %v", err)
		return false, err.Error()
	}
	return true, ""
}

func (s *Server) authorized(r *http.Request) bool {
	if secretMatch(r.Header.Get("X-Webhook-Secret"), s.secret) {
		return true
	}
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return secretMatch(strings.TrimSpace(header[len(prefix):]), s.secret)
	}
	return false
}

func secretMatch(got, want string) bool {
	if got == "" || want == "" || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *Server) userFromCookie(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		writeError(w, http.StatusUnauthorized, "Войдите в аккаунт.")
		return store.User{}, false
	}
	user, err := s.store.UserBySession(r.Context(), cookie.Value)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Войдите в аккаунт.")
		return store.User{}, false
	}
	return user, true
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.secure,
		MaxAge:   int((14 * 24 * time.Hour).Seconds()),
	})
}

func userJSON(user store.User) map[string]any {
	return map[string]any{
		"id":                user.ID,
		"login":             user.Login,
		"credits":           user.Credits,
		"chatplacePayments": user.ChatPlacePayments,
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"message": message})
}
