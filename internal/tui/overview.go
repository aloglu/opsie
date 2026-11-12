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

// overviewPage builds the Overview tab, letting the charts grow into any
// spare height on screens wide enough to show several cards at once.
func (m *Model) overviewPage(w, h int) block {
	m.grow = 0
	content := m.overview(w)
	if spare := h - content.height(); spare > 0 && w >= mediumFrom {
		// Medium layouts have two rows of charts to share the room.
		rows := 1
		if w < wideFrom {
			rows = 2
		}
		m.grow = min(spare/rows, 12)
		content = m.overview(w)
	}
	return content
}

// overview lays the cards out for the width: three columns on wide
// screens, two on medium ones, and a single column on phones.
func (m *Model) overview(w int) block {
	switch {
	case w >= wideFrom:
		cols := split(w, 3, 1)
		metrics := m.row(1, cols, m.cpuCard, m.memCard, m.netCard)
		lw := (w - 1) * 3 / 5
		rw := w - 1 - lw
		right := vstack(rw, m.cardBlock(m.systemCard(rw-4), rw, 0), m.cardBlock(m.diskCard(rw-4), rw, 0))
		left := m.cardBlock(m.healthCard(lw-4), lw, right.height()-2)
		return vstack(w, metrics, hstack(1, left, right))
	case w >= mediumFrom:
		cols := split(w, 2, 1)
		return vstack(w,
			m.cardBlock(m.healthCard(w-4), w, 0),
			m.row(1, cols, m.cpuCard, m.memCard),
			m.row(1, cols, m.netCard, m.diskCard),
			m.cardBlock(m.systemCard(w-4), w, 0),
		)
	}
	// A phone gets the readings as one card of bars, a line each, so they
	// and the top of the checklist are on screen together; their charts
	// are a tap away.
	return vstack(w,
		m.cardBlock(m.vitalsCard(w-4), w, 0),
		m.cardBlock(m.healthCard(w-4), w, 0),
		m.cardBlock(m.systemCard(w-4), w, 0),
	)
}

// vitalsCard is every reading in a line: CPU, memory, swap, each disk and
// the network. A tap on one opens its history.
func (m *Model) vitalsCard(inner int) cardSpec {
	t := m.th
	s := cardSpec{title: "Now", selected: -1, focus: noFocus()}
	if !m.haveSnap {
		s.body = []string{t.dim.Render("Sampling…")}
		return s
	}
	th := m.env.Config.Thresholds
	snap := m.snap
	const labelW, pctW = 5, 4
	row := func(label string, pct float64, c colorT, extra, open string, tab tabID) {
		line := t.subtle.Render(fit(label, labelW)) + " "
		num := fmt.Sprintf("%*.0f%%", pctW-1, pct)
		extraW := 0
		if inner >= 38 {
			extraW = 10
		}
		if gw := inner - labelW - 1 - 1 - pctW - extraW; gw >= 4 {
			line += t.gauge(pct, gw, c) + " "
		}
		line += lipgloss.NewStyle().Foreground(c).Bold(true).Render(num)
		if extraW > 0 {
			line += t.dim.Render(fmt.Sprintf("%*s", extraW, extra))
		}
		s.hits = append(s.hits, hit{x: -2, y: len(s.body), w: inner + 4, h: 1, click: func(m *Model) tea.Cmd {
			if tab != tabOverview {
				return m.switchTab(tab)
			}
			return m.openItem(tabOverview, open)
		}})
		s.body = append(s.body, line)
	}
	row("CPU", snap.CPU, t.level(t.cpu, snap.CPU, 80, 95), fmt.Sprintf("load %.2f", snap.Load[0]), "load", tabOverview)
	row("RAM", snap.Mem.Percent, t.level(t.mem, snap.Mem.Percent, th.MemoryWarn, th.MemoryCrit), human.Bytes(snap.Mem.Used)+"/"+human.Bytes(snap.Mem.Total), "memory", tabOverview)
	if sw := snap.Swap; sw.Total > 0 {
		row("Swap", sw.Percent, t.level(t.mem, sw.Percent, th.SwapWarn, 95), human.Bytes(sw.Used)+"/"+human.Bytes(sw.Total), "swap", tabOverview)
	}
	for _, d := range snap.Disks {
		row(d.Mount, d.Percent, t.level(t.disk, d.Percent, th.DiskWarn, th.DiskCrit), human.Bytes(d.Free)+" free", "", tabStorage)
	}
	if n := snap.Net; n.Ready {
		paint := func(c colorT, s string) string { return lipgloss.NewStyle().Foreground(c).Bold(true).Render(s) }
		line := t.subtle.Render(fit("Net", labelW)) + " " + paint(t.rx, "↓") + " " + human.Rate(n.RxRate) + "  " + paint(t.tx, "↑") + " " + human.Rate(n.TxRate)
		s.hits = append(s.hits, hit{x: -2, y: len(s.body), w: inner + 4, h: 1, click: func(m *Model) tea.Cmd { return m.switchTab(tabNetwork) }})
		s.body = append(s.body, line)
	}
	return s
}

