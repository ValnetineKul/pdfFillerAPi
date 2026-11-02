package main

import (
	"bufio"
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"pdffiller/api/internal/chatplace"
	"pdffiller/api/internal/httpapi"
	"pdffiller/api/internal/store"
)

func main() {
	loadDotEnv(".env")

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	webhookSecret := os.Getenv("CHATPLACE_WEBHOOK_SECRET")
	if webhookSecret == "" {
		log.Fatal("CHATPLACE_WEBHOOK_SECRET is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	data, err := store.Open(ctx, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer data.Close()

	credits, _ := strconv.Atoi(os.Getenv("SEED_CREDITS"))
	if err := data.Seed(context.Background(), os.Getenv("SEED_LOGIN"), os.Getenv("SEED_PASSWORD"), credits); err != nil {
		log.Fatal(err)
	}

	address := listenAddress()
	log.Printf("listening on %s", address)
	cfg := httpapi.Config{
		WebhookSecret: webhookSecret,
		LoginURL:      os.Getenv("APP_LOGIN_URL"),
	}
	if apiKey := os.Getenv("CHATPLACE_API_KEY"); apiKey != "" {
		cfg.Sender = chatplace.NewClient(apiKey, os.Getenv("CHATPLACE_API_URL"))
	}
	if err := http.ListenAndServe(address, httpapi.New(data, cfg).Handler()); err != nil {
		log.Fatal(err)
	}
}

func listenAddress() string {
	if address := strings.TrimSpace(os.Getenv("ADDR")); address != "" {
		return address
	}
	if port := strings.TrimPrefix(strings.TrimSpace(os.Getenv("PORT")), ":"); port != "" {
		return ":" + port
	}
	return ":8080"
}

// loadDotEnv sets missing process env vars from a local .env file (KEY=VALUE).
func loadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, value)
	}
}
