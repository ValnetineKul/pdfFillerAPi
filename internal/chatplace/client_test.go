package chatplace

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendMessage(t *testing.T) {
	var gotAuth, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"isError":false}}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL)
	if err := client.SendMessage(context.Background(), "chat-1", "Логин: anna"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth: %s", gotAuth)
	}
	if !strings.Contains(gotBody, `"name":"chats_send_message"`) || !strings.Contains(gotBody, `"chatId":"chat-1"`) {
		t.Fatalf("body: %s", gotBody)
	}
}

func TestSendMessageRejectsToolError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"outside the 24 hour window"}]}}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL)
	err := client.SendMessage(context.Background(), "chat-1", "hello")
	if err == nil || !strings.Contains(err.Error(), "24 hour") {
		t.Fatalf("error: %v", err)
	}
}