// row renders cards side by side at the same height.
func (m *Model) row(gap int, widths []int, cards ...func(inner int) cardSpec) block {
	specs := make([]cardSpec, len(cards))
	tallest := 0
	for i, card := range cards {
		specs[i] = card(widths[i] - 4)
		tallest = max(tallest, len(specs[i].body))
	}
	blocks := make([]block, len(specs))
	for i, spec := range specs {
		blocks[i] = m.cardBlock(spec, widths[i], tallest)
	}
	return hstack(gap, blocks...)
}

// cardSpec is a card's content before it's boxed. Hits and focus are in
// body coordinates.
type cardSpec struct {
	title, badge string
	body         []string
	selected     int
	// selEnd is the line after the selected item, which can wrap.
	selEnd int
	// band is how far into the row the selection band goes; 0 is all the
	// way.
	band  int
	hits  []hit
	focus [2]int
	// open is the item a tap on the card opens, on openTab.
	open    string
	openTab tabID
	// focused is a card that is itself the selection: its border and
	// title are drawn in the selection's color.
	focused bool
}

func (m *Model) cardBlock(s cardSpec, w, minBody int) block {
	b := m.th.cardBand(s.title, s.badge, s.body, w, minBody, s.selected, s.selEnd, s.band, s.focused)
	if s.open != "" {
		id, tab := s.open, s.openTab
		h := hit{x: 0, y: 0, w: w, h: b.height(), click: func(m *Model) tea.Cmd { return m.openItem(tab, id) }}
		if tab != tabOverview {
			// Overview's cards open checklist results; they aren't items of
			// their own.
			h.id = id
		}
		b.hits = append(b.hits, h)
	}
	for _, h := range s.hits {
		h.x += 2
		h.y++
		b.hits = append(b.hits, h)
	}
	if s.focus[0] >= 0 {
		b.focus = [2]int{s.focus[0] + 1, s.focus[1] + 1}
		if s.focus[0] == 0 {
			b.focus[0] = 0
		}
		if s.focus[1] >= len(s.body) {
			b.focus[1] = b.height()
		}
	}
	return b
}

func noFocus() [2]int { return [2]int{-1, -1} }

// --- cards ---

func (m *Model) healthCard(inner int) cardSpec {
	t := m.th
	l := &m.lists[tabOverview]
	s := cardSpec{title: "Health", badge: m.badge(), selected: -1, focus: noFocus()}
	if len(m.results) == 0 {
		s.body = []string{t.dim.Render("Running checks…")}
		return s
	}
	for i, r := range m.results {
		title := r.Title
		titleStyle := lipgloss.NewStyle().Foreground(t.text)
		summaryStyle := t.subtle
		if r.Status >= health.Warn {
			summaryStyle = lipgloss.NewStyle().Foreground(t.statusColor(r.Status))
		}
		left := t.statusIcon(r.Status) + " " + titleStyle.Render(title)
		start := len(s.body)
		if line, ok := spread(left, summaryStyle.Render(r.Summary), inner); ok {
			s.body = append(s.body, line)
		} else {
			// Not enough room on one line: the summary goes underneath
			// rather than being cut off.
			s.body = append(s.body, fit(left, inner), "  "+fit(summaryStyle.Render(r.Summary), inner-2))
		}
		if i == l.sel {
			s.selected, s.selEnd = start, len(s.body)
			s.focus = [2]int{start, len(s.body)}
			if i == 0 {
				s.focus[0] = 0
			}
			if i == len(m.results)-1 {
				s.focus[1] = len(s.body) + 1
			}
		}
		id := r.ID
		s.hits = append(s.hits, hit{x: -2, y: start, w: inner + 4, h: len(s.body) - start, id: id, click: func(m *Model) tea.Cmd {
			return m.openItem(tabOverview, id)
		}})
	}
	return s
}

