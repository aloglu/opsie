package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
)

// Layout breakpoints, in columns. Below mediumFrom everything is one
// column and the tabs move to the bottom, within reach of a thumb: that's
// what a phone in portrait gets.
const (
	minWidth   = 30
	minHeight  = 10
	wideFrom   = 110
	mediumFrom = 72
	headerRows = 2
)

func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "opsie"
	if host := m.snap.Host.Hostname; host != "" {
		v.WindowTitle = "opsie · " + host
	}
	return v
}

// narrow reports whether the phone layout is in use.
func (m *Model) narrow() bool { return m.width < mediumFrom }

func (m *Model) footerRows() int {
	if m.narrow() {
		return 2 // tabs, then hints
	}
	return 1
}

func (m *Model) bodyHeight() int { return m.height - headerRows - m.footerRows() }

func (m *Model) render() string {
	m.hits = m.hits[:0]
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	if m.width < minWidth || m.height < minHeight {
		msg := m.th.subtle.Render("Make the window a little bigger for opsie.")
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			lipgloss.NewStyle().Width(m.width).Align(lipgloss.Center).Render(msg))
	}
	w, h := m.width, m.bodyHeight()
	lines := m.header(w)
	switch {
	case m.help:
		lines = append(lines, m.helpBody(w, h)...)
	case m.modal != nil:
		// The page is drawn behind the popup but can't be tapped through it.
		mark := len(m.hits)
		bg := m.page(w, h, headerRows)
		m.hits = m.hits[:mark]
		lines = append(lines, m.modalBody(bg, w, h, headerRows)...)
	case m.logs != nil:
		lines = append(lines, m.logsBody(w, h, headerRows)...)
	default:
		lines = append(lines, m.page(w, h, headerRows)...)
	}
	if m.narrow() {
		lines = append(lines, m.tabBar(w, m.height-2, true))
	}
	lines = append(lines, m.footer(w))
	return strings.Join(lines, "\n")
}

// page renders the current tab's content, scrolled to keep the selection
// in view, with a scrollbar when it doesn't fit.
func (m *Model) page(w, h, top int) []string {
	build := func(w int) block {
		switch m.tab {
		case tabDocker:
			return m.dockerPage(w)
		case tabSecurity, tabUpdates, tabServices, tabSettings, tabStorage, tabNetwork, tabAgents:
			return m.sectionsPage(m.tab, w)
		case tabProcs:
			return m.procsPage(w)
		}
		return m.overviewPage(w, h)
	}
	l := &m.lists[m.tab]
	content := build(w)
	scrollable := content.height() > h
	if scrollable {
		content = build(w - 1)
	}
	maxScroll := max(content.height()-h, 0)
	if l.follow && content.focus[0] >= 0 {
		f := content.focus
		if f[0] < l.scroll {
			l.scroll = f[0]
		}
		if f[1] > l.scroll+h {
			l.scroll = f[1] - h
		}
	}
	l.follow = false
	l.scroll = min(max(l.scroll, 0), maxScroll)
	m.spots = m.spots[:0]
	for _, hh := range content.hits {
		if hh.id != "" {
			m.spots = append(m.spots, hh)
		}
	}

	out := make([]string, h)
	for i := range out {
		row := l.scroll + i
		line := ""
		if row < content.height() {
			line = content.lines[row]
		}
		if scrollable {
			out[i] = fit(line, w-1) + m.scrollbar(i, h, content.height(), l.scroll)
		} else {
			out[i] = fit(line, w)
		}
	}
	m.placeHits(content.hits, l.scroll, h, top)
	return out
}

// placeHits records content hits that are on screen after scrolling.
func (m *Model) placeHits(hits []hit, scroll, h, top int) {
	for _, hh := range hits {
		y := hh.y - scroll
		if y+hh.h <= 0 || y >= h {
			continue
		}
		if y < 0 {
			hh.h += y
			y = 0
		}
		hh.h = min(hh.h, h-y)
		hh.y = y + top
		m.hits = append(m.hits, hh)
	}
}

func (m *Model) scrollbar(row, h, total, scroll int) string {
	thumb := max(h*h/total, 1)
	maxScroll := total - h
	pos := 0
	if maxScroll > 0 {
		pos = scroll * (h - thumb) / maxScroll
	}
	if row >= pos && row < pos+thumb {
		return m.th.subtle.Render("┃")
	}
	return m.th.borderText.Render("│")
}

// --- header, tabs and footer ---

func (m *Model) badge() string {
	if !m.haveFacts {
		return m.th.dim.Render("checking…")
	}
	crit, warn := health.Issues(m.results)
	switch {
	case crit > 0:
		return m.th.dangerText.Bold(true).Render("✗ " + human.Plural(crit+warn, "issue", "issues"))
	case warn > 0:
		return m.th.warnText.Bold(true).Render("▲ " + human.Plural(warn, "issue", "issues"))
	}
	return m.th.okText.Bold(true).Render("✓ all good")
}

