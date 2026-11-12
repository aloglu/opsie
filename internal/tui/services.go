package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/priv"
	"github.com/aloglu/opsie/internal/systemd"
)

// servicesEvery is how often the Services tab refreshes while it's open.
const servicesEvery = 5 * time.Second

type svcState struct {
	units  []systemd.Unit
	timers []systemd.Timer
	err    string
	loaded bool
	busy   bool
	gen    int
}

type (
	servicesMsg struct {
		units  []systemd.Unit
		timers []systemd.Timer
		err    error
	}
	servicesDueMsg int
)

func (m *Model) pollServices() tea.Cmd {
	if m.svc.busy {
		return nil
	}
	m.svc.busy = true
	return tea.Batch(m.loadCron(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		units, err := systemd.Services(ctx)
		if err != nil {
			return servicesMsg{err: err}
		}
		timers, err := systemd.Timers(ctx)
		return servicesMsg{units: units, timers: timers, err: err}
	})
}

func (m *Model) servicesUpdated(msg servicesMsg) tea.Cmd {
	s := &m.svc
	s.busy, s.loaded = false, true
	if msg.err != nil {
		s.err = msg.err.Error()
	} else {
		s.err, s.units, s.timers = "", msg.units, msg.timers
	}
	m.syncLists()
	s.gen++
	gen := s.gen
	return tea.Tick(servicesEvery, func(time.Time) tea.Msg { return servicesDueMsg(gen) })
}

// wantsServices reports whether anything on screen shows service data.
func (m *Model) wantsServices() bool {
	return m.tab == tabServices || (m.modal != nil && (strings.HasPrefix(m.modal.key, "unit:") || strings.HasPrefix(m.modal.key, "cron:")))
}

func (m *Model) unit(name string) (systemd.Unit, bool) {
	for _, u := range m.svc.units {
		if u.Name == name {
			return u, true
		}
	}
	return systemd.Unit{}, false
}

// --- page ---

func (m *Model) servicesSections(inner int) []section {
	t := m.th
	s := &m.svc
	if !s.loaded {
		return []section{{title: "Services", intro: []string{t.dim.Render("Reading systemd…")}}}
	}
	if s.err != "" && len(s.units) == 0 {
		return []section{{title: "Services", intro: wrap("Couldn't read systemd: "+s.err, inner)}}
	}
	var failed, running, stopped []entry
	for _, u := range s.units {
		id := "unit:" + u.Name
		switch {
		case u.Failed():
			right := t.dangerText.Render("failed")
			if !u.Since.IsZero() {
				right += t.dim.Render(" · " + human.Duration(m.now.Sub(u.Since)))
			}
			failed = append(failed, entry{id: id, lines: m.itemRow(t.statusIcon(health.Crit)+" "+t.bold.Render(u.Short()), right, inner)})
		case u.Running():
			var parts []string
			if u.HaveMemory {
				parts = append(parts, human.Bytes(u.Memory))
			}
			if !u.Since.IsZero() && inner >= 40 {
				parts = append(parts, human.Duration(m.now.Sub(u.Since)))
			}
			icon := t.okText.Render("●")
			if u.Restarts >= 3 {
				icon = t.statusIcon(health.Warn)
				parts = append([]string{t.warnText.Render(fmt.Sprintf("%d restarts", u.Restarts))}, parts...)
			}
			running = append(running, entry{id: id, lines: m.itemRow(icon+" "+u.Short(), t.subtle.Render(strings.Join(parts, " · ")), inner)})
		case u.Active == "inactive" && u.FileState == "enabled":
			stopped = append(stopped, entry{id: id, lines: m.itemRow(t.dim.Render("○")+" "+u.Short(), t.dim.Render("not running"), inner)})
		}
	}
	var out []section
	if len(failed) > 0 {
		out = append(out, section{title: "Failed", badge: t.dangerText.Render(strconv.Itoa(len(failed))), entries: failed})
	}
	out = append(out, section{title: "Running", badge: t.subtle.Render(strconv.Itoa(len(running))), entries: running})
	if len(s.timers) > 0 {
		sec := section{title: "Timers", badge: t.subtle.Render(strconv.Itoa(len(s.timers)))}
		for _, tm := range s.timers {
			right := t.dim.Render("not scheduled")
			if !tm.Next.IsZero() {
				right = t.subtle.Render("in " + human.Duration(tm.Next.Sub(m.now)))
			}
			sec.entries = append(sec.entries, entry{id: "timer:" + tm.Name, lines: m.itemRow(t.dim.Render("◷")+" "+strings.TrimSuffix(tm.Name, ".timer"), right, inner)})
		}
		out = append(out, sec)
	}
	if sec, ok := m.cronSection(inner); ok {
		out = append(out, sec)
	}
	if len(stopped) > 0 {
		out = append(out, section{title: "Enabled, not running", badge: t.subtle.Render(strconv.Itoa(len(stopped))), entries: stopped,
			outro: wrap(t.dim.Render("These start at boot or when something asks for them, and usually finish on their own."), inner)})
	}
	return out
}

// --- details ---

