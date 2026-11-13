package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/metrics"
)

// overviewPage builds the Overview tab, letting the charts grow into any
// spare height on screens wide enough to show several cards at once.
func (m *Model) overviewPage(w, h int) block {
	m.grow = 0
	content := m.overview(w)
	if spare := h - content.height(); spare > 0 && w >= mediumFrom {
		// Charts in two rows have the room to share.
		rows := 2
		if w >= chartsRowFrom {
			rows = 1
		}
		m.grow = min(spare/rows, 12)
		content = m.overview(w)
	}
	return content
}

// chartsRowFrom is the narrowest a screen can be and still give the four
// charts a row to themselves without squeezing them.
const chartsRowFrom = 140

// overview lays the cards out for the width. The charts are always what
// comes first: in a row where four fit comfortably, two by two where
// they don't, and as one card of lines on a phone. The checklist and the
// system are under them, side by side where there's room.
func (m *Model) overview(w int) block {
	if w < mediumFrom {
		// A phone gets the readings as one card, a line each, so they and
		// the top of the checklist are on screen together; their charts
		// are a tap away.
		return vstack(w,
			m.cardBlock(m.vitalsCard(w-4), w, 0),
			m.cardBlock(m.healthCard(w-4), w, 0),
			m.cardBlock(m.systemCard(w-4), w, 0),
		)
	}
	var charts block
	if w >= chartsRowFrom {
		charts = m.row(1, split(w, 4, 1), m.cpuCard, m.memCard, m.netCard, m.diskCard)
	} else {
		cols := split(w, 2, 1)
		charts = vstack(w, m.row(1, cols, m.cpuCard, m.memCard), m.row(1, cols, m.netCard, m.diskCard))
	}
	if w < wideFrom {
		return vstack(w, charts, m.cardBlock(m.healthCard(w-4), w, 0), m.cardBlock(m.systemCard(w-4), w, 0))
	}
	lw := (w - 1) * 3 / 5
	rw := w - 1 - lw
	right := m.cardBlock(m.systemCard(rw-4), rw, 0)
	left := m.cardBlock(m.healthCard(lw-4), lw, right.height()-2)
	return vstack(w, charts, hstack(1, left, right))
}