func (m *Model) header(w int) []string {
	t := m.th
	name := " " + t.wordmark()
	if m.env.Dev {
		name += t.dim.Render(" dev")
	}
	clock := m.now.Format("15:04:05")
	if w >= 60 {
		clock = m.now.Format("15:04:05 MST")
	}
	right := m.badge() + "  " + t.subtle.Render(clock) + " "

	line := ""
	if host := m.snap.Host.Hostname; host != "" {
		if l, ok := spread(name+t.dim.Render(" · ")+t.bold.Render(host), right, w); ok {
			line = l
		}
	}
	if line == "" {
		if l, ok := spread(name, right, w); ok {
			line = l
		} else {
			line, _ = spread(name, m.badge()+" ", w)
		}
	}
	// The badge opens the checklist from anywhere.
	bw := lipgloss.Width(m.badge())
	m.hits = append(m.hits, hit{x: w - lipgloss.Width(right), y: 0, w: bw, h: 1, click: func(m *Model) tea.Cmd { return m.switchTab(tabOverview) }})

	var facts []string
	if s := m.snap; m.haveSnap {
		facts = append(facts, "up "+human.Duration(s.Host.Uptime))
		if s.Host.OS != "" {
			facts = append(facts, s.Host.OS)
		}
		facts = append(facts, human.Plural(s.Cores, "core", "cores"), human.Bytes(s.Mem.Total)+" RAM")
		if n := len(s.Sessions); n > 0 {
			facts = append(facts, human.Plural(n, "session", "sessions"))
		}
	}
	if m.narrow() {
		// As many of the facts as fit whole, rather than one cut short.
		for len(facts) > 1 && lipgloss.Width(strings.Join(facts, " · ")) > w-2 {
			facts = facts[:len(facts)-1]
		}
		return []string{line, fit(" "+t.dim.Render(strings.Join(facts, " · ")), w)}
	}
	sub := t.dim.Render(strings.Join(facts, " · ") + " ")
	tabs := m.tabBar(w, 1, false)
	second, ok := spread(tabs, sub, w)
	if !ok {
		second = fit(tabs, w)
	}
	return []string{line, second}
}

// tabBar draws the tabs on row y, as much of their names as the width
// has room for: numbered, plain, shortened, and when even that's too much,
// a strip of the ones around the tab you're on that moves along with it.
// The gaps between tabs are all the same, whatever the names' lengths.
// In the header the bar is as short as it can be, for the facts beside
// it; spread, at the bottom of a phone, it takes the whole row, so each
// tab is as big a target for a thumb as the row allows.
func (m *Model) tabBar(w, y int, spread bool) string {
	t := m.th
	avail := w - 2
	label := func(i int, numbered, short bool) (string, int) {
		tb := tabs[i]
		name := tb.name
		if short {
			name = tb.short
		}
		num := ""
		if numbered {
			num = tabKey(i) + " "
		}
		// The tab you're on is one underlined label, number and all.
		text := t.dim.Render(num) + t.subtle.Render(name)
		if tb.id == m.tab {
			text = t.tab.Render(num + name)
		}
		if mark := m.tabMark(tb.id); mark != "" {
			text += " " + mark
		}
		return text, lipgloss.Width(text)
	}
	type form struct {
		numbered, short bool
		gap             int
	}
	forms := []form{{true, false, 3}, {true, false, 2}, {false, false, 2}, {false, true, 2}}
	if spread {
		// No keyboard, no numbers.
		forms = forms[2:]
	}
	labels := make([]string, len(tabs))
	widths := make([]int, len(tabs))
	first, last, gap := 0, len(tabs)-1, 0
	fits := false
	for _, f := range forms {
		total := f.gap * (len(tabs) - 1)
		for i := range tabs {
			labels[i], widths[i] = label(i, f.numbered, f.short)
			total += widths[i]
		}
		if total <= avail {
			fits, gap = true, f.gap
			break
		}
	}
	more := [2]bool{}
	if !fits {
		// The strip: the tab you're on, then its neighbors on either
		// side for as long as they fit between the arrows.
		gap = 2
		cur := 0
		for i, tb := range tabs {
			if tb.id == m.tab {
				cur = i
			}
		}
		first, last = cur, cur
		used := widths[cur]
		room := avail - 4 // an arrow and a space at each end
		for grew := true; grew; {
			grew = false
			if last+1 < len(tabs) && used+gap+widths[last+1] <= room {
				last++
				used += gap + widths[last]
				grew = true
			}
			if first > 0 && used+gap+widths[first-1] <= room {
				first--
				used += gap + widths[first]
				grew = true
			}
		}
		more = [2]bool{first > 0, last < len(tabs)-1}
	}

	// Every gap is exactly as wide as the others; spare room that doesn't
	// divide among them goes to the ends of the row.
	n := last - first + 1
	gaps := make([]int, max(n-1, 0))
	used := 0
	for i := first; i <= last; i++ {
		used += widths[i]
	}
	left := 1
	if spread || !fits {
		edge := 1
		if !fits {
			edge = 3 // an arrow and a space
		}
		if len(gaps) > 0 {
			// More than six apart and they stop reading as one bar.
			gap = min((w-2*edge-used)/len(gaps), 6)
		}
		left = max((w-used-gap*len(gaps))/2, edge)
	}
	for i := range gaps {
		gaps[i] = gap
	}

	var b strings.Builder
	arrow := func(show bool, glyph string, x, to int) {
		if !show {
			return
		}
		id := tabs[to].id
		m.hits = append(m.hits, hit{x: x - 1, y: y, w: 3, h: 1, click: func(m *Model) tea.Cmd { return m.switchTab(id) }})
	}
	if more[0] {
		b.WriteString(" " + t.navText.Render("‹") + strings.Repeat(" ", left-2))
		arrow(true, "‹", 1, first-1)
	} else {
		b.WriteString(strings.Repeat(" ", left))
	}
	x := left
	for i := first; i <= last; i++ {
		// A tab's target reaches halfway into the gaps beside it.
		before, after := left, w-x-widths[i]
		if i > first {
			before = gaps[i-first-1] - gaps[i-first-1]/2
		}
		if i < last {
			after = gaps[i-first] / 2
		}
		if more[0] && i == first {
			before = 1
		}
		if more[1] && i == last {
			after = 1
		}
		id := tabs[i].id
		m.hits = append(m.hits, hit{x: x - before, y: y, w: before + widths[i] + after, h: 1, click: func(m *Model) tea.Cmd { return m.switchTab(id) }})
		b.WriteString(labels[i])
		x += widths[i]
		if i < last {
			b.WriteString(strings.Repeat(" ", gaps[i-first]))
			x += gaps[i-first]
		}
	}
	if more[1] {
		b.WriteString(strings.Repeat(" ", max(w-2-x, 0)) + t.navText.Render("›"))
		arrow(true, "›", w-2, last+1)
	}
	if spread || !fits {
		return fit(b.String(), w)
	}
	return b.String()
}

