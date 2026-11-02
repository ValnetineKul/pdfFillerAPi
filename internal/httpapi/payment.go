package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

type paymentInput struct {
	PaymentID string
	Login     string
	Channel   string
	ChatID    string
}

func readPayment(r *http.Request) (paymentInput, error) {
	fields, err := paymentFields(r)
	if err != nil {
		return paymentInput{}, err
	}
	channel := normalizeChannel(first(fields, "channel", "platform", "messenger", "source"))
	login := first(fields, "username", "instagramusername", "telegramusername", "login")
	if login == "" {
		login = first(fields, "instagram_username", "telegram_username")
	}
	if channel == "" {
		switch {
		case first(fields, "instagramusername", "instagram_username") != "":
			channel = "instagram"
		case first(fields, "telegramusername", "telegram_username") != "":
			channel = "telegram"
		}
	}
	if login == "" && channel == "telegram" {
		if id := digits(first(fields, "telegramid", "telegram_id")); id != "" {
			login = "tg" + id
		}
	}
	if login == "" {
		if email := first(fields, "email"); strings.Contains(email, "@") {
			login = strings.SplitN(email, "@", 2)[0]
		}
	}
	if login == "" {
		if phone := digits(first(fields, "phone")); phone != "" {
			login = phone
		}
	}
	return paymentInput{
		PaymentID: first(fields, "paymentid", "payment_id", "externalid", "external_id"),
		Login:     login,
		Channel:   channel,
		ChatID:    first(fields, "chatid", "chat_id"),
	}, nil
}

func paymentFields(r *http.Request) (map[string]string, error) {
	if strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		fields := make(map[string]string, len(r.Form))
		for key, values := range r.Form {
			if len(values) == 0 {
				continue
			}
			if value := cleanValue(values[0]); value != "" {
				fields[strings.ToLower(key)] = value
			}
		}
		return fields, nil
	}
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	return flattenFields(raw), nil
}

func flattenFields(raw map[string]any) map[string]string {
	fields := map[string]string{}
	var walk func(map[string]any, bool)
	walk = func(value map[string]any, top bool) {
		for key, item := range value {
			switch typed := item.(type) {
			case map[string]any:
				walk(typed, false)
			default:
				text := cleanValue(asString(typed))
				if text == "" {
					continue
				}
				name := strings.ToLower(key)
				if top || fields[name] == "" {
					fields[name] = text
				}
			}
		}
	}
	walk(raw, false)
	walk(raw, true)
	return fields
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func cleanValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "{{") {
		return ""
	}
	return value
}

func first(fields map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := fields[strings.ToLower(key)]; value != "" {
			return value
		}
	}
	return ""
}

func digits(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func normalizeChannel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "instagram", "ig", "insta":
		return "instagram"
	case "telegram", "tg":
		return "telegram"
	default:
		return ""
	}
}

type paymentNotice int

const (
	noticeCreated paymentNotice = iota
	noticeRenewed
	noticeDuplicate
)

func credentialsMessage(login, password, loginURL string, notice paymentNotice) string {
	var b strings.Builder
	switch notice {
	case noticeCreated:
		b.WriteString("Оплата прошла. Аккаунт создан.\n\n")
		b.WriteString("Логин: " + login + "\n")
		b.WriteString("Пароль: " + password + "\n")
	case noticeRenewed:
		b.WriteString("Оплата прошла. На аккаунт добавлена 1 генерация.\n\n")
		b.WriteString("Логин: " + login + "\n")
		b.WriteString("Пароль отправляли после первой оплаты.\n")
	default:
		b.WriteString("Эта оплата уже учтена.\n\n")
		b.WriteString("Логин: " + login + "\n")
	}
	if loginURL != "" {
		b.WriteString("Войти: " + loginURL + "\n")
	}
	return strings.TrimSpace(b.String())
}