// vitalsCard is every reading on a line of its own, all drawn the same
// way: its name, the last while as a chart one row tall, and where it is
// now. A tap on one opens its history or its tab.
func (m *Model) vitalsCard(inner int) cardSpec {
	t := m.th
	s := cardSpec{title: "Now", selected: -1, focus: noFocus()}
	if !m.haveSnap {
		s.body = []string{t.dim.Render("Sampling…")}
		return s
	}
	th := m.env.Config.Thresholds
	snap := m.snap
	const labelW, valueW = 5, 7
	cw := inner - labelW - 1 - 1 - valueW
	// line is one reading. mark, the last character of the label, says
	// which way a rate goes; top is what a full-height bar stands for, or
	// 0 for the most there's been.
	line := func(label, mark string, past series, top float64, color func(float64) colorT, now float64, value string, click func(m *Model) tea.Cmd) {
		c := color(now)
		text := t.subtle.Render(fit(label, labelW-1)) + lipgloss.NewStyle().Foreground(c).Bold(true).Render(fit(mark, 1))
		if cw >= 4 {
			if top == 0 {
				top = 1024
				for _, v := range past[max(len(past)-cw, 0):] {
					top = max(top, v)
				}
			}
			text += " " + t.chart(past, cw, 1, top, color)[0]
		}
		text += " " + lipgloss.NewStyle().Foreground(c).Bold(true).Render(fmt.Sprintf("%*s", valueW, value))
		s.hits = append(s.hits, hit{x: -2, y: len(s.body), w: inner + 4, h: 1, click: click})
		s.body = append(s.body, text)
	}
	open := func(id string) func(m *Model) tea.Cmd {
		return func(m *Model) tea.Cmd { return m.openItem(tabOverview, id) }
	}
	goTo := func(tab tabID) func(m *Model) tea.Cmd {
		return func(m *Model) tea.Cmd { return m.switchTab(tab) }
	}
	pct := func(v float64) string { return fmt.Sprintf("%.0f%%", v) }
	level := func(base colorT, warn, crit float64) func(float64) colorT {
		return func(v float64) colorT { return t.level(base, v, warn, crit) }
	}
	plain := func(c colorT) func(float64) colorT { return func(float64) colorT { return c } }

	line("CPU", "", m.cpu, 100, level(t.cpu, 80, 95), snap.CPU, pct(snap.CPU), open("load"))
	line("RAM", "", m.mem, 100, level(t.mem, th.MemoryWarn, th.MemoryCrit), snap.Mem.Percent, pct(snap.Mem.Percent), open("memory"))
	if sw := snap.Swap; sw.Total > 0 {
		line("Swap", "", m.swap, 100, level(t.mem, th.SwapWarn, 95), sw.Percent, pct(sw.Percent), open("swap"))
	}
	// One line for the disks: the main one, and any other only once it's
	// filling up. The rest are on Storage. How full a disk is doesn't
	// move in the time a chart covers, so its line is level.
	for _, d := range snap.Disks {
		label := d.Mount
		switch {
		case d.Mount == mainDisk(snap.Disks).Mount:
			label = "Disk"
		case d.Percent < th.DiskWarn:
			continue
		}
		steady := make(series, max(cw, 1))
		for i := range steady {
			steady[i] = d.Percent
		}
		line(label, "", steady, 100, level(t.disk, th.DiskWarn, th.DiskCrit), d.Percent, pct(d.Percent), goTo(tabStorage))
	}
	if n := snap.Net; n.Ready {
		line("Net", "↓", m.rx, 0, plain(t.rx), n.RxRate, human.Rate(n.RxRate), goTo(tabNetwork))
		line("", "↑", m.tx, 0, plain(t.tx), n.TxRate, human.Rate(n.TxRate), goTo(tabNetwork))
	}
	if io := snap.IO; io.Ready {
		line("I/O", "R", m.rd, 0, plain(t.disk), io.ReadRate, human.Rate(io.ReadRate), open("history:io"))
		line("", "W", m.wr, 0, plain(t.wr), io.WriteRate, human.Rate(io.WriteRate), open("history:io"))
	}
	return s
}

// mainDisk is the disk the system is on, or the first there is.
func mainDisk(disks []metrics.Disk) metrics.Disk {
	for _, d := range disks {
		if d.Mount == "/" {
			return d
		}
	}
	if len(disks) > 0 {
		return disks[0]
	}
	return metrics.Disk{}
}