func (m *Model) unitDetail(name string, b *detailBuilder) detail {
	t := m.th
	u, ok := m.unit(name)
	if !ok {
		if !m.svc.loaded {
			b.text("Reading systemd…", t.dim)
		} else {
			b.text("This service isn't loaded anymore.", t.subtle)
		}
		return detail{title: strings.TrimSuffix(name, ".service"), body: b.body}
	}
	st, word := health.Info, u.Active
	switch {
	case u.Failed():
		st, word = health.Crit, "failed"
	case u.Running():
		st, word = health.OK, "running"
	case u.Active == "active":
		st, word = health.OK, u.Sub
	}
	summary := ""
	if !u.Since.IsZero() && u.Active != "inactive" {
		summary = "for " + human.Duration(m.now.Sub(u.Since))
	}
	if u.Description != "" && u.Description != u.Name {
		b.text(u.Description, lipgloss.NewStyle())
	}
	if u.Failed() {
		b.text("It stopped with an error ("+orDash(u.Result)+"). The log usually says why.", t.dangerText)
	}
	b.kv("Starts", fileStateWord(u.FileState))
	if u.MainPID > 0 {
		b.kv("PID", strconv.Itoa(u.MainPID))
	}
	if u.HaveMemory {
		b.kv("Memory", human.Bytes(u.Memory))
	}
	if u.CPU > 0 {
		b.kv("CPU time", human.Duration(u.CPU))
	}
	if u.Tasks > 0 {
		b.kv("Tasks", strconv.Itoa(u.Tasks))
	}
	if u.Restarts > 0 {
		b.kv("Restarts", strconv.Itoa(u.Restarts))
	}
	if u.TriggeredBy != "" {
		b.kv("Started by", u.TriggeredBy)
	}
	if u.Fragment != "" {
		b.kv("File", u.Fragment)
	}
	if priv.Vital(u.Name) {
		b.text("The server needs this to stay reachable, so opsie only offers to restart it.", t.subtle)
	}
	return detail{title: u.Short(), headline: headline(t, st, word, summary), body: b.body, fixes: unitFixes(u)}
}

func fileStateWord(s string) string {
	switch s {
	case "enabled":
		return "at boot"
	case "disabled":
		return "only when asked (disabled)"
	case "static":
		return "when something needs it"
	case "masked":
		return "never (masked)"
	case "generated", "transient":
		return "set up by systemd"
	}
	return orDash(s)
}

// unitFixes are the actions that fit a service's state.
func unitFixes(u systemd.Unit) []health.Fix {
	args := map[string]string{"unit": u.Name}
	short := u.Short()
	var out []health.Fix
	restart := health.Fix{Label: "Restart", Op: "unit.restart", Args: args, Done: "Restarted " + short}
	if u.Name == "docker.service" || u.Name == "containerd.service" {
		restart.Danger = true
		restart.Confirm = "Restarting " + short + " stops every container for a moment; ones set to restart come back on their own."
	}
	noRestart := u.Name == "dbus.service" || u.Name == "dbus-broker.service"
	logs := health.Fix{Label: "Logs", Special: "logs", Args: map[string]string{"unit": u.Name}}
	switch {
	case u.Failed():
		out = append(out, restart, logs, health.Fix{Label: "Clear failure", Op: "unit.reset-failed", Args: args, Done: "Cleared " + short})
	case u.Active == "active" || u.Active == "reloading" || u.Active == "activating":
		if !noRestart {
			out = append(out, restart)
		}
		if u.CanReload {
			out = append(out, health.Fix{Label: "Reload", Op: "unit.reload", Args: args, Done: "Reloaded " + short})
		}
		out = append(out, logs)
		if u.CanStop && !u.RefuseStop && !priv.Vital(u.Name) {
			out = append(out, health.Fix{Label: "Stop", Op: "unit.stop", Args: args, Danger: true,
				Confirm: "Stop " + short + "? Anything that depends on it stops working until it's started again.", Done: "Stopped " + short})
		}
	default:
		out = append(out, health.Fix{Label: "Start", Op: "unit.start", Args: args, Done: "Started " + short}, logs)
	}
	switch u.FileState {
	case "enabled":
		if !priv.Vital(u.Name) {
			out = append(out, health.Fix{Label: "Don't start at boot", Op: "unit.disable", Args: args, Danger: true,
				Confirm: short + " won't start when the server boots anymore. It keeps running now.", Done: short + " won't start at boot"})
		}
	case "disabled":
		out = append(out, health.Fix{Label: "Start at boot", Op: "unit.enable", Args: args, Done: short + " starts at boot"})
	}
	return out
}

func (m *Model) timerDetail(name string, b *detailBuilder) detail {
	t := m.th
	for _, tm := range m.svc.timers {
		if tm.Name != name {
			continue
		}
		when := func(at time.Time, future bool) string {
			if at.IsZero() {
				return "—"
			}
			rel := human.Duration(at.Sub(m.now)) + " from now"
			if !future {
				rel = human.Duration(m.now.Sub(at)) + " ago"
			}
			return at.Local().Format("Mon 2 Jan 15:04") + " · " + rel
		}
		b.kv("Next", when(tm.Next, true))
		b.kv("Last", when(tm.Last, false))
		b.kv("Runs", tm.Activates)
		var fixes []health.Fix
		if priv.ValidUnit(tm.Activates) {
			short := strings.TrimSuffix(tm.Activates, ".service")
			fixes = []health.Fix{
				{Label: "Run now", Op: "unit.start", Args: map[string]string{"unit": tm.Activates}, Done: "Started " + short},
				{Label: "Logs", Special: "logs", Args: map[string]string{"unit": tm.Activates}},
				{Label: "Open " + short, Special: "open:services:unit:" + tm.Activates},
			}
		}
		return detail{title: strings.TrimSuffix(tm.Name, ".timer"), headline: headline(t, health.Info, "timer", ""), body: b.body, fixes: fixes}
	}
	b.text("This timer isn't there anymore.", t.subtle)
	return detail{title: "Timer", body: b.body}
}
