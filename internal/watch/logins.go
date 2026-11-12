package watch

import (
	"time"

	"github.com/aloglu/opsie/internal/priv"
)

// forgetAfter is how long an address stays known without a login from it.
const forgetAfter = 90 * 24 * time.Hour

// Logins remembers which addresses have logged in over SSH, so a login
// from a new one stands out.
type Logins struct {
	Known  map[string]time.Time `json:"known"` // address: last login from it
	Last   time.Time            `json:"last"`  // newest login seen
	Seeded bool                 `json:"seeded"`
}

// Step takes the recent logins and returns the ones that are new since the
// last step, and whether each is from an address not seen before. The
// first step only learns what's there, so turning this on doesn't alert
// about every login of the past day.
func (l *Logins) Step(accepted []priv.Login, now time.Time) (fresh []priv.Login, firstTime []bool) {
	if l.Known == nil {
		l.Known = map[string]time.Time{}
	}
	for _, a := range accepted {
		if !a.At.After(l.Last) {
			continue
		}
		_, known := l.Known[a.From]
		if l.Seeded {
			fresh = append(fresh, a)
			firstTime = append(firstTime, !known)
		}
		l.Known[a.From] = a.At
		l.Last = a.At
	}
	l.Seeded = true
	for ip, at := range l.Known {
		if now.Sub(at) > forgetAfter {
			delete(l.Known, ip)
		}
	}
	return fresh, firstTime
}
