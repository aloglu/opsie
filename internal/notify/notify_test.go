package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aloglu/opsie/internal/config"
)

func TestSendToEveryTarget(t *testing.T) {
	got := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/topic":
			got["ntfy"] = r.Header.Get("Title") + "|" + r.Header.Get("Priority") + "|" + r.Header.Get("Authorization") + "|" + string(body)
		case strings.HasPrefix(r.URL.Path, "/botSECRET/"):
			var v map[string]any
			_ = json.Unmarshal(body, &v)
			got["telegram"] = v["chat_id"].(string) + "|" + v["text"].(string)
		case r.URL.Path == "/hook":
			var v map[string]any
			_ = json.Unmarshal(body, &v)
			got["webhook"] = v["level"].(string)
		}
	}))
	defer srv.Close()
	TelegramBase = srv.URL
	n := config.Notify{NtfyURL: srv.URL + "/topic", NtfyToken: "tk", TelegramToken: "SECRET", TelegramChat: "42", WebhookURL: srv.URL + "/hook"}
	m := Message{Title: "opsie · harbor", Lines: []string{"✗ Disk / 96%", "✓ web is healthy"}, Level: LevelCrit}
	if err := Send(context.Background(), n, m); err != nil {
		t.Fatal(err)
	}
	if got["ntfy"] != "opsie · harbor|high|Bearer tk|✗ Disk / 96%\n✓ web is healthy" {
		t.Errorf("ntfy %q", got["ntfy"])
	}
	if got["telegram"] != "42|opsie · harbor\n✗ Disk / 96%\n✓ web is healthy" {
		t.Errorf("telegram %q", got["telegram"])
	}
	if got["webhook"] != "crit" {
		t.Errorf("webhook %q", got["webhook"])
	}
}

func TestFailuresHideTheTelegramToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()
	TelegramBase = srv.URL
	err := Send(context.Background(), config.Notify{TelegramToken: "SECRET", TelegramChat: "1", NtfyURL: srv.URL}, Message{Title: "x"})
	if err == nil || strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), "ntfy") || !strings.Contains(err.Error(), "telegram") {
		t.Fatalf("%v", err)
	}
}
