package chatplace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultURL = "https://mcp.chatplace.io/mcp"

// Client sends a chat message through the ChatPlace MCP API.
// The same call delivers to Instagram or Telegram, whichever chat chatId belongs to.
type Client struct {
	apiKey string
	url    string
	http   *http.Client
}

func NewClient(apiKey, apiURL string) *Client {
	if strings.TrimSpace(apiURL) == "" {
		apiURL = defaultURL
	}
	return &Client{
		apiKey: strings.TrimSpace(apiKey),
		url:    apiURL,
		http:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) SendMessage(ctx context.Context, chatID, text string) error {
	if c == nil || c.apiKey == "" {
		return fmt.Errorf("CHATPLACE_API_KEY is not set")
	}
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return fmt.Errorf("chat id is empty")
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "chats_send_message",
			"arguments": map[string]string{
				"chatId": chatID,
				"text":   text,
			},
		},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("chatplace request failed")
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("chatplace response was unreadable")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("chatplace returned status %d", resp.StatusCode)
	}
	return parseSendResult(payload)
}

func parseSendResult(payload []byte) error {
	var raw struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Result *struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return fmt.Errorf("chatplace returned an unreadable response")
	}
	if raw.Error != nil {
		if raw.Error.Message != "" {
			return fmt.Errorf("chatplace: %s", raw.Error.Message)
		}
		return fmt.Errorf("chatplace rejected the message")
	}
	if raw.Result != nil && raw.Result.IsError {
		for _, part := range raw.Result.Content {
			if text := strings.TrimSpace(part.Text); text != "" {
				return fmt.Errorf("chatplace: %s", text)
			}
		}
		return fmt.Errorf("chatplace rejected the message")
	}
	return nil
}
