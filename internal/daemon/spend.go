package daemon

import (
	"context"
	"reflect"
	"time"

	"github.com/aloglu/opsie/internal/agents"
	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/health"
)

// spendEvery is how often the services paid by use are asked what their
// keys have left. What's left moves slowly, and they limit how often
// they're asked.
const spendEvery = 15 * time.Minute

// spendWatch keeps the services' last answers between checks.
type spendWatch struct {
	keys agents.Keys
	at   time.Time
	list []agents.Service
	ask  func(context.Context, agents.Keys) []agents.Service
}

// read is what the services last said, asking again when that's old or
// the keys have changed. With the check off or no keys, it's nothing.
func (w *spendWatch) read(ctx context.Context, cfg config.Agents, now time.Time) health.Spending {
	keys := cfg.Keys()
	if !cfg.Check || !keys.Any() {
		w.list, w.at = nil, time.Time{}
		return health.Spending{}
	}
	if w.at.IsZero() || now.Sub(w.at) >= spendEvery || !reflect.DeepEqual(keys, w.keys) {
		ask := w.ask
		if ask == nil {
			ask = agents.Services
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		fresh := ask(ctx, keys)
		cancel()
		// A service that wasn't reached this time keeps its last answer,
		// so a moment offline doesn't clear a warning.
		for i, s := range fresh {
			if s.Err == "" || s.Refused() || !reflect.DeepEqual(keys, w.keys) {
				continue
			}
			for _, old := range w.list {
				if old.ID == s.ID && old.Tail == s.Tail && old.Err == "" {
					fresh[i] = old
				}
			}
		}
		w.keys, w.at, w.list = keys, now, fresh
	}
	return health.Spending{Services: w.list, LowPercent: cfg.LowPercent, LowBalance: cfg.LowBalance}
}
