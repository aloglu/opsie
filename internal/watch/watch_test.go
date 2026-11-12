package watch

import (
	"strings"
	"testing"
	"time"

	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/notify"
)

var t0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func res(id string, st health.Status) health.Result {
	return health.Result{ID: id, Title: id, Status: st, Summary: "s"}
}

type run struct {
	s *State
	n config.Notify
	t time.Time
}

func (r *run) step(results ...health.Result) (*notify.Message, []Event) {
	r.t = r.t.Add(30 * time.Second)
	return r.s.Step(results, r.t, r.n)
}

func newRun() *run {
	return &run{s: &State{}, n: config.Default().Notify, t: t0}
}

func TestFirstRoundIsOneSummary(t *testing.T) {
	r := newRun()
	m, ev := r.step(res("disk", health.Warn), res("load", health.OK))
	if m == nil || len(ev) != 1 || ev[0].Kind != "watching" || !strings.Contains(m.Text(), "1 thing needs attention") || m.Level != notify.LevelWarn {
		t.Fatalf("%+v %+v", m, ev)
	}
	// Nothing new on the next round.
	if m, _ := r.step(res("disk", health.Warn), res("load", health.OK)); m != nil {
		t.Fatalf("repeated: %+v", m)
	}
	r2 := newRun()
	if m, _ := r2.step(res("load", health.OK)); m == nil || !strings.Contains(m.Text(), "Nothing needs attention") {
		t.Fatalf("all-clear start: %+v", m)
	}
}

func TestProblemsMustSettle(t *testing.T) {
	r := newRun()
	r.step(res("load", health.OK))
	if m, _ := r.step(res("load", health.Crit)); m != nil {
		t.Fatal("one bad check shouldn't alert")
	}
	if m, _ := r.step(res("load", health.OK)); m != nil {
		t.Fatal("a spike that went away shouldn't alert")
	}
	r.step(res("load", health.Crit))
	m, ev := r.step(res("load", health.Crit))
	if m == nil || ev[0].Kind != "problem" || m.Level != notify.LevelCrit || !strings.HasPrefix(m.Lines[0], "✗ load") {
		t.Fatalf("%+v", m)
	}
	// Recovery also needs two checks.
	if m, _ := r.step(res("load", health.OK)); m != nil {
		t.Fatal("recovered too soon")
	}
	m, ev = r.step(res("load", health.OK))
	if m == nil || ev[0].Kind != "recovered" || m.Level != notify.LevelOK || m.Lines[0] != "✓ load is fine again" {
		t.Fatalf("%+v", m)
	}
}

func TestEscalationAndReminders(t *testing.T) {
	r := newRun()
	r.step()
	r.step(res("disk", health.Warn))
	if m, _ := r.step(res("disk", health.Warn)); m == nil {
		t.Fatal("warn")
	}
	r.step(res("disk", health.Crit))
	if m, ev := r.step(res("disk", health.Crit)); m == nil || ev[0].Kind != "problem" || m.Level != notify.LevelCrit {
		t.Fatal("getting worse should alert again")
	}
	// Twelve hours later, a reminder.
	r.t = r.t.Add(12 * time.Hour)
	if m, ev := r.step(res("disk", health.Crit)); m == nil || ev[0].Kind != "still" || !strings.Contains(m.Lines[0], "still") {
		t.Fatalf("reminder %+v", m)
	}
	if m, _ := r.step(res("disk", health.Crit)); m != nil {
		t.Fatal("reminder twice")
	}
}

func TestQuietHoursHoldWarnings(t *testing.T) {
	r := newRun()
	r.n.QuietHours = "11:00-13:00"
	r.step()
	r.step(res("swap", health.Warn), res("svc", health.Crit))
	m, ev := r.step(res("swap", health.Warn), res("svc", health.Crit))
	if m == nil || len(ev) != 1 || ev[0].ID != "svc" {
		t.Fatalf("only problems get through quiet hours: %+v", ev)
	}
	// After quiet hours the held warning goes out.
	r.t = time.Date(2026, 11, 10, 13, 0, 0, 0, time.UTC)
	if _, ev := r.step(res("swap", health.Warn), res("svc", health.Crit)); len(ev) != 1 || ev[0].ID != "swap" {
		t.Fatalf("held warning: %+v", ev)
	}
}

func TestCritOnlyLevel(t *testing.T) {
	r := newRun()
	r.n.Level = "crit"
	r.step()
	r.step(res("swap", health.Warn))
	if m, _ := r.step(res("swap", health.Warn)); m != nil {
		t.Fatal("warnings shouldn't send at level crit")
	}
}

func TestGoneResultsRecover(t *testing.T) {
	r := newRun()
	r.step(res("ctr:web", health.Crit))
	r.step()
	m, ev := r.step()
	if m == nil || ev[0].Kind != "recovered" || ev[0].ID != "ctr:web" {
		t.Fatalf("%+v", ev)
	}
	if len(r.s.Tracks) != 0 {
		t.Fatal("gone results should be forgotten")
	}
}

func TestUnknownIsNoNews(t *testing.T) {
	r := newRun()
	r.step(res("disk", health.Crit))
	for range 3 {
		if m, _ := r.step(res("disk", health.Unknown)); m != nil {
			t.Fatalf("unknown sent %+v", m)
		}
	}
}

func TestCloneIsDeep(t *testing.T) {
	r := newRun()
	r.step(res("disk", health.Warn))
	c := r.s.Clone()
	r.s.Tracks["disk"].Told = health.OK
	if c.Tracks["disk"].Told != health.Warn {
		t.Fatal("clone shares tracks")
	}
}
