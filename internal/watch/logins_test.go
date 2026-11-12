package watch

import (
	"testing"
	"time"

	"github.com/aloglu/opsie/internal/priv"
)

func TestLoginsFromNewAddresses(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	home := priv.Login{At: now.Add(-time.Hour), User: "debian", From: "203.0.113.5"}
	var l Logins
	if fresh, _ := l.Step([]priv.Login{home}, now); len(fresh) != 0 {
		t.Fatal("the first step alerted")
	}
	again := home
	again.At = now
	if fresh, first := l.Step([]priv.Login{home, again}, now); len(fresh) != 1 || first[0] {
		t.Fatalf("known address: %v %v", fresh, first)
	}
	if fresh, _ := l.Step([]priv.Login{home, again}, now); len(fresh) != 0 {
		t.Fatal("the same login twice")
	}
	stranger := priv.Login{At: now.Add(time.Minute), User: "debian", From: "198.51.100.9"}
	if fresh, first := l.Step([]priv.Login{again, stranger}, now); len(fresh) != 1 || !first[0] {
		t.Fatalf("new address: %v %v", fresh, first)
	}
	// After three months without a login, an address is new again.
	l.Step(nil, now.Add(100*24*time.Hour))
	if _, ok := l.Known["203.0.113.5"]; ok {
		t.Error("an old address wasn't forgotten")
	}
}