func (m *Model) cpuCard(inner int) cardSpec {
	t := m.th
	s := cardSpec{title: "CPU", selected: -1, focus: noFocus(), open: "load"}
	if !m.haveSnap {
		s.body = []string{t.dim.Render("Sampling…")}
		return s
	}
	s.badge = lipgloss.NewStyle().Foreground(t.level(t.cpu, m.snap.CPU, 80, 95)).Bold(true).Render(fmt.Sprintf("%.0f%%", m.snap.CPU))
	s.body = t.chart(m.cpu, inner, 4+m.grow, 100, func(v float64) colorT { return t.level(t.cpu, v, 80, 95) })
	l := m.snap.Load
	loadText := t.subtle.Render("load ") + fmt.Sprintf("%.2f %.2f %.2f", l[0], l[1], l[2])
	line, ok := spread(loadText, t.dim.Render(human.Plural(m.snap.Cores, "core", "cores")), inner)
	if !ok {
		line = fit(loadText, inner)
	}
	s.body = append(s.body, line)
	return s
}

func (m *Model) memCard(inner int) cardSpec {
	t := m.th
	s := cardSpec{title: "Memory", selected: -1, focus: noFocus(), open: "memory"}
	if !m.haveSnap {
		s.body = []string{t.dim.Render("Sampling…")}
		return s
	}
	th := m.env.Config.Thresholds
	mem, sw := m.snap.Mem, m.snap.Swap
	s.badge = lipgloss.NewStyle().Foreground(t.level(t.mem, mem.Percent, th.MemoryWarn, th.MemoryCrit)).Bold(true).Render(fmt.Sprintf("%.0f%%", mem.Percent))
	gaugeLine := func(label string, used, total uint64, pct float64, warn, crit float64) string {
		value := human.Bytes(used) + "/" + human.Bytes(total)
		gw := inner - 5 - 1 - lipgloss.Width(value)
		if gw < 4 {
			return t.subtle.Render(fmt.Sprintf("%-5s", label)) + value
		}
		return t.subtle.Render(fmt.Sprintf("%-5s", label)) + t.gauge(pct, gw, t.level(t.mem, pct, warn, crit)) + " " + value
	}
	s.body = append(s.body, gaugeLine("RAM", mem.Used, mem.Total, mem.Percent, th.MemoryWarn, th.MemoryCrit))
	if sw.Total > 0 {
		s.body = append(s.body, gaugeLine("Swap", sw.Used, sw.Total, sw.Percent, th.SwapWarn, 95))
	} else {
		s.body = append(s.body, t.subtle.Render("Swap ")+t.dim.Render("none"))
	}
	s.body = append(s.body, t.chart(m.mem, inner, 3+m.grow, 100, func(v float64) colorT { return t.level(t.mem, v, th.MemoryWarn, th.MemoryCrit) })...)
	return s
}

func (m *Model) netCard(inner int) cardSpec {
	t := m.th
	s := cardSpec{title: "Network", selected: -1, focus: noFocus(), open: "history:net"}
	n := m.snap.Net
	if !m.haveSnap || !n.Ready {
		s.body = []string{t.dim.Render("Measuring…")}
		return s
	}
	if len(n.Interfaces) > 0 {
		s.badge = t.dim.Render(strings.Join(n.Interfaces, " "))
	}
	const valueW = 9
	cw := inner - 2 - 1 - valueW
	window := func(v series) series {
		if len(v) > cw {
			return v[len(v)-cw:]
		}
		return v
	}
	top := 1024.0
	for _, v := range append(append(series{}, window(m.rx)...), window(m.tx)...) {
		top = max(top, v)
	}
	draw := func(arrow string, style lipgloss.Style, values series, rate float64) {
		rows := t.chart(values, cw, 2+m.grow/2, top, func(float64) colorT { return style.GetForeground() })
		for i, row := range rows {
			label, value := "  ", strings.Repeat(" ", valueW)
			if i == 0 {
				label = style.Bold(true).Render(arrow) + " "
				value = fmt.Sprintf("%*s", valueW, human.Rate(rate))
			}
			s.body = append(s.body, label+row+" "+value)
		}
	}
	if cw >= 4 {
		draw("↓", lipgloss.NewStyle().Foreground(t.rx), m.rx, n.RxRate)
		draw("↑", lipgloss.NewStyle().Foreground(t.tx), m.tx, n.TxRate)
	} else {
		s.body = append(s.body, "↓ "+human.Rate(n.RxRate), "↑ "+human.Rate(n.TxRate))
	}
	s.body = append(s.body, t.dim.Render(fmt.Sprintf("since boot ↓ %s  ↑ %s", human.Bytes(n.RxTotal), human.Bytes(n.TxTotal))))
	return s
}

