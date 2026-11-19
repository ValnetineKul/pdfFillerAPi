package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadPaymentAliases(t *testing.T) {
	body := `{"paymentId":"pay-1","instagramUsername":"@Anna.Smith","channel":"IG","chatId":42}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/chatplace", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	got, err := readPayment(req)
	if err != nil {
		t.Fatal(err)
	}
	if got.PaymentID != "pay-1" || got.Login != "@Anna.Smith" || got.Channel != "instagram" || got.ChatID != "42" {
		t.Fatalf("parsed %+v", got)
	}
}

func TestReadPaymentTelegramWithoutUsername(t *testing.T) {
	body := `{"channel":"telegram","telegramId":"100200","profile":{"username":"{{ username }}"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/chatplace", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	got, err := readPayment(req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Login != "tg100200" || got.Channel != "telegram" {
		t.Fatalf("parsed %+v", got)
	}
}

func TestReadPaymentTopLevelWins(t *testing.T) {
	body := `{"username":"ann","profile":{"username":"other","chatId":"99"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/chatplace", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	got, err := readPayment(req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Login != "ann" || got.ChatID != "99" {
		t.Fatalf("parsed %+v", got)
	}
}

func TestCredentialsMessage(t *testing.T) {
	created := credentialsMessage("anna", "secret", "https://app.example", noticeCreated)
	if !strings.Contains(created, "Пароль: secret") || !strings.Contains(created, "https://app.example") {
		t.Fatalf("created message: %s", created)
	}
	renewed := credentialsMessage("kulioValio", "secret", "https://app.example", noticeRenewed)
	if !strings.Contains(renewed, "Пароль: secret") || !strings.Contains(renewed, "kulioValio") || !strings.Contains(renewed, "1 генерац") {
		t.Fatalf("renewed message: %s", renewed)
	}
	duplicate := credentialsMessage("anna", "secret", "", noticeDuplicate)
	if strings.Contains(duplicate, "secret") || !strings.Contains(duplicate, "уже учтена") {
		t.Fatalf("duplicate message: %s", duplicate)
	}
}

func TestWebhookSecret(t *testing.T) {
	server := New(nil, Config{WebhookSecret: "dev-secret"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/chatplace", nil)
	if server.authorized(req) {
		t.Fatal("missing secret was accepted")
	}
	req.Header.Set("Authorization", "Bearer dev-secret")
	if !server.authorized(req) {
		t.Fatal("bearer secret was rejected")
	}
}
