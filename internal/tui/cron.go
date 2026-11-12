package tui

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/cron"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/priv"
)

type cronState struct {
	tab     priv.CronTable
	at      time.Time
	loading bool
}

type cronMsg priv.CronTable

// loadCron reads the crontabs, at most once a minute. Without admin access
// it reads what anyone can, which leaves out other users' own crontabs.
func (m *Model) loadCron() tea.Cmd {
	if m.cron.loading || (!m.cron.at.IsZero() && time.Since(m.cron.at) < time.Minute) {
		return nil
	}
	m.cron.loading = true
	admin := m.admin
	return func() tea.Msg {
		if admin != nil && admin.Alive() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var t priv.CronTable
			if err := admin.Call(ctx, "cron.list", nil, &t); err == nil {
				return cronMsg(t)
			}
		}
		return cronMsg(priv.ReadCron("/"))
	}
}

func (m *Model) cronUpdated(msg cronMsg) {
	m.cron = cronState{tab: priv.CronTable(msg), at: time.Now()}
	m.syncLists()
}

// cronRunning is whether a cron program is running, as far as the
// Services tab knows.
func (m *Model) cronRunning() bool {
	for _, n := range []string{"cron.service", "crond.service", "cronie.service", "fcron.service"} {
		if u, ok := m.unit(n); ok && u.Running() {
			return true
		}
	}
	return false
}

// jobName is a short name for a cron job: the program it runs, after any
// check like `test -e … ||` in front of it.
func jobName(j cron.Job) string {
	cmd := j.Command
	for _, sep := range []string{"||", "&&", ";"} {
		if i := strings.LastIndex(cmd, sep); i >= 0 {
			cmd = cmd[i+len(sep):]
		}
	}
	for _, f := range strings.Fields(cmd) {
		if !strings.Contains(f, "=") {
			return filepath.Base(f)
		}
	}
	return filepath.Base(j.Source)
}

func (m *Model) cronSection(inner int) (section, bool) {
	t := m.th
	c := m.cron
	if c.at.IsZero() || (len(c.tab.Jobs) == 0 && len(c.tab.Periodic) == 0) {
		return section{}, false
	}
	off := !c.tab.Installed || (m.svc.loaded && !m.cronRunning())
	sec := section{title: "Cron", badge: t.subtle.Render(strconv.Itoa(len(c.tab.Jobs) + len(c.tab.Periodic)))}
	for i, j := range c.tab.Jobs {
		right := t.subtle.Render(cron.Describe(j.Schedule))
		if next := cron.Next(j.Schedule, m.now); !next.IsZero() && inner < 50 {
			right = t.subtle.Render("in " + human.Duration(next.Sub(m.now)))
		}
		if off {
			right = t.dim.Render("doesn't run")
		}
		sec.entries = append(sec.entries, entry{id: "cron:job:" + strconv.Itoa(i), lines: m.itemRow(t.dim.Render("◷")+" "+jobName(j), right, inner)})
	}
	for _, f := range c.tab.Periodic {
		right := t.subtle.Render(human.Plural(len(f.Scripts), "script", "scripts"))
		if off {
			right = t.dim.Render("don't run")
		}
		sec.entries = append(sec.entries, entry{id: "cron:every:" + f.Every, lines: m.itemRow(t.dim.Render("▤")+" cron."+f.Every, right, inner)})
	}
	switch {
	case !c.tab.Installed:
		sec.outro = wrap(t.dim.Render("Cron isn't installed, so none of these run. Packages leave these files anyway; most of them do the same work with a systemd timer."), inner)
	case off:
		sec.outro = wrap(t.warnText.Render("Cron is installed but isn't running, so none of these run."), inner)
	}
	return sec, true
}

func (m *Model) cronDetail(id string, b *detailBuilder) detail {
	t := m.th
	tab := m.cron.tab
	plain := lipgloss.NewStyle()
	off := ""
	switch {
	case !tab.Installed:
		off = "Cron isn't installed on this server, so this doesn't run."
	case m.svc.loaded && !m.cronRunning():
		off = "Cron is installed but isn't running, so this doesn't run."
	}
	gone := func() detail {
		b.text("This isn't in the crontabs anymore.", t.subtle)
		return detail{title: "Cron", body: b.body}
	}
	word, st := "scheduled", health.Info
	if off != "" {
		word = "doesn't run"
	}
	if n, ok := strings.CutPrefix(id, "job:"); ok {
		i, err := strconv.Atoi(n)
		if err != nil || i < 0 || i >= len(tab.Jobs) {
			return gone()
		}
		j := tab.Jobs[i]
		if off != "" {
			b.text(off, t.warnText)
		}
		b.kv("Runs", cron.Describe(j.Schedule))
		if d := cron.Describe(j.Schedule); d != j.Schedule {
			b.kv("Schedule", j.Schedule)
		}
		if next := cron.Next(j.Schedule, m.now); !next.IsZero() && off == "" {
			b.kv("Next", next.Local().Format("Mon 2 Jan 15:04")+" · "+human.Duration(next.Sub(m.now))+" from now")
		}
		b.kv("As", orDash(j.User))
		b.kv("File", j.Source+":"+strconv.Itoa(j.Line))
		b.para = true
		b.row(t.bold.Render("Command"))
		b.text(j.Command, plain)
		return detail{title: jobName(j), headline: headline(t, st, word, ""), body: b.body}
	}
	every := strings.TrimPrefix(id, "every:")
	for _, f := range tab.Periodic {
		if f.Every != every {
			continue
		}
		if off != "" {
			b.text(off, t.warnText)
		}
		verb := "runs"
		if off != "" {
			verb = "would run"
		}
		b.text("Cron "+verb+" each script in "+f.Path+" in turn, "+map[string]string{"hourly": "every hour", "daily": "once a day", "weekly": "once a week", "monthly": "once a month"}[every]+".", plain)
		for _, s := range f.Scripts {
			b.row("  " + s)
		}
		return detail{title: "cron." + every, headline: headline(t, st, word, human.Plural(len(f.Scripts), "script", "scripts")), body: b.body}
	}
	return gone()
}
