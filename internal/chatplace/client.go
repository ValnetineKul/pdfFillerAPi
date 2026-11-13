package chatplace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const defaultURL = "https://mcp.chatplace.io/mcp"

// Client sends a chat message through the ChatPlace MCP API.
type Client struct {
	apiKey string
	url    string
	http   *http.Client

	mu       sync.Mutex
	schema   map[string][]string
	schemaOK bool
}

func NewClient(apiKey, apiURL string) *Client {
	if strings.TrimSpace(apiURL) == "" {
		apiURL = defaultURL
	}
	return &Client{
		apiKey: strings.TrimSpace(apiKey),
		url:    apiURL,
		http:   &http.Client{Timeout: 20 * time.Second},
	}
}

// SendInstagram finds the Instagram chat for username and sends text there.
func (c *Client) SendInstagram(ctx context.Context, username, text string) error {
	chatID, err := c.findInstagramChat(ctx, username)
	if err != nil {
		return err
	}
	return c.SendMessage(ctx, chatID, text)
}

func (c *Client) SendMessage(ctx context.Context, chatID, text string) error {
	if c == nil || c.apiKey == "" {
		return fmt.Errorf("CHATPLACE_API_KEY is not set")
	}
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return fmt.Errorf("chat id is empty")
	}
	_, err := c.callTool(ctx, "chats_send_message", map[string]any{
		"chatId": chatID,
		"text":   text,
	})
	return err
}

func (c *Client) findInstagramChat(ctx context.Context, username string) (string, error) {
	if c == nil || c.apiKey == "" {
		return "", fmt.Errorf("CHATPLACE_API_KEY is not set")
	}
	handle := instagramHandle(username)
	if handle == "" {
		return "", fmt.Errorf("instagram username is empty")
	}
	props := c.toolProperties(ctx, "chats_list")
	searchKey := firstName(props, "search", "query", "q", "username")
	cursorKey := firstName(props, "cursor", "pageToken", "nextCursor", "offset")
	channelKey := firstName(props, "channel", "platform", "messenger")
	if len(props) == 0 {
		searchKey = "search"
	}

	argsFor := func(cursor string, withSearch bool) map[string]any {
		args := map[string]any{}
		if withSearch && searchKey != "" {
			args[searchKey] = handle
		}
		if cursor != "" && cursorKey != "" {
			args[cursorKey] = cursor
		}
		if channelKey != "" {
			args[channelKey] = "instagram"
		}
		return args
	}

	cursor := ""
	withSearch := searchKey != ""
	for page := 0; page < 8; page++ {
		raw, err := c.callTool(ctx, "chats_list", argsFor(cursor, withSearch))
		if err != nil && withSearch {
			withSearch = false
			cursor = ""
			raw, err = c.callTool(ctx, "chats_list", argsFor("", false))
		}
		if err != nil {
			return "", err
		}
		if id, ok := pickInstagramChat(raw, handle); ok {
			return id, nil
		}
		next := nextCursor(raw)
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return "", fmt.Errorf("instagram chat for @%s was not found", handle)
}

func (c *Client) toolProperties(ctx context.Context, toolName string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.schemaOK {
		c.schema = map[string][]string{}
		raw, err := c.rpc(ctx, "tools/list", map[string]any{})
		if err == nil {
			var parsed any
			if json.Unmarshal(raw, &parsed) == nil {
				collectSchemas(parsed, c.schema)
			}
		}
		c.schemaOK = true
	}
	return c.schema[toolName]
}

func (c *Client) callTool(ctx context.Context, name string, arguments map[string]any) (json.RawMessage, error) {
	body, err := c.rpc(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	})
	if err != nil {
		return nil, err
	}
	return toolResult(body)
}

func (c *Client) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c == nil || c.apiKey == "" {
		return nil, fmt.Errorf("CHATPLACE_API_KEY is not set")
	}
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		payload["params"] = params
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chatplace request failed")
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("chatplace response was unreadable")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("chatplace returned status %d", resp.StatusCode)
	}
	return jsonPayload(responseBody), nil
}

func jsonPayload(body []byte) json.RawMessage {
	body = bytes.TrimSpace(body)
	if bytes.HasPrefix(body, []byte("{")) || bytes.HasPrefix(body, []byte("[")) {
		return body
	}
	var last []byte
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if bytes.Equal(data, []byte("[DONE]")) || len(data) == 0 || data[0] != '{' {
			continue
		}
		last = data
	}
	if last != nil {
		return last
	}
	return body
}

