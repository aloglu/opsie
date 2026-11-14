package health

import (
	"testing"

	"github.com/aloglu/opsie/internal/agents"
)

func TestSpending(t *testing.T) {
	or := func(n int, tail string) agents.Service {
		return agents.Service{ID: "openrouter", Name: "OpenRouter", N: n, Tail: tail, HaveSpend: true}
	}
	fine := or(0, "aaaa")
	fine.HaveLimit, fine.Limit, fine.Remaining = true, 50, 40
	low := or(1, "bbbb")
	low.KeyName, low.HaveLimit, low.Limit, low.Remaining, low.Reset = "work", true, 50, 4, "monthly"
	out := or(2, "cccc")
	out.HaveLimit, out.Limit, out.Blocked = true, 10, true
	refused := or(3, "dddd")
	refused.Err = "the key was refused"
	offline := or(4, "eeee")
	offline.Err = "couldn't reach the service"
	credits := or(5, "ffff")
	credits.HaveCredits, credits.Bought, credits.Used = true, 100, 95
	deepseek := agents.Service{ID: "deepseek", Name: "DeepSeek", Tail: "gggg", Balances: []agents.Balance{{Currency: "USD", Total: 0.4}}}

	got := map[string]Result{}
	for _, r := range spending(Spending{Services: []agents.Service{fine, low, out, refused, offline, credits, deepseek}, LowPercent: 10, LowBalance: 1}) {
		got[r.ID] = r
	}
	for id, want := range map[string]struct {
		st             Status
		title, summary string
	}{
		"spend:openrouter:bbbb": {Warn, "OpenRouter key work", "$4.00 left of $50.00"},
		"spend:openrouter:cccc": {Crit, "OpenRouter key ••••cccc", "nothing left"},
		"spend:openrouter:dddd": {Warn, "OpenRouter key ••••dddd", "refused"},
		"spend:openrouter:ffff": {Warn, "OpenRouter key ••••ffff", "$5.00 of credits left"},
		"spend:deepseek:gggg":   {Warn, "DeepSeek key", "balance is low"},
	} {
		if r := got[id]; r.Status != want.st || r.Title != want.title || r.Summary != want.summary {
			t.Errorf("%s: %v %q %q", id, r.Status, r.Title, r.Summary)
		}
	}
	// The fine one and the one that wasn't reached say nothing on their own.
	if len(got) != 5 {
		t.Errorf("results: %+v", got)
	}
	if got["spend:openrouter:bbbb"].Fixes[0].Special != "open:agents:service:openrouter#2" {
		t.Errorf("fix: %+v", got["spend:openrouter:bbbb"].Fixes)
	}

	// With nothing wrong, one line covers every key.
	all := spending(Spending{Services: []agents.Service{fine, offline}, LowPercent: 10})
	if len(all) != 1 || all[0].ID != "spend" || all[0].Status != OK || all[0].Summary != "1 key with enough left · 1 wasn't reached" {
		t.Errorf("all fine: %+v", all)
	}
	// Zero turns the warnings off, but not running out.
	if off := spending(Spending{Services: []agents.Service{low, out, deepseek}}); len(off) != 1 || off[0].Status != Crit {
		t.Errorf("thresholds off: %+v", off)
	}
	if none := spending(Spending{}); none != nil {
		t.Errorf("no keys: %+v", none)
	}
}
