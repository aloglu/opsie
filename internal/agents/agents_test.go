package agents

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadAddsUpBothAgents(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC)
	stamp := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	claude := func(at, id, session string, in, out int) string {
		return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"requestId":"r-%s","sessionId":%q,"version":"2.9.1","message":{"id":%q,"model":"claude-fable-5-2","usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":1000,"cache_creation_input_tokens":100}}}`+"\n", at, id, session, id, in, out)
	}
	write(t, filepath.Join(home, ".claude", "projects", "-srv-app", "s1.jsonl"),
		`{"type":"user","timestamp":"`+stamp(-time.Hour)+`","message":{"content":"mentions \"usage\" and assistant"}}`+"\n"+
			claude(stamp(-time.Hour), "m1", "s1", 10, 20)+
			claude(stamp(-time.Hour), "m1", "s1", 10, 20)+ // the same answer, written twice
			claude(stamp(-50*time.Hour), "m2", "s1", 1, 2)+
			claude(stamp(-30*24*time.Hour), "m3", "s1", 5000, 5000)) // too old to count
	write(t, filepath.Join(home, ".claude", "projects", "-srv-app", "s2", "subagents", "a.jsonl"), claude(stamp(-2*time.Hour), "m4", "s2", 3, 4))

	count := func(at string, in, cached, out int, limits string) string {
		return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d}},"rate_limits":%s}}`+"\n", at, in, cached, out, limits)
	}
	limits := fmt.Sprintf(`{"primary":{"used_percent":12.0,"window_minutes":300,"resets_at":%d},"secondary":{"used_percent":46.0,"window_minutes":10080,"resets_at":%d},"plan_type":"plus"}`,
		now.Add(-time.Minute).Unix(), now.Add(48*time.Hour).Unix())
	write(t, filepath.Join(home, ".codex", "sessions", "2026", "11", "12", "rollout-1.jsonl"),
		`{"timestamp":"`+stamp(-3*time.Hour)+`","type":"session_meta","payload":{"cli_version":"0.167.2"}}`+"\n"+
			`{"timestamp":"`+stamp(-3*time.Hour)+`","type":"turn_context","payload":{"model":"gpt-6.1"}}`+"\n"+
			count(stamp(-3*time.Hour), 1000, 800, 50, "null")+
			count(stamp(-2*time.Hour), 3000, 2500, 90, limits))

	// The logs were just written, so they're all recent files.
	list := Read(home, now)
	if len(list) != 2 {
		t.Fatalf("found %d agents", len(list))
	}
	c := list[0]
	if c.ID != "claude" || c.Today.Input != 13 || c.Today.Output != 24 || c.Today.CacheRead != 2000 || c.Week.Input != 14 || c.SessionsToday != 2 || c.SessionsWeek != 2 {
		t.Errorf("claude %+v", c)
	}
	if c.Model != "claude-fable-5-2" || c.Version != "2.9.1" || c.Daily[Days-1] != c.Today.Total() || c.Daily[Days-3] != 1103 {
		t.Errorf("claude model %q version %q daily %v", c.Model, c.Version, c.Daily)
	}
	x := list[1]
	// Cached input isn't counted twice: 3000 in, of which 2500 cached.
	if x.ID != "codex" || x.Today.Input != 500 || x.Today.CacheRead != 2500 || x.Today.Output != 90 || x.SessionsToday != 1 || x.Model != "gpt-6.1" || x.Version != "0.167.2" || x.Plan != "plus" {
		t.Errorf("codex %+v", x)
	}
	if len(x.Limits) != 2 || x.Limits[0].Used != 0 || !x.Limits[0].Resets.IsZero() || x.Limits[1].Used != 46 || x.Limits[1].Name != "Week" {
		t.Errorf("codex limits %+v: the session window has started over, the week hasn't", x.Limits)
	}
}