// row renders cards side by side at the same height. A card shorter than
// the tallest is built again with the difference in pad, for its charts
// to take up.
func (m *Model) row(gap int, widths []int, cards ...func(inner int) cardSpec) block {
	specs := make([]cardSpec, len(cards))
	tallest := 0
	for i, card := range cards {
		specs[i] = card(widths[i] - 4)
		tallest = max(tallest, len(specs[i].body))
	}
	for i, card := range cards {
		if short := tallest - len(specs[i].body); short > 0 {
			m.pad = short
			specs[i] = card(widths[i] - 4)
			m.pad = 0
		}
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
	l := m.snap.Load
	loadText := t.subtle.Render("load ") + fmt.Sprintf("%.2f %.2f %.2f", l[0], l[1], l[2])
	line, ok := spread(loadText, t.dim.Render(human.Plural(m.snap.Cores, "core", "cores")), inner)
	if !ok && lipgloss.Width(loadText) > inner {
		// The last minute is the one that matters most.
		loadText = t.subtle.Render("load ") + fmt.Sprintf("%.2f", l[0])
	}
	if !ok {
		line = fit(loadText, inner)
	}
	// What's written goes above a chart, in every card, so nothing sits
	// between the chart and the card's edge.
	s.body = append(s.body, line)
	s.body = append(s.body, t.chart(m.cpu, inner, 4+m.grow+m.pad, 100, func(v float64) colorT { return t.level(t.cpu, v, 80, 95) })...)
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
	s.body = append(s.body, t.chart(m.mem, inner, 3+m.grow+m.pad, 100, func(v float64) colorT { return t.level(t.mem, v, th.MemoryWarn, th.MemoryCrit) })...)
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
	s.body = []string{m.sinceBoot(fmt.Sprintf("↓ %s  ↑ %s", human.Bytes(n.RxTotal), human.Bytes(n.TxTotal)), inner)}
	s.body = append(s.body, m.flowCharts(inner, 4+m.grow+m.pad,
		flow{mark: "↓", c: t.rx, past: m.rx, rate: n.RxRate},
		flow{mark: "↑", c: t.tx, past: m.tx, rate: n.TxRate})...)
	return s
}

// sinceBoot is the line of totals under a pair of charts. It says what
// they're totals of where there's room to.
func (m *Model) sinceBoot(totals string, inner int) string {
	if lipgloss.Width("since boot "+totals) <= inner {
		totals = "since boot " + totals
	}
	return m.th.dim.Render(fit(totals, inner))
}

// diskCard is how full each disk is, over charts of what they're reading
// and writing.
func (m *Model) diskCard(inner int) cardSpec {
	t := m.th
	s := cardSpec{title: "Disks", selected: -1, focus: noFocus()}
	if m.haveSnap && len(m.snap.IO.Devices) > 0 {
		// The card opens what the disks are doing; each bar, its own disk.
		s.open = "history:io"
		if io := m.snap.IO; io.Ready {
			s.badge = t.dim.Render(fmt.Sprintf("%.0f%% busy", io.Busy))
		}
	}
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
	s.body = append(s.body, m.ioLines(inner)...)
	return s
}

// ioLines is what's been read and written, over charts of it. It's empty where
// the system doesn't say.
func (m *Model) ioLines(inner int) []string {
	t := m.th
	io := m.snap.IO
	switch {
	case !m.haveSnap || len(io.Devices) == 0:
		return nil
	case !io.Ready:
		return []string{t.dim.Render("Measuring…")}
	}
	body := m.flowCharts(inner, 4+m.grow+m.pad,
		flow{mark: "R", c: t.disk, past: m.rd, rate: io.ReadRate},
		flow{mark: "W", c: t.wr, past: m.wr, rate: io.WriteRate})
	return append([]string{m.sinceBoot(fmt.Sprintf("R %s  W %s", human.Bytes(io.ReadTotal), human.Bytes(io.WriteTotal)), inner)}, body...)
}

// flow is one of a set of rates drawn together: read and written, in and
// out.
type flow struct {
	mark string
	c    colorT
	past series
	rate float64
}

// flowCharts draws rates as charts one above the other on one scale,
// each with its mark in front and its latest value after. They share
// rows evenly, so they read against each other; a row that's left over
// goes between them.
func (m *Model) flowCharts(inner, rows int, flows ...flow) []string {
	const valueW = 9
	cw := inner - 2 - 1 - valueW
	var body []string
	if cw < 4 {
		for _, f := range flows {
			body = append(body, f.mark+" "+human.Rate(f.rate))
		}
		return body
	}
	spare := rows % len(flows)
	rows /= len(flows)
	top := 1024.0
	for _, f := range flows {
		for _, v := range f.past[max(len(f.past)-cw, 0):] {
			top = max(top, v)
		}
	}
	for n, f := range flows {
		if n > 0 {
			for ; spare > 0; spare-- {
				body = append(body, "")
			}
		}
		for i, row := range m.th.chart(f.past, cw, rows, top, func(float64) colorT { return f.c }) {
			label, value := "  ", strings.Repeat(" ", valueW)
			if i == 0 {
				label = lipgloss.NewStyle().Foreground(f.c).Bold(true).Render(f.mark) + " "
				value = fmt.Sprintf("%*s", valueW, human.Rate(f.rate))
			}
			body = append(body, label+row+" "+value)
		}
	}
	return body
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
