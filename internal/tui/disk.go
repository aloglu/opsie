package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/priv"
)

// duFresh is how long a folder's measurement is reused.
const duFresh = 10 * time.Minute

type duEntry struct {
	loading bool
	u       priv.DirUsage
	err     string
	at      time.Time
}

type cleanState struct {
	loading bool
	c       priv.Cleanable
	err     string
	at      time.Time
}

type (
	duMsg struct {
		path string
		u    priv.DirUsage
		err  error
	}
	cleanMsg struct {
		c   priv.Cleanable
		err error
	}
)

// explore opens the folder sizes of path.
func (m *Model) explore(path string, again bool) tea.Cmd {
	m.modal = &modal{key: "du:" + path}
	if again {
		delete(m.du, path)
	}
	return m.loadDu(path)
}

func (m *Model) loadDu(path string) tea.Cmd {
	if m.du == nil {
		m.du = map[string]duEntry{}
	}
	if e, ok := m.du[path]; ok && (e.loading || time.Since(e.at) < duFresh) {
		return nil
	}
	if m.admin == nil || !m.admin.Alive() {
		m.du[path] = duEntry{err: "Measuring every folder needs admin access, which is locked.", at: time.Now()}
		return nil
	}
	m.du[path] = duEntry{loading: true}
	admin := m.admin
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		var u priv.DirUsage
		err := admin.Call(ctx, "disk.usage", map[string]string{"path": path}, &u)
		return duMsg{path: path, u: u, err: err}
	}
}

func (m *Model) duUpdated(msg duMsg) {
	e := duEntry{u: msg.u, at: time.Now()}
	if msg.err != nil {
		e.err = msg.err.Error()
	}
	m.du[msg.path] = e
}

func (m *Model) loadClean() tea.Cmd {
	if m.clean.loading || (!m.clean.at.IsZero() && time.Since(m.clean.at) < time.Minute) {
		return nil
	}
	if m.admin == nil || !m.admin.Alive() {
		m.clean = cleanState{err: "Finding what can be cleaned needs admin access, which is locked.", at: time.Now()}
		return nil
	}
	m.clean.loading = true
	admin := m.admin
	return tea.Batch(m.pollDocker(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var c priv.Cleanable
		err := admin.Call(ctx, "disk.cleanable", nil, &c)
		return cleanMsg{c: c, err: err}
	})
}

func (m *Model) cleanUpdated(msg cleanMsg) {
	m.clean = cleanState{c: msg.c, at: time.Now()}
	if msg.err != nil {
		m.clean.err = msg.err.Error()
	}
}

// forgetDisk drops measurements after something freed space.
func (m *Model) forgetDisk() {
	m.du = nil
	m.clean = cleanState{}
}

// duNote explains folders that are better cleaned another way.
func duNote(path string) (string, *health.Fix) {
	switch {
	case path == "/var/lib/docker" || strings.HasPrefix(path, "/var/lib/docker/"):
		return "Docker's images, containers and volumes. Don't delete files here; Docker's own clean-up is safe.", &health.Fix{Label: "Docker clean-up", Special: "open:prune"}
	case path == "/var/lib/containerd" || strings.HasPrefix(path, "/var/lib/containerd/"):
		return "Container images that containerd keeps for Docker. Docker's own clean-up frees them.", &health.Fix{Label: "Docker clean-up", Special: "open:prune"}
	case strings.HasPrefix(path, "/var/log/journal"), path == "/var/log":
		return "Logs. The system log can be trimmed safely.", &health.Fix{Label: "Free up space", Special: "clean"}
	case strings.HasPrefix(path, "/var/cache/apt"):
		return "Packages apt downloaded to install. They can be cleared safely.", &health.Fix{Label: "Free up space", Special: "clean"}
	case path == "/proc" || path == "/sys" || path == "/dev" || path == "/run":
		return "Not files on the disk: the system shows these in memory.", nil
	}
	return "", nil
}

