package tui

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	logsEvery = 2 * time.Second
	logsTail  = "1000"
)

// logView follows a container's log.
type logView struct {
	// container is the container's name, or the unit's when unit is set.
	container string
	unit      bool
	lines     []logLine
	err       string
	loaded    bool
	// follow keeps the newest lines in view; scrolling up turns it off.
	follow bool
	// offset is how many rows the view is scrolled up from the bottom.
	offset int
	wrap   bool
	busy   bool
	gen    int
}

type logLine struct {
	at   time.Time
	text string
}

type (
	logsMsg struct {
		container string
		text      string
		err       error
	}
	logsDueMsg int
)

func (m *Model) openLogs(container string) tea.Cmd {
	m.modal = nil
	m.logs = &logView{container: container, follow: true, wrap: true}
	return m.fetchLogs()
}

func (m *Model) openUnitLogs(unit string) tea.Cmd {
	m.modal = nil
	m.logs = &logView{container: unit, unit: true, follow: true, wrap: true}
	return m.fetchLogs()
}

func (m *Model) fetchLogs() tea.Cmd {
	v := m.logs
	if v == nil || v.busy || m.admin == nil || !m.admin.Alive() {
		return nil
	}
	v.busy = true
	admin, name := m.admin, v.container
	op, args := "docker.logs", map[string]string{"container": name, "tail": logsTail}
	if v.unit {
		op, args = "unit.logs", map[string]string{"unit": name, "tail": logsTail}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var text string
		err := admin.Call(ctx, op, args, &text)
		return logsMsg{container: name, text: text, err: err}
	}
}

func (m *Model) logsUpdated(msg logsMsg) tea.Cmd {
	v := m.logs
	if v == nil || v.container != msg.container {
		return nil
	}
	v.busy, v.loaded = false, true
	if msg.err != nil {
		v.err = msg.err.Error()
	} else {
		v.err = ""
		v.lines = parseLogs(msg.text)
	}
	v.gen++
	gen := v.gen
	return tea.Tick(logsEvery, func(time.Time) tea.Msg { return logsDueMsg(gen) })
}

var stampRe = regexp.MustCompile(`^\[?(\d{4}[-/]\d{2}[-/]\d{2}[T ])?\d{2}:\d{2}:\d{2}`)

// ownStamps reports whether most lines start with their own timestamp.
func ownStamps(lines []logLine) bool {
	if len(lines) == 0 {
		return false
	}
	n := 0
	for _, l := range lines {
		if stampRe.MatchString(l.text) {
			n++
		}
	}
	return n*5 >= len(lines)*4
}

// parseLogs splits `docker logs --timestamps` output, dropping escape
// codes so a program's colors or cursor moves can't break the layout.
func parseLogs(text string) []logLine {
	raw := strings.Split(strings.TrimRight(text, "\n"), "\n")
	out := make([]logLine, 0, len(raw))
	for _, line := range raw {
		if line == "" {
			continue
		}
		var l logLine
		if ts, rest, ok := strings.Cut(line, " "); ok {
			if at, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				l.at, line = at, rest
			}
		}
		l.text = strings.ReplaceAll(ansi.Strip(strings.TrimRight(line, "\r")), "\t", "    ")
		out = append(out, l)
	}
	return out
}

func (m *Model) logsKey(key string, page int) tea.Cmd {
	v := m.logs
	switch key {
	case "esc", "q", "backspace":
		m.logs = nil
	case "up", "k":
		v.offset++
		v.follow = false
	case "down", "j":
		v.offset--
	case "pgup", "b":
		v.offset += page
		v.follow = false
	case "pgdown", "f", "space":
		v.offset -= page
	case "home", "g":
		v.offset = 1 << 30
		v.follow = false
	case "end", "G":
		v.offset, v.follow = 0, true
	case "w":
		v.wrap = !v.wrap
	case "r":
		return m.fetchLogs()
	}
	if v.offset <= 0 {
		v.offset, v.follow = 0, true
	}
	return nil
}