type hint struct {
	key, label string
	press      func(m *Model) tea.Cmd
}

func (m *Model) hints() []hint {
	switch {
	case m.help:
		return []hint{{key: "esc", label: "close", press: func(m *Model) tea.Cmd { m.help = false; return nil }}, {key: "↑↓", label: "scroll"}}
	case m.modal != nil:
		back := hint{key: "esc", label: "back", press: func(m *Model) tea.Cmd { return m.modalKey("esc") }}
		if m.modal.stage == stageInput {
			return []hint{{key: "⏎", label: m.modal.fix.Label, press: func(m *Model) tea.Cmd { return m.submitInput() }}, {key: "tab", label: "choose"}, {key: "esc", label: "cancel", press: func(m *Model) tea.Cmd { m.leaveInput(); return nil }}}
		}
		if len(m.buttons()) > 1 {
			arrows := "←→"
			if m.modal.rows > 1 {
				arrows = "←→↑↓"
			}
			return []hint{{key: "⏎", label: "press"}, {key: arrows, label: "choose"}, back}
		}
		return []hint{{key: "⏎", label: "press"}, back}
	case m.logs != nil:
		return m.logsHints()
	}
	out := []hint{
		{key: "⏎", label: "open", press: func(m *Model) tea.Cmd { return m.openSelected() }},
		{key: m.selectKeys(), label: "select"},
		{key: "r", label: "refresh", press: func(m *Model) tea.Cmd { return m.refresh() }},
		{key: "?", label: "help", press: func(m *Model) tea.Cmd { m.help = true; return nil }},
		{key: "q", label: "quit", press: func(m *Model) tea.Cmd { return m.quit() }},
	}
	if m.tab == tabProcs {
		label := "by memory"
		if m.procs.byMem {
			label = "by CPU"
		}
		out = append(out[:2:2], append([]hint{{key: "s", label: label, press: func(m *Model) tea.Cmd { m.setProcSort(!m.procs.byMem); return nil }}}, out[2:]...)...)
	}
	return out
}