func TestClaudeLimits(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := ClaudeLimits(context.Background(), dir); err == nil {
		t.Fatal("no login should be an error")
	}
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"five_hour":{"utilization":31.5,"resets_at":"2026-11-12T18:00:00Z"},"seven_day":{"utilization":8,"resets_at":null},"seven_day_opus":null}`)
	}))
	defer srv.Close()
	old := claudeUsageURL
	claudeUsageURL = srv.URL
	defer func() { claudeUsageURL = old }()

	write(t, filepath.Join(dir, ".credentials.json"), fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"tok","expiresAt":%d,"subscriptionType":"max"}}`, time.Now().Add(-time.Hour).UnixMilli()))
	if _, plan, err := ClaudeLimits(context.Background(), dir); err == nil || plan != "max" || got != "" {
		t.Fatalf("a login that has run out mustn't be sent: err %v, sent %q", err, got)
	}
	write(t, filepath.Join(dir, ".credentials.json"), fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"tok","expiresAt":%d,"subscriptionType":"max"}}`, time.Now().Add(time.Hour).UnixMilli()))
	limits, plan, err := ClaudeLimits(context.Background(), dir)
	if err != nil || plan != "max" || got != "Bearer tok" {
		t.Fatalf("err %v plan %q sent %q", err, plan, got)
	}
	if len(limits) != 2 || limits[0].Used != 31.5 || limits[0].Resets.IsZero() || limits[1].Name != "Week" {
		t.Fatalf("limits %+v", limits)
	}
}

func TestServices(t *testing.T) {
	sent := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent[r.URL.Path] = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/or/key":
			if r.Header.Get("Authorization") != "Bearer or-key" {
				w.WriteHeader(401)
				return
			}
			fmt.Fprint(w, `{"data":{"label":"sk-or-v1-abc...123","usage":25.75,"usage_daily":0.42,"usage_weekly":3.1,"usage_monthly":12.4,"limit":50,"limit_remaining":24.25,"limit_reset":"monthly","is_free_tier":false,"free_model_daily_requests":{"used":12,"limit":1000,"remaining":988}}}`)
		case "/or/credits":
			w.WriteHeader(403) // not a management key
		case "/ds/user/balance":
			fmt.Fprint(w, `{"is_available":false,"balance_infos":[{"currency":"USD","total_balance":"0.00","granted_balance":"0.00","topped_up_balance":"0.00"}]}`)
		}
	}))
	defer srv.Close()
	oldOR, oldDS := openRouterURL, deepSeekURL
	openRouterURL, deepSeekURL = srv.URL+"/or", srv.URL+"/ds"
	defer func() { openRouterURL, deepSeekURL = oldOR, oldDS }()

	if list := Services(context.Background(), Keys{}); len(list) != 0 || len(sent) != 0 {
		t.Fatalf("no keys, nothing to ask: %+v %v", list, sent)
	}
	list := Services(context.Background(), Keys{OpenRouter: "or-key", DeepSeek: "ds-key"})
	if len(list) != 2 {
		t.Fatalf("got %+v", list)
	}
	or := list[0]
	if or.Err != "" || or.Today != 0.42 || or.Month != 12.4 || !or.HaveLimit || or.Limit != 50 || or.Remaining != 24.25 || or.Reset != "monthly" || or.HaveCredits || or.FreeUsed != 12 || or.Blocked {
		t.Errorf("openrouter %+v", or)
	}
	ds := list[1]
	if ds.Err != "" || len(ds.Balances) != 1 || ds.Balances[0].Currency != "USD" || !ds.Blocked {
		t.Errorf("deepseek %+v", ds)
	}
	// Each key went only to its own service.
	if sent["/or/key"] != "Bearer or-key" || sent["/ds/user/balance"] != "Bearer ds-key" {
		t.Errorf("sent %v", sent)
	}
	if bad := Services(context.Background(), Keys{OpenRouter: "wrong"}); bad[0].Err != "the key was refused" {
		t.Errorf("a refused key: %+v", bad[0])
	}
}