func toolResult(body json.RawMessage) (json.RawMessage, error) {
	var raw struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("chatplace returned an unreadable response")
	}
	if raw.Error != nil {
		if raw.Error.Message != "" {
			return nil, fmt.Errorf("chatplace: %s", raw.Error.Message)
		}
		return nil, fmt.Errorf("chatplace rejected the message")
	}
	var result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if len(raw.Result) > 0 {
		_ = json.Unmarshal(raw.Result, &result)
	}
	if result.IsError {
		for _, part := range result.Content {
			if text := strings.TrimSpace(part.Text); text != "" {
				return nil, fmt.Errorf("chatplace: %s", text)
			}
		}
		return nil, fmt.Errorf("chatplace rejected the message")
	}
	if len(raw.Result) == 0 {
		return body, nil
	}
	return raw.Result, nil
}

type chatHit struct {
	id       string
	username string
	channel  string
}

func pickInstagramChat(raw json.RawMessage, handle string) (string, bool) {
	var parsed any
	if json.Unmarshal(raw, &parsed) != nil {
		return "", false
	}
	var hits []chatHit
	collectChats(parsed, 0, &hits)
	for _, hit := range hits {
		if instagramHandle(hit.username) != handle {
			continue
		}
		switch hit.channel {
		case "", "instagram", "ig", "insta":
			return hit.id, true
		}
	}
	return "", false
}

func collectChats(node any, depth int, out *[]chatHit) {
	if depth > 8 || node == nil {
		return
	}
	switch typed := node.(type) {
	case map[string]any:
		if hit, ok := chatFromMap(typed); ok {
			*out = append(*out, hit)
		}
		for _, value := range typed {
			collectChats(value, depth+1, out)
		}
	case []any:
		for _, value := range typed {
			collectChats(value, depth+1, out)
		}
	case string:
		text := strings.TrimSpace(typed)
		if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
			var parsed any
			if json.Unmarshal([]byte(text), &parsed) == nil {
				collectChats(parsed, depth+1, out)
			}
		}
	}
}

func chatFromMap(value map[string]any) (chatHit, bool) {
	username := firstString(value, "username", "userName", "instagramUsername", "instagram_username", "login")
	if username == "" {
		return chatHit{}, false
	}
	id := firstString(value, "chatId", "chat_id", "id")
	if id == "" {
		return chatHit{}, false
	}
	return chatHit{
		id:       id,
		username: username,
		channel:  strings.ToLower(firstString(value, "channel", "platform", "messenger", "source")),
	}, true
}

func nextCursor(raw json.RawMessage) string {
	var parsed any
	if json.Unmarshal(raw, &parsed) != nil {
		return ""
	}
	return cursorValue(parsed, 0)
}

func cursorValue(node any, depth int) string {
	if depth > 6 || node == nil {
		return ""
	}
	switch typed := node.(type) {
	case map[string]any:
		if cursor := firstString(typed, "nextCursor", "next_cursor", "nextPage"); cursor != "" {
			return cursor
		}
		for _, value := range typed {
			if cursor := cursorValue(value, depth+1); cursor != "" {
				return cursor
			}
		}
	case []any:
		for _, value := range typed {
			if cursor := cursorValue(value, depth+1); cursor != "" {
				return cursor
			}
		}
	}
	return ""
}

func collectSchemas(node any, schemas map[string][]string) {
	switch typed := node.(type) {
	case map[string]any:
		name, _ := typed["name"].(string)
		if name != "" {
			schema := typed["inputSchema"]
			if schema == nil {
				schema = typed["input_schema"]
			}
			if props := propertyNames(schema); len(props) > 0 {
				schemas[name] = props
			}
		}
		for _, value := range typed {
			collectSchemas(value, schemas)
		}
	case []any:
		for _, value := range typed {
			collectSchemas(value, schemas)
		}
	case string:
		text := strings.TrimSpace(typed)
		if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
			var parsed any
			if json.Unmarshal([]byte(text), &parsed) == nil {
				collectSchemas(parsed, schemas)
			}
		}
	}
}

func propertyNames(schema any) []string {
	object, ok := schema.(map[string]any)
	if !ok {
		return nil
	}
	props, ok := object["properties"].(map[string]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	return names
}

func firstName(names []string, candidates ...string) string {
	for _, candidate := range candidates {
		for _, name := range names {
			if strings.EqualFold(name, candidate) {
				return name
			}
		}
	}
	return ""
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		for name, item := range value {
			if !strings.EqualFold(name, key) {
				continue
			}
			switch typed := item.(type) {
			case string:
				if text := strings.TrimSpace(typed); text != "" {
					return text
				}
			case float64:
				return fmt.Sprintf("%.0f", typed)
			case json.Number:
				return typed.String()
			}
		}
	}
	return ""
}

func instagramHandle(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "@")
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
