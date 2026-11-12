// Package notify sends alerts to ntfy, Telegram or a webhook.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aloglu/opsie/internal/config"
)

// Level is how serious a message is.
type Level int

const (
	LevelInfo Level = iota
	LevelOK
	LevelWarn
	LevelCrit
)

// Message is one alert, possibly about several things at once.
type Message struct {
	Title string
	Lines []string
	Level Level
}

// Text is the message as plain text.
func (m Message) Text() string { return strings.Join(m.Lines, "\n") }

// TelegramBase is the Bot API's address; tests point it elsewhere.
var TelegramBase = "https://api.telegram.org"

var client = &http.Client{Timeout: 15 * time.Second}

// Send delivers m to every target that's set, and reports the ones that
// failed.
func Send(ctx context.Context, n config.Notify, m Message) error {
	var errs []error
	if n.NtfyURL != "" {
		if err := ntfy(ctx, n, m); err != nil {
			errs = append(errs, fmt.Errorf("ntfy: %w", err))
		}
	}
	if n.TelegramToken != "" && n.TelegramChat != "" {
		if err := telegram(ctx, n, m); err != nil {
			errs = append(errs, fmt.Errorf("telegram: %w", err))
		}
	}
	if n.WebhookURL != "" {
		if err := webhook(ctx, n, m); err != nil {
			errs = append(errs, fmt.Errorf("webhook: %w", err))
		}
	}
	return errors.Join(errs...)
}

func ntfy(ctx context.Context, n config.Notify, m Message) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.NtfyURL, strings.NewReader(m.Text()))
	if err != nil {
		return err
	}
	req.Header.Set("Title", m.Title)
	priority, tag := "default", "information_source"
	switch m.Level {
	case LevelCrit:
		priority, tag = "high", "rotating_light"
	case LevelWarn:
		tag = "warning"
	case LevelOK:
		priority, tag = "low", "white_check_mark"
	}
	req.Header.Set("Priority", priority)
	req.Header.Set("Tags", tag)
	if n.NtfyToken != "" {
		req.Header.Set("Authorization", "Bearer "+n.NtfyToken)
	}
	return do(req)
}

func telegram(ctx context.Context, n config.Notify, m Message) error {
	body, _ := json.Marshal(map[string]any{
		"chat_id":              n.TelegramChat,
		"text":                 m.Title + "\n" + m.Text(),
		"disable_notification": m.Level < LevelWarn,
	})
	u := TelegramBase + "/bot" + url.PathEscape(n.TelegramToken) + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	err = do(req)
	// The token is in the URL; keep it out of error messages.
	if err != nil {
		return errors.New(strings.ReplaceAll(err.Error(), n.TelegramToken, "…"))
	}
	return nil
}

func webhook(ctx context.Context, n config.Notify, m Message) error {
	level := map[Level]string{LevelInfo: "info", LevelOK: "ok", LevelWarn: "warn", LevelCrit: "crit"}[m.Level]
	body, _ := json.Marshal(map[string]any{"title": m.Title, "text": m.Text(), "lines": m.Lines, "level": level})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return do(req)
}

func do(req *http.Request) error {
	req.Header.Set("User-Agent", "opsie")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}