func (m *Model) duDetail(path string, b *detailBuilder) detail {
	t := m.th
	title := "Space in " + tildePath(path)
	e, ok := m.du[path]
	var fixes []health.Fix
	if note, fix := duNote(path); note != "" {
		b.text(note, t.subtle)
		if fix != nil {
			fixes = append(fixes, *fix)
		}
	}
	switch {
	case !ok || e.loading:
		b.text("Measuring every folder… This reads the whole disk at low priority, so it can take a minute on a big one.", t.dim)
		return detail{title: title, headline: headline(t, health.Info, "measuring", ""), body: b.body, fixes: append(fixes, m.upFix(path)...)}
	case e.err != "":
		b.text(e.err, t.dangerText)
		return detail{title: title, body: b.body, fixes: append(fixes, m.upFix(path)...)}
	}
	u := e.u
	// Folders under a megabyte only make the list longer.
	var big []priv.DirSize
	small, smallSize := u.More, u.MoreSize
	for _, c := range u.Children {
		if c.Size < 1<<20 {
			small++
			smallSize += c.Size
		} else {
			big = append(big, c)
		}
	}
	// Everything here drawn as its share of the folder: the folders, the
	// files beside them, and the small folders together.
	if b.para || len(b.body) > 0 {
		b.body = append(b.body, "")
	}
	b.para = false
	row := func(name string, size uint64, style lipgloss.Style) {
		line, _ := m.shareRow(style.Render(name), size, u.Size, b.inner)
		b.body = append(b.body, line)
	}
	for _, c := range big {
		row(filepath.Base(c.Path)+"/", c.Size, lipgloss.NewStyle())
	}
	if u.Files > 0 {
		row("files here", u.Files, t.subtle)
	}
	if small > 0 {
		row(fmt.Sprintf("%d smaller", small), smallSize, t.subtle)
	}
	if len(u.Children) == 0 {
		b.text("No folders inside, only files.", t.subtle)
	}
	b.text("Measured "+human.Duration(m.now.Sub(e.at))+" ago. Disks mounted inside this one aren't counted.", t.dim)
	// Each folder is a button that opens it.
	var folders []health.Fix
	for _, c := range big {
		folders = append(folders, health.Fix{Label: filepath.Base(c.Path) + "/", Special: "explore:" + c.Path})
	}
	fixes = append(folders, fixes...)
	fixes = append(fixes, m.upFix(path)...)
	fixes = append(fixes, health.Fix{Label: "Measure again", Special: "explore!:" + path})
	return detail{title: title, headline: headline(t, health.Info, human.Bytes(u.Size), ""), body: b.body, fixes: fixes}
}

func (m *Model) upFix(path string) []health.Fix {
	if path == "/" {
		return nil
	}
	parent := filepath.Dir(path)
	return []health.Fix{{Label: "‹ " + tildePath(parent), Special: "explore:" + parent}}
}

func (m *Model) cleanDetail(b *detailBuilder) detail {
	t := m.th
	plain := lipgloss.NewStyle()
	c := m.clean
	if c.err != "" {
		b.text(c.err, t.dangerText)
		return detail{title: "Free up space", body: b.body}
	}
	if c.at.IsZero() || c.loading {
		b.text("Looking for what can go…", t.dim)
		return detail{title: "Free up space", body: b.body}
	}
	b.text("Only things that are safe to remove: nothing here is your data, and each one asks first if it can't be undone.", plain)
	var fixes []health.Fix
	var total uint64

	journal := human.Bytes(c.c.Journal)
	b.kv("System log", journal)
	if c.c.Journal > 600<<20 {
		total += c.c.Journal - 500<<20
		fixes = append(fixes, health.Fix{Label: "Trim the system log", Op: "journal.vacuum", Done: "Trimmed the system log",
			Danger: true, Confirm: "Delete the oldest system log entries until the log is 500 MB? Older history from journalctl is gone after this."})
	}
	b.kv("Downloads", human.Bytes(c.c.AptCache)+t.dim.Render(" · packages apt already installed"))
	if c.c.AptCache > 10<<20 {
		total += c.c.AptCache
		fixes = append(fixes, health.Fix{Label: "Clear downloaded packages", Op: "apt.clean", Done: "Cleared downloaded packages"})
	}
	if n := len(c.c.Autoremove); n > 0 {
		b.kv("Unneeded", human.Plural(n, "package", "packages")+t.dim.Render(" · "+human.Bytes(c.c.AutoremoveSize)))
		b.text(strings.Join(c.c.Autoremove, ", "), t.subtle)
		total += c.c.AutoremoveSize
		fixes = append(fixes, health.Fix{Label: "Remove " + human.Plural(n, "unneeded package", "unneeded packages"), Op: "apt.autoremove",
			Args: map[string]string{"expect": strings.Join(c.c.Autoremove, " ")}, Danger: true, Done: "Removed unneeded packages",
			Confirm: "Remove these packages, which were installed only for something that's gone now: " + strings.Join(c.c.Autoremove, ", ") + "? Old kernels are among them when there are any; the one running now is never removed."})
	} else {
		b.kv("Unneeded", "no packages")
	}
	for _, u := range m.dock.df {
		if r := reclaimable(u); r != "" && u.Type != "Local Volumes" {
			b.kv("Docker", r+t.dim.Render(" in unused images and build cache"))
			fixes = append(fixes, health.Fix{Label: "Docker clean-up", Special: "open:prune"})
			break
		}
	}
	if len(fixes) == 0 {
		b.text("There's nothing worth removing right now. To see what takes up the space, look at what's using it.", t.subtle)
	} else if total > 0 {
		b.text("About "+human.Bytes(total)+" can go, not counting Docker.", t.subtle)
	}
	fixes = append(fixes, health.Fix{Label: "What's using space", Special: "explore:/"})
	return detail{title: "Free up space", body: b.body, fixes: fixes}
}
