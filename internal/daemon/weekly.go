package daemon

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/history"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/metrics"
	"github.com/aloglu/opsie/internal/notify"
	"github.com/aloglu/opsie/internal/watch"
)

const weeklyFile = "weekly.json"

var days = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}

// ParseWeekly reads a time like "sun 18:00".
func ParseWeekly(s string) (time.Weekday, int, int, bool) {
	d, clock, ok := strings.Cut(strings.ToLower(strings.TrimSpace(s)), " ")
	day, known := days[d]
	t, err := time.Parse("15:04", strings.TrimSpace(clock))
	if !ok || !known || err != nil {
		return 0, 0, 0, false
	}
	return day, t.Hour(), t.Minute(), true
}

// weeklyDue reports whether the weekly summary should go now.
func weeklyDue(now time.Time, when string, last time.Time) bool {
	day, h, m, ok := ParseWeekly(when)
	if !ok || now.Weekday() != day {
		return false
	}
	at := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	return !now.Before(at) && now.Sub(at) < 6*time.Hour && now.Sub(last) > 24*time.Hour
}

// Weekly is the summary of the past week.
func Weekly(host string, now time.Time, snap metrics.Snapshot, results []health.Result, events []watch.Event,
	samples []history.Sample, nightly []string, runs []health.BackupRun) notify.Message {
	from := now.Add(-7 * 24 * time.Hour)
	m := notify.Message{Title: watch.Title(host), Level: notify.LevelInfo}
	add := func(s string) { m.Lines = append(m.Lines, s) }

	up := "Up " + human.Duration(snap.Host.Uptime)
	if snap.Host.Uptime > 0 && snap.Host.Uptime < 7*24*time.Hour {
		up += ", restarted " + snap.Host.Boot.Local().Format("Mon 15:04")
	}
	add("Your week: " + up + ".")

	// Problems that came up, by how often.
	count := map[string]int{}
	for _, e := range events {
		if e.Kind == "problem" && e.At.After(from) {
			count[e.Title]++
		}
	}
	var names []string
	for n := range count {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		return count[names[i]] > count[names[j]] || count[names[i]] == count[names[j]] && names[i] < names[j]
	})
	if len(names) == 0 {
		add("No problems came up.")
	} else {
		var parts []string
		for i, n := range names {
			if i == 5 {
				parts = append(parts, fmt.Sprintf("%d more", len(names)-5))
				break
			}
			if count[n] > 1 {
				n += fmt.Sprintf(" (%d×)", count[n])
			}
			parts = append(parts, n)
		}
		add("Came up: " + strings.Join(parts, ", ") + ".")
	}
	var open []string
	for _, r := range results {
		if r.Status >= health.Warn {
			open = append(open, r.Title)
		}
	}
	if len(open) == 0 {
		add("Nothing needs attention now.")
	} else {
		add("Needs attention now: " + strings.Join(open, ", ") + ".")
	}

	// How busy it was.
	if _, avg, high, n := history.Stats(samples, from, func(s history.Sample) (float64, bool) { return s.CPU, true }); n > 0 {
		_, mavg, mhigh, _ := history.Stats(samples, from, func(s history.Sample) (float64, bool) { return s.Mem, true })
		add(fmt.Sprintf("CPU %.0f%% on average, %.0f%% at most; memory %.0f%%, %.0f%% at most.", avg, high, mavg, mhigh))
	}
	for _, d := range snap.Disks {
		if d.Mount != "/" {
			continue
		}
		line := fmt.Sprintf("Disk / is %.0f%% full", d.Percent)
		for _, s := range samples {
			if v, ok := s.Disks["/"]; ok && !s.At.Before(from) {
				if diff := d.Percent - v; diff >= 0.5 || diff <= -0.5 {
					line += fmt.Sprintf(" (%+.0f%% this week)", diff)
				}
				break
			}
		}
		add(line + ", " + human.Bytes(d.Free) + " free.")
	}

	if len(nightly) > 0 {
		var parts []string
		for _, app := range nightly {
			word := app + " not yet"
			for _, r := range runs {
				if r.Project == app {
					word = app + " ✓"
					if !r.OK {
						word = app + " failed"
					}
				}
			}
			parts = append(parts, word)
		}
		add("Last night's backups: " + strings.Join(parts, ", ") + ".")
	}
	return m
}

type weeklyState struct {
	Last time.Time `json:"last"`
}

// weekly sends the summary when it's due.
func weeklyStep(dir, when string, now time.Time, build func() notify.Message, send func(notify.Message) error) {
	path := filepath.Join(dir, weeklyFile)
	var s weeklyState
	readJSON(path, &s)
	if !weeklyDue(now, when, s.Last) {
		return
	}
	if send(build()) == nil {
		s.Last = now
		saveJSON(path, s, 0o644)
	}
}
