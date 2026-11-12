package tui

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/daemon"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/history"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/motd"
	"github.com/aloglu/opsie/internal/notify"
	"github.com/aloglu/opsie/internal/priv"
	"github.com/aloglu/opsie/internal/update"
	"github.com/aloglu/opsie/internal/watch"
)

type watcherMsg struct {
	w       health.Watcher
	history []watch.Event
	cfg     config.Config
	cfgErr  error
}

// readWatcher looks at the background service without needing root: its
// unit file and status are readable by everyone. It also rereads the
// settings, so edits to config.toml show up without restarting.
func (m *Model) readWatcher() tea.Cmd {
	env := m.env
	return func() tea.Msg {
		cfg, cfgErr := config.Load(env.Paths.ConfigFile())
		w := health.Watcher{Dev: env.Dev, Mine: env.Version, ConfigPath: env.Paths.ConfigFile(), Notify: cfg.Notify}
		if !env.Dev {
			if home, err := os.UserHomeDir(); err == nil {
				w.Motd = motd.Installed(home, os.Getenv("SHELL"))
			}
			w.Installed = priv.ServiceInstalled()
			if w.Installed {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				out, _ := exec.CommandContext(ctx, "systemctl", "is-active", priv.ServiceUnit).Output()
				cancel()
				w.Active = strings.TrimSpace(string(out)) == "active"
			}
		}
		if st, err := daemon.ReadStatus(env.Paths.Service); err == nil {
			w.HaveStatus = true
			w.Version, w.Checked, w.Every = st.Version, st.Checked, st.Every
			w.SendError, w.Problems, w.LastSent = st.SendError, st.Problems, st.LastSent
		}
		w.Backups = daemon.ReadBackups(env.Paths.Service)
		return watcherMsg{w: w, history: daemon.ReadHistory(env.Paths.Service, 15), cfg: cfg, cfgErr: cfgErr}
	}
}

func (m *Model) watcherUpdated(msg watcherMsg) {
	m.watcher, m.haveWatcher, m.history = msg.w, true, msg.history
	if msg.cfgErr == nil {
		m.env.Config = msg.cfg
		m.applyTheme()
	}
	m.evaluate()
}

// testAlert sends a test message with the current settings.
func (m *Model) testAlert(f health.Fix) tea.Cmd {
	n := m.env.Config.Notify
	host := m.snap.Host.Hostname
	if m.modal != nil {
		m.modal.stage, m.modal.err, m.modal.started = stageRunning, "", time.Now()
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := notify.Send(ctx, n, notify.Message{Title: watch.Title(host), Lines: []string{"✓ Test alert from opsie. Alerts will reach you here."}, Level: notify.LevelOK})
		f.Done = "Test alert sent"
		return fixDoneMsg{fix: f, err: err}
	}
}

// sendWeekly sends this week's summary now, built from what the
// background checks recorded.
func (m *Model) sendWeekly(f health.Fix) tea.Cmd {
	n := m.env.Config.Notify
	dir := m.env.Paths.Service
	snap, results, now := m.snap, m.results, m.now
	nightly, runs := m.env.Config.Backup.Nightly, m.watcher.Backups
	if m.modal != nil {
		m.modal.stage, m.modal.err, m.modal.started = stageRunning, "", time.Now()
	}
	return func() tea.Msg {
		samples, _ := history.Read(dir, now.Add(-7*24*time.Hour))
		msg := daemon.Weekly(snap.Host.Hostname, now, snap, results, daemon.ReadHistory(dir, 500), samples, nightly, runs)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		f.Done = "Sent this week's summary"
		return fixDoneMsg{fix: f, err: notify.Send(ctx, n, msg)}
	}
}

// historyLines are the recent events for the background checks' detail.
func (m *Model) historyLines(b *detailBuilder) {
	t := m.th
	if len(m.history) == 0 {
		return
	}
	b.text("Recent", t.bold)
	b.para = false
	for _, e := range m.history {
		when := human.Duration(m.now.Sub(e.At)) + " ago"
		if m.now.Sub(e.At) > 24*time.Hour {
			when = e.At.Local().Format("2 Jan 15:04")
		}
		b.row(t.dim.Render(fit(when, 10)) + " " + e.Line())
	}
}

// loginSummary adds or removes `opsie motd` in your login file. It runs
// as you and only touches the lines opsie added.
func (m *Model) loginSummary(on bool) tea.Cmd {
	m.modal = nil
	home, err := os.UserHomeDir()
	if err != nil {
		return m.setFlash(err.Error(), flashError, 5*time.Second)
	}
	shell := os.Getenv("SHELL")
	if on {
		exe, err := update.Executable()
		if err == nil {
			var path string
			path, err = motd.Install(home, shell, exe)
			if err == nil {
				return tea.Batch(m.setFlash("The summary shows when you log in (in "+tildePath(path)+")", flashOK, 6*time.Second), m.readWatcher())
			}
		}
		return m.setFlash("Couldn't set it up: "+err.Error(), flashError, 6*time.Second)
	}
	if _, _, err := motd.Remove(home, shell); err != nil {
		return m.setFlash("Couldn't remove it: "+err.Error(), flashError, 6*time.Second)
	}
	return tea.Batch(m.setFlash("The summary won't show at login anymore", flashOK, 5*time.Second), m.readWatcher())
}
