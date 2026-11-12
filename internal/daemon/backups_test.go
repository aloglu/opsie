package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/history"
	"github.com/aloglu/opsie/internal/metrics"
	"github.com/aloglu/opsie/internal/watch"
)

func TestNightlyIsDue(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 10, h, m, 0, 0, time.UTC) }
	for _, c := range []struct {
		now  time.Time
		last string
		want bool
	}{
		{at(3, 29), "2026-11-09", false},
		{at(3, 30), "2026-11-09", true},
		{at(5, 0), "", true},
		{at(5, 30), "2026-11-09", false}, // too late: don't stop apps midday
		{at(3, 45), "2026-11-10", false}, // already ran today
	} {
		if got := due(c.now, "03:30", c.last); got != c.want {
			t.Errorf("%v last %q: got %v", c.now, c.last, got)
		}
	}
}

func TestWeekly(t *testing.T) {
	sun := time.Date(2026, 11, 15, 18, 5, 0, 0, time.UTC)
	if !weeklyDue(sun, "sun 18:00", time.Time{}) || weeklyDue(sun, "sun 18:00", sun.Add(-time.Hour)) || weeklyDue(sun.Add(24*time.Hour), "sun 18:00", time.Time{}) {
		t.Error("due at the wrong time")
	}
	if _, _, _, ok := ParseWeekly("someday 18:00"); ok {
		t.Error("bad day accepted")
	}
	now := sun
	snap := metrics.Snapshot{Host: metrics.Host{Uptime: 3 * 24 * time.Hour, Boot: now.Add(-3 * 24 * time.Hour)},
		Disks: []metrics.Disk{{Mount: "/", Percent: 52, Free: 18 << 30}}}
	events := []watch.Event{{At: now.Add(-time.Hour), Title: "Disk /", Kind: "problem"}, {At: now.Add(-2 * time.Hour), Title: "Disk /", Kind: "problem"},
		{At: now.Add(-3 * time.Hour), Title: "web", Kind: "problem"}, {At: now.Add(-30 * 24 * time.Hour), Title: "old", Kind: "problem"}}
	samples := []history.Sample{{At: now.Add(-6 * 24 * time.Hour), CPU: 10, Mem: 50, Disks: map[string]float64{"/": 49}}, {At: now, CPU: 30, Mem: 70}}
	results := []health.Result{{Title: "web", Status: health.Warn}}
	m := Weekly("harbor.example", now, snap, results, events, samples, []string{"app", "db"}, []health.BackupRun{{Project: "app", OK: true}})
	text := m.Text()
	for _, want := range []string{"restarted", "Disk / (2×), web", "Needs attention now: web", "CPU 20% on average, 30% at most", "(+3% this week)", "app ✓, db not yet"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "old") {
		t.Error("an old problem was counted")
	}
}
