package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"pdffiller/api/internal/store"
)

const sessionCookie = "pdffiller_session"

type Server struct {
	store  *store.Store
	secret string
	secure bool
}

func New(data *store.Store, webhookSecret string) *Server {
	return &Server{
		store:  data,
		secret: webhookSecret,
		secure: os.Getenv("COOKIE_SECURE") == "1",
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
	if s.secret == "" || r.Header.Get("X-Webhook-Secret") != s.secret {
		writeError(w, http.StatusUnauthorized, "Неверный секрет вебхука.")
		return
	}
	var body struct {
		PaymentID         string `json:"paymentId"`
		InstagramUsername string `json:"instagramUsername"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Некорректный запрос.")
		return
	}
	result, err := s.store.ApplyChatPlacePayment(r.Context(), body.PaymentID, body.InstagramUsername)
	if errors.Is(err, store.ErrBadPayment) {
		writeError(w, http.StatusBadRequest, "Нужны paymentId и instagramUsername.")
		return
	}
	if errors.Is(err, store.ErrDuplicatePayment) {
		user, lookupErr := s.store.UserByLogin(r.Context(), body.InstagramUsername)
		if lookupErr != nil {
			writeError(w, http.StatusConflict, "Эта оплата уже учтена.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"login":             user.Login,
			"credits":           user.Credits,
			"chatplacePayments": user.ChatPlacePayments,
			"created":           false,
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось учесть оплату.")
		return
	}
	payload := map[string]any{
		"login":             result.User.Login,
		"credits":           result.User.Credits,
		"chatplacePayments": result.User.ChatPlacePayments,
		"created":           result.Created,
	}
	if result.Created {
		payload["password"] = result.Password
	}
	writeJSON(w, http.StatusOK, payload)
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
