package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/update"
)

// latestKnown is the newest release GitHub has told this opsie about,
// when it said so, and whether it's newer than the one running. A build
// from a checkout isn't a release, so nothing is newer than it.
func (m *Model) latestKnown() (latest string, at time.Time, newer bool) {
	latest, at = update.Last(m.env.Paths.MetaFile())
	return latest, at, update.Newer(m.env.Version, latest)
}

// versionWord is the version in Settings' list, with what's known about
// newer ones.
func (m *Model) versionWord() string {
	t := m.th
	latest, at, newer := m.latestKnown()
	switch {
	case m.env.Dev:
		return t.subtle.Render(m.env.Version) + t.dim.Render(" · dev build")
	case newer:
		return t.subtle.Render(m.env.Version) + t.dim.Render(" · ") + t.infoText.Render(latest+" is out")
	case !at.IsZero() && latest != "":
		return t.subtle.Render(m.env.Version) + t.dim.Render(" · newest")
	}
	return t.subtle.Render(m.env.Version)
}

func (m *Model) versionDetail(b *detailBuilder) detail {
	t := m.th
	plain := lipgloss.NewStyle()
	latest, at, newer := m.latestKnown()
	d := detail{title: "opsie " + m.env.Version}
	check := health.Fix{Label: "Check now", Special: "update:check"}
	switch {
	case m.env.Dev:
		d.headline = headline(t, health.Info, "dev build", "")
		b.text("This opsie was built from a checkout. It keeps its files apart from an installed opsie and never updates itself.", plain)
	case newer:
		d.headline = headline(t, health.Info, latest+" is out", "")
		b.text("Updating downloads the new version from GitHub, checks it against its published checksum, and starts it in place of this one.", plain)
		d.fixes = append(d.fixes, health.Fix{Label: "Update opsie", Special: "update"})
	case latest != "":
		d.headline = headline(t, health.OK, "newest version", "")
		b.text("opsie asks GitHub once a day whether a newer version is out, and says so here and in the corner of the screen when one is.", plain)
	default:
		b.text("opsie asks GitHub once a day whether a newer version is out, and says so here and in the corner of the screen when one is.", plain)
	}
	b.kv("Running", m.env.Version)
	if latest != "" {
		b.kv("Newest", latest)
	}
	checked := t.dim.Render("never")
	if !at.IsZero() {
		checked = human.Duration(m.now.Sub(at)) + " ago"
	}
	b.kv("Checked", checked)
	if exe, err := update.Executable(); err == nil {
		b.kv("Installed", tildePath(exe))
	}
	d.body = b.body
	d.fixes = append(d.fixes, check)
	return d
}

// checkVersion asks GitHub for the newest release now, with the popup
// that asked left open for the answer.
func (m *Model) checkVersion() tea.Cmd {
	if m.modal != nil {
		m.modal.stage, m.modal.err, m.modal.started = stageRunning, "", time.Now()
	}
	path := m.env.Paths.MetaFile()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		latest, err := update.Check(ctx, path)
		return versionCheckedMsg{latest: latest, err: err}
	}
}

func (m *Model) versionChecked(msg versionCheckedMsg) tea.Cmd {
	if m.modal != nil && m.modal.key == "set:version" {
		m.modal.stage, m.modal.focus = stageView, 0
	}
	if msg.err != nil {
		if m.modal != nil && m.modal.key == "set:version" {
			m.modal.err = "Couldn't check: " + msg.err.Error()
			return nil
		}
		return m.setFlash("Couldn't check: "+msg.err.Error(), flashError, 6*time.Second)
	}
	switch {
	case m.env.Dev:
		return m.setFlash("The newest release is "+msg.latest, flashOK, 5*time.Second)
	case update.Newer(m.env.Version, msg.latest):
		m.updateVersion = msg.latest
		m.evaluate()
		return m.setFlash("opsie "+msg.latest+" is out", flashOK, 5*time.Second)
	}
	return m.setFlash("opsie "+m.env.Version+" is the newest", flashOK, 5*time.Second)
}