func (m *Model) logsHints() []hint {
	v := m.logs
	out := []hint{{key: "esc", label: "back", press: func(m *Model) tea.Cmd { m.logs = nil; return nil }}}
	// One of the two jumps, whichever goes somewhere: back to the newest
	// lines when scrolled up, to the oldest when following.
	if v.follow {
		out = append(out, hint{key: "g", label: "oldest", press: func(m *Model) tea.Cmd { return m.logsKey("g", 0) }})
	} else {
		out = append(out, hint{key: "G", label: "newest", press: func(m *Model) tea.Cmd { return m.logsKey("G", 0) }})
	}
	wrapLabel := "wrap"
	if v.wrap {
		wrapLabel = "no wrap"
	}
	return append(out,
		hint{key: "w", label: wrapLabel, press: func(m *Model) tea.Cmd { return m.logsKey("w", 0) }},
		hint{key: "r", label: "reload", press: func(m *Model) tea.Cmd { return m.logsKey("r", 0) }},
		hint{key: "↑↓", label: "scroll"},
	)
}

// logStyle picks out lines that look like errors or warnings.
func (m *Model) logStyle(text string) lipgloss.Style {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "error"), strings.Contains(lower, "fatal"), strings.Contains(lower, "panic"), strings.Contains(lower, "exception"):
		return m.th.dangerText
	case strings.Contains(lower, "warn"):
		return m.th.warnText
	}
	return lipgloss.NewStyle()
}

func (m *Model) logsBody(w, h, top int) []string {
	t := m.th
	v := m.logs
	inner := w - 4
	// Show Docker's timestamps unless the program writes its own, so the
	// time isn't there twice.
	stamp := inner >= 40 && !ownStamps(v.lines)
	const stampW = 9 // "15:04:05 "

	// Render every line to rows of the card's width.
	var rows []string
	for _, l := range v.lines {
		prefix, indent := "", ""
		textW := inner
		if stamp {
			prefix = t.dim.Render(fit(l.at.Local().Format("15:04:05"), stampW))
			if l.at.IsZero() {
				prefix = strings.Repeat(" ", stampW)
			}
			indent = strings.Repeat(" ", stampW)
			textW -= stampW
		}
		style := m.logStyle(l.text)
		if !v.wrap {
			rows = append(rows, prefix+style.Render(fit(l.text, textW)))
			continue
		}
		if !stamp {
			// A hanging indent tells continued lines from new ones.
			indent = "  "
			textW -= 2
		}
		for i, part := range wrap(l.text, max(textW, 1)) {
			if i == 0 {
				rows = append(rows, prefix+style.Render(part))
			} else {
				rows = append(rows, indent+style.Render(part))
			}
		}
	}

	view := max(h-2, 1)
	maxOffset := max(len(rows)-view, 0)
	if v.follow {
		v.offset = 0
	}
	v.offset = min(max(v.offset, 0), maxOffset)
	start := max(len(rows)-view-v.offset, 0)
	body := rows[start:min(start+view, len(rows))]
	switch {
	case !v.loaded:
		body = []string{t.dim.Render("Loading…")}
	case v.err != "" && len(v.lines) == 0:
		body = wrap("Couldn't read the log: "+v.err, inner)
	case len(v.lines) == 0:
		body = []string{t.dim.Render("The log is empty.")}
	}
	// Short logs sit at the bottom, like a terminal.
	if pad := view - len(body); pad > 0 && v.loaded && len(v.lines) > 0 {
		body = append(make([]string, pad), body...)
	}

	badge := t.okText.Render("● live")
	if !v.follow {
		badge = t.warnText.Render("‖ paused")
		if v.offset > 0 {
			badge = t.warnText.Render("‖ " + strconv.Itoa(min(v.offset, 9999)) + " up")
		}
	}
	if v.err != "" && len(v.lines) > 0 {
		badge = t.dangerText.Render("✗ stale")
	}
	title := "Logs · " + v.container
	if v.unit {
		title = "Logs · " + strings.TrimSuffix(v.container, ".service")
	}
	card := t.card(title, badge, body, w, view, -1, -1, true)
	// Tapping the badge goes back to live.
	m.hits = append(m.hits, hit{x: w - lipgloss.Width(badge) - 3, y: top, w: lipgloss.Width(badge) + 2, h: 1, click: func(m *Model) tea.Cmd { return m.logsKey("G", 0) }})
	out := make([]string, 0, h)
	for _, line := range card.lines {
		out = append(out, fit(line, w))
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", w))
	}
	return out[:h]
}
