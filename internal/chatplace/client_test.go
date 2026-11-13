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

func TestSendInstagram(t *testing.T) {
	var sent, listed string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "tools/list"):
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"chats_list","inputSchema":{"type":"object","properties":{"search":{"type":"string"}}}}]}}`))
		case strings.Contains(string(body), "chats_list"):
			listed = string(body)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"chats\":[{\"id\":\"tg-1\",\"username\":\"anna\",\"channel\":\"telegram\"},{\"id\":\"ig-1\",\"username\":\"@Anna\",\"channel\":\"instagram\"}]}"}]}}`))
		default:
			sent = string(body)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"isError":false}}`))
		}
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL)
	if err := client.SendInstagram(context.Background(), "@Anna", "Логин: anna\nПароль: secret"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listed, `"search":"anna"`) {
		t.Fatalf("search body: %s", listed)
	}
	if !strings.Contains(sent, `"chatId":"ig-1"`) || !strings.Contains(sent, "Пароль: secret") {
		t.Fatalf("sent: %s", sent)
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
