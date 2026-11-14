package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/aloglu/opsie/internal/agents"
	"github.com/aloglu/opsie/internal/config"
)

func TestSpendIsAskedNowAndThen(t *testing.T) {
	asked, offline := 0, false
	w := &spendWatch{ask: func(_ context.Context, k agents.Keys) []agents.Service {
		asked++
		s := agents.Service{ID: "openrouter", Tail: "1234", HaveLimit: true, Limit: 10, Remaining: float64(asked)}
		if offline {
			s = agents.Service{ID: "openrouter", Tail: "1234", Err: "couldn't reach the service"}
		}
		return []agents.Service{s}
	}}
	cfg := config.Default().Agents
	now := time.Now()
	if got := w.read(context.Background(), cfg, now); asked != 0 || got.Services != nil {
		t.Fatalf("no keys, nothing to ask: %d %+v", asked, got)
	}
	cfg.OpenRouterKey = config.Keys{{Secret: "sk-or-v1-test-1234"}}
	w.read(context.Background(), cfg, now)
	got := w.read(context.Background(), cfg, now.Add(time.Minute))
	if asked != 1 || len(got.Services) != 1 || got.LowPercent != 10 {
		t.Fatalf("asked %d times in a minute: %+v", asked, got)
	}
	// Not reached: the last answer stands.
	offline = true
	got = w.read(context.Background(), cfg, now.Add(spendEvery+time.Minute))
	if asked != 2 || got.Services[0].Err != "" || got.Services[0].Remaining != 1 {
		t.Fatalf("offline: %d %+v", asked, got)
	}
	// A changed key is asked about straight away.
	offline = false
	cfg.OpenRouterKey = append(cfg.OpenRouterKey, config.Key{Secret: "sk-or-v1-other-5678"})
	if w.read(context.Background(), cfg, now.Add(spendEvery+2*time.Minute)); asked != 3 {
		t.Fatalf("a new key wasn't asked about: %d", asked)
	}
	cfg.Check = false
	if got := w.read(context.Background(), cfg, now.Add(spendEvery+3*time.Minute)); asked != 3 || got.Services != nil {
		t.Fatalf("turned off: %d %+v", asked, got)
	}
}
