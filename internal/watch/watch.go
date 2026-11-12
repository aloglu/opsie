// Package watch decides when the background checks should send an alert:
// a problem that's lasted two checks, a fix that's held for two checks,
// and reminders, minding quiet hours.
package watch

import (
	"fmt"
	"strings"
	"time"

	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/notify"
)

// settle is how many checks in a row a change must hold before it counts,
// so a moment's spike doesn't send anything.
const settle = 2

// State is what the watcher remembers between checks, and across
// restarts.
type State struct {
	Started bool              `json:"started"`
	Tracks  map[string]*Track `json:"tracks"`
}

// Track follows one checklist result.
type Track struct {
	Title   string        `json:"title"`
	Summary string        `json:"summary"`
	Status  health.Status `json:"status"`
	// Streak is how many checks in a row it's had this status.
	Streak int `json:"streak"`
	// Told is the status the last alert reported; SentAt when.
	Told   health.Status `json:"told"`
	SentAt time.Time     `json:"sent_at"`
	// Missing counts checks it wasn't in the results at all.
	Missing int `json:"missing,omitempty"`
}

// Event is something worth keeping in the history.
type Event struct {
	At      time.Time     `json:"at"`
	ID      string        `json:"id"`
	Title   string        `json:"title"`
	Summary string        `json:"summary,omitempty"`
	Status  health.Status `json:"status"`
	// Kind is "problem", "still", "recovered" or "watching".
	Kind string `json:"kind"`
}

// Line is the event as one line of an alert.
func (e Event) Line() string {
	text := e.Title
	if e.Summary != "" {
		text += ": " + e.Summary
	}
	switch e.Kind {
	case "recovered":
		return "✓ " + e.Title + " is fine again"
	case "still":
		return icon(e.Status) + " still: " + text
	}
	return icon(e.Status) + " " + text
}

func icon(s health.Status) string {
	switch s {
	case health.Crit:
		return "✗"
	case health.Warn:
		return "▲"
	}
	return "●"
}

// skip are results that only mean something to someone at the dashboard.
var skip = map[string]bool{"admin": true, "update": true}

// Step takes one round of results and returns what to send, if anything,
// and what happened. The caller saves the state only once the message is
// delivered, so a failed send is tried again next time.
func (s *State) Step(results []health.Result, now time.Time, n config.Notify) (msg *notify.Message, events []Event) {
	if s.Tracks == nil {
		s.Tracks = map[string]*Track{}
	}
	min := health.Warn
	if n.Level == "crit" {
		min = health.Crit
	}
	quiet := n.Quiet(now)
	allowed := func(st health.Status) bool { return !quiet || st >= health.Crit }
	repeat := time.Duration(n.RepeatHours * float64(time.Hour))

	seen := map[string]bool{}
	for _, r := range results {
		if skip[r.ID] {
			continue
		}
		seen[r.ID] = true
		t := s.Tracks[r.ID]
		if t == nil {
			t = &Track{Status: r.Status}
			s.Tracks[r.ID] = t
		}
		if r.Status == health.Unknown {
			continue // no news either way
		}
		t.Missing = 0
		if r.Status == t.Status {
			t.Streak++
		} else {
			t.Status, t.Streak = r.Status, 1
		}
		t.Title, t.Summary = r.Title, r.Summary
		ev := Event{At: now, ID: r.ID, Title: r.Title, Summary: r.Summary, Status: r.Status}

		if !s.Started {
			// The first round sets the baseline: what's wrong now goes in
			// one "watching" message rather than an alert each.
			t.Streak = settle
			if r.Status >= min {
				t.Told, t.SentAt = r.Status, now
				ev.Kind = "watching"
				events = append(events, ev)
			}
			continue
		}
		switch bad := r.Status >= min; {
		case bad && t.Streak >= settle && r.Status > t.Told && allowed(r.Status):
			t.Told, t.SentAt = r.Status, now
			ev.Kind = "problem"
			events = append(events, ev)
		case bad && t.Told >= min && repeat > 0 && now.Sub(t.SentAt) >= repeat && allowed(r.Status):
			t.Told, t.SentAt = r.Status, now
			ev.Kind = "still"
			events = append(events, ev)
		case !bad && t.Told >= min && t.Streak >= settle && (allowed(t.Told)):
			t.Told, t.SentAt = health.OK, now
			ev.Kind = "recovered"
			events = append(events, ev)
		}
	}
	// A result that's gone, like a removed container, is fine now.
	for id, t := range s.Tracks {
		if seen[id] {
			continue
		}
		t.Missing++
		if t.Missing < settle {
			continue
		}
		if t.Told >= min && allowed(t.Told) {
			events = append(events, Event{At: now, ID: id, Title: t.Title, Status: health.OK, Kind: "recovered"})
		}
		delete(s.Tracks, id)
	}
	first := !s.Started
	s.Started = true

	if len(events) == 0 {
		if first {
			return &notify.Message{Lines: []string{"✓ Watching. Nothing needs attention."}, Level: notify.LevelOK}, []Event{{At: now, Kind: "watching", Title: "Watching", Status: health.OK}}
		}
		return nil, nil
	}
	m := &notify.Message{Level: notify.LevelOK}
	if first {
		m.Lines = append(m.Lines, fmt.Sprintf("Watching. %s:", plural(len(events), "thing needs", "things need")+" attention"))
	}
	for _, e := range events {
		m.Lines = append(m.Lines, e.Line())
		switch {
		case e.Kind != "recovered" && e.Status >= health.Crit:
			m.Level = notify.LevelCrit
		case e.Kind != "recovered" && e.Status >= health.Warn && m.Level < notify.LevelWarn:
			m.Level = notify.LevelWarn
		}
	}
	return m, events
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Clone copies the state, to put back if sending fails.
func (s *State) Clone() *State {
	c := &State{Started: s.Started, Tracks: make(map[string]*Track, len(s.Tracks))}
	for id, t := range s.Tracks {
		cp := *t
		c.Tracks[id] = &cp
	}
	return c
}

// Title is an alert's title for a server.
func Title(host string) string {
	host, _, _ = strings.Cut(host, ".")
	return "opsie · " + host
}