func (m *Model) footer(w int) string {
	t := m.th
	y := m.height - 1
	if f := m.flash; f.text != "" && time.Now().Before(f.until) {
		icon, style := "•", t.subtle
		switch f.kind {
		case flashOK:
			icon, style = "✓", t.okText
		case flashError:
			icon, style = "✗", t.dangerText
		}
		m.hits = append(m.hits, hit{x: 0, y: y, w: w, h: 1, click: func(m *Model) tea.Cmd { m.flash = flash{}; return nil }})
		return fit(" "+style.Render(icon+" "+f.text), w)
	}

	right := ""
	if m.updateVersion != "" && m.modal == nil && !m.help && m.logs == nil {
		right = t.infoText.Render("↑ "+m.updateVersion) + " "
	}
	room := w - lipgloss.Width(right)
	var b strings.Builder
	x := 1
	b.WriteString(" ")
	hints := m.hints()
	// On narrow screens, drop the arrow hints first: arrows are obvious,
	// taps aren't.
	if w < 50 {
		var kept []hint
		for _, h := range hints {
			if h.key != "↑↓" && h.key != "←→" {
				kept = append(kept, h)
			}
		}
		hints = kept
	}
	for i, h := range hints {
		text := t.key.Render(h.key) + " " + t.subtle.Render(h.label)
		cell := lipgloss.Width(text)
		gap := 0
		if i > 0 {
			gap = 2
		}
		if x+gap+cell > room {
			break
		}
		b.WriteString(strings.Repeat(" ", gap))
		x += gap
		if h.press != nil {
			m.hits = append(m.hits, hit{x: x, y: y, w: cell, h: 1, click: h.press})
		}
		b.WriteString(text)
		x += cell
	}
	line, ok := spread(b.String(), right, w)
	if !ok {
		line = fit(b.String(), w)
	}
	return line
}

// --- help ---

func (m *Model) helpBody(w, h int) []string {
	t := m.th
	cw := min(w, 64)
	inner := cw - 4
	keyW := 10
	section := func(body []string, title string, rows [][2]string) []string {
		if len(body) > 0 {
			body = append(body, "")
		}
		body = append(body, t.bold.Render(title))
		for _, r := range rows {
			for i, line := range wrap(r[1], inner-keyW) {
				k := ""
				if i == 0 {
					k = r[0]
				}
				body = append(body, t.key.Render(fit(k, keyW))+t.subtle.Render(line))
			}
		}
		return body
	}
	para := func(body []string, title, text string) []string {
		body = append(body, "", t.bold.Render(title))
		for _, line := range wrap(text, inner) {
			body = append(body, t.subtle.Render(line))
		}
		return body
	}
	var body []string
	body = section(body, "Everywhere", [][2]string{
		{"1–9  0", "switch tabs"},
		{"tab", "next tab (shift+tab: previous)"},
		{"↑ ↓  j k", "move through the list; on wide screens, down a column and on to the next"},
		{"← →  h l", "move to the column beside, on wide screens"},
		{"g  G", "first or last item (or home, end)"},
		{"⏎", "open the selected item"},
		{"PgUp PgDn", "scroll the page (or b, f, space, or swipe)"},
		{"s", "on Processes: sort by CPU or memory"},
		{"r", "check everything again now"},
		{"?", "show this help"},
		{"q", "quit"},
	})
	body = para(body, "In a detail view", "A few buttons sit in a row; more become a menu. The arrows choose one: ←→ along a row, ↑↓ to the row above or below; with one row of buttons, ↑↓ scroll. tab also moves along them. ⏎ or space presses the chosen one, 1–9 press one directly, PgUp PgDn scroll, esc (or q) goes back. Anything disruptive asks first with Cancel chosen, and the number keys don't work there. Everything is written to the audit log.")
	body = para(body, "Typing a value", "Some actions ask for something first, like a key or a port. Type or paste it; opsie checks it as you go. ⏎ goes ahead, tab or ↑↓ moves to Cancel, esc cancels.")
	body = section(body, "In the log viewer", [][2]string{
		{"↑ ↓  j k", "scroll a line; scrolling up stops following"},
		{"PgUp PgDn", "scroll a page (or b, f, space)"},
		{"g  home", "jump to the oldest lines"},
		{"G  end", "jump to the newest lines and follow them live"},
		{"w", "wrap long lines or not"},
		{"r", "read the log again"},
		{"esc  q", "back"},
	})
	body = para(body, "Touch", "Tap a line or a card to open it, tap buttons, tabs and the hints at the bottom, and swipe to scroll.")
	body = append(body, "", t.dim.Render(fmt.Sprintf("opsie %s · audit log: %s", m.env.Version, tildePath(m.env.Paths.AuditLog()))))
	card := t.card("Help", "", body, cw, 0, -1, -1, true)
	lines := card.lines
	if len(lines) > h {
		// Short screens scroll the help instead of cutting it off.
		m.helpScroll = min(max(m.helpScroll, 0), len(lines)-h)
		lines = lines[m.helpScroll : m.helpScroll+h]
	} else {
		m.helpScroll = 0
	}
	out := make([]string, 0, h)
	pad := strings.Repeat(" ", (w-cw)/2)
	for _, line := range lines {
		out = append(out, fit(pad+line, w))
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", w))
	}
	return out[:h]
}