func (m *Model) diskCard(inner int) cardSpec {
	t := m.th
	s := cardSpec{title: "Disks", selected: -1, focus: noFocus()}
	if !m.haveSnap {
		s.body = []string{t.dim.Render("Sampling…")}
		return s
	}
	if len(m.snap.Disks) == 0 {
		s.body = []string{t.dim.Render("No disks found")}
		return s
	}
	th := m.env.Config.Thresholds
	nameW := 0
	for _, d := range m.snap.Disks {
		nameW = max(nameW, lipgloss.Width(d.Mount))
	}
	nameW = min(nameW, max(inner/3, 6))
	showSizes := inner >= 44
	for _, d := range m.snap.Disks {
		pct := fmt.Sprintf("%4.0f%%", d.Percent)
		sizes := ""
		if showSizes {
			sizes = " " + t.dim.Render(fmt.Sprintf("%9s", human.Bytes(d.Used)+"/"+human.Bytes(d.Total)))
		}
		gw := inner - nameW - 1 - len(pct) - lipgloss.Width(sizes) - 1
		line := t.subtle.Render(fit(d.Mount, nameW)) + " "
		if gw >= 3 {
			line += t.gauge(d.Percent, gw, t.level(t.disk, d.Percent, th.DiskWarn, th.DiskCrit)) + " "
		}
		line += pct + sizes
		id := "disk:" + d.Mount
		s.hits = append(s.hits, hit{x: -2, y: len(s.body), w: inner + 4, h: 1, click: func(m *Model) tea.Cmd { return m.openItem(tabOverview, id) }})
		s.body = append(s.body, line)
	}
	return s
}

func (m *Model) systemCard(inner int) cardSpec {
	t := m.th
	s := cardSpec{title: "System", selected: -1, focus: noFocus()}
	if !m.haveSnap {
		s.body = []string{t.dim.Render("Sampling…")}
		return s
	}
	snap := m.snap
	kv := func(k, v string) string { return t.subtle.Render(fmt.Sprintf("%-9s", k)) + v }

	clock := m.now.Format("Mon 2 Jan 15:04:05")
	if m.haveFacts {
		if tz := m.facts.Time.Timezone; tz != "" {
			clock += " " + t.dim.Render(tz)
		}
	}
	s.body = append(s.body,
		kv("Time", clock),
		kv("Uptime", human.Duration(snap.Host.Uptime)+t.dim.Render(" since "+snap.Host.Boot.Format("2 Jan 15:04"))),
		kv("OS", snap.Host.OS),
		kv("Kernel", snap.Host.Kernel),
		kv("Procs", fmt.Sprintf("%d", snap.Procs)),
	)
	if len(snap.Sessions) == 0 {
		s.body = append(s.body, kv("Sessions", t.dim.Render("none")))
		return s
	}
	s.body = append(s.body, kv("Sessions", fmt.Sprintf("%d", len(snap.Sessions))))
	for _, ss := range snap.Sessions {
		from := ss.From
		if from == "" {
			from = "local"
		}
		since := ss.Started.Format("15:04")
		if time.Since(ss.Started) > 20*time.Hour {
			since = ss.Started.Format("2 Jan")
		}
		left := "  " + t.bold.Render(ss.User) + " " + t.dim.Render(ss.Terminal) + " " + from
		if line, ok := spread(left, t.dim.Render(since), inner); ok {
			s.body = append(s.body, line)
		} else {
			s.body = append(s.body, fit(left, inner))
		}
	}
	return s
}
