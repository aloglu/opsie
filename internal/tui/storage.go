package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/metrics"
	"github.com/aloglu/opsie/internal/priv"
)

// The Storage tab: every disk, what fills them and what can go.

// storagePoll measures what the tab shows: the folders of / and what's
// safe to remove. Both are kept for a while, so coming back is free.
func (m *Model) storagePoll() tea.Cmd {
	return tea.Batch(m.loadDu("/"), m.loadClean(), m.loadHistory())
}

func (m *Model) storageSections(inner int) []section {
	t := m.th
	if !m.haveSnap {
		return []section{{title: "Storage", intro: []string{t.dim.Render("Measuring…")}}}
	}
	var out []section
	if len(m.snap.IO.Devices) > 0 {
		out = append(out, m.activitySection(inner))
	}
	for _, d := range m.snap.Disks {
		out = append(out, m.diskSection(d, inner))
	}
	if len(m.snap.Disks) == 0 {
		out = append(out, section{title: "Storage", intro: []string{t.dim.Render("No disks found")}})
	}
	out = append(out, m.foldersSection(inner), m.cleanSection(inner))
	if !m.facts.NoDocker && len(m.dock.df) > 0 {
		out = append(out, m.dockerDiskSection(inner))
	}
	return out
}

func (m *Model) diskLevel(pct float64) colorT {
	th := m.env.Config.Thresholds
	return m.th.level(m.th.disk, pct, th.DiskWarn, th.DiskCrit)
}

// diskSection is one disk: how full it is, drawn as wide as the card.
func (m *Model) diskSection(d metrics.Disk, inner int) section {
	t := m.th
	c := m.diskLevel(d.Percent)
	s := section{title: d.Mount, badge: lipgloss.NewStyle().Foreground(c).Bold(true).Render(fmt.Sprintf("%.0f%%", d.Percent))}
	used := t.bold.Render(human.Bytes(d.Used)) + t.subtle.Render(" used of "+human.Bytes(d.Total))
	free := t.bold.Render(human.Bytes(d.Free)) + t.subtle.Render(" free")
	lines := []string{t.gauge(d.Percent, inner, c)}
	lines = append(lines, m.itemRow(used, free, inner)...)
	var about []string
	if d.FSType != "" {
		about = append(about, d.FSType)
	}
	if d.Device != "" {
		about = append(about, d.Device)
	}
	if d.InodePercent >= 1 {
		about = append(about, fmt.Sprintf("%.0f%% of its file slots", d.InodePercent))
	}
	if len(about) > 0 {
		lines = append(lines, t.dim.Render(fit(strings.Join(about, " · "), inner)))
	}
	// What's using it, and its history, are in its popup.
	s.entries = []entry{{id: "vol:" + d.Mount, lines: lines, block: true}}
	return s
}

// activitySection is what the disks are reading and writing, as an item.
// storagePage draws it, as a pair of cards.
func (m *Model) activitySection(int) section {
	return section{title: "Activity", entries: []entry{{id: "history:io"}}}
}

// storagePage puts what the disks are reading and writing above the rest,
// a card each.
func (m *Model) storagePage(w int) block {
	if !m.haveSnap || len(m.snap.IO.Devices) == 0 {
		return m.sectionsPage(tabStorage, w, 0)
	}
	t := m.th
	io := m.snap.IO
	top := m.pair(w,
		m.rateCard(tabStorage, "history:io", io.Ready, "Read", t.disk, m.rd, io.ReadRate, io.ReadTotal, strings.Join(io.Devices, " ")),
		m.rateCard(tabStorage, "history:io", io.Ready, "Write", t.wr, m.wr, io.WriteRate, io.WriteTotal, fmt.Sprintf("%.0f%% busy", io.Busy)))
	// The cards stand in for the Activity section, which is first.
	return vstack(w, top, m.sectionsPage(tabStorage, w, 1))
}

// rateCard is one side of a pair of rates as a card, read or written, in
// or out: its chart on its own scale, with the peak that scale comes from
// above it. It opens id on tab, and is lit when that's the selection.
func (m *Model) rateCard(tab tabID, id string, ready bool, title string, c colorT, past series, rate float64, total uint64, note string) func(inner int) cardSpec {
	return func(inner int) cardSpec {
		t := m.th
		s := cardSpec{title: title, selected: -1, focus: noFocus(), open: id, openTab: tab}
		if !ready {
			s.body = []string{t.dim.Render("Measuring…")}
			return s
		}
		s.badge = lipgloss.NewStyle().Foreground(c).Bold(true).Render(human.Rate(rate))
		peak := 0.0
		for _, v := range past[max(len(past)-inner, 0):] {
			peak = max(peak, v)
		}
		// The peak stays where there isn't room for everything: it's what
		// says how tall the chart is.
		about := "peak " + human.Rate(peak)
		line, ok := spread(t.dim.Render("since boot "+human.Bytes(total)+" · "+about), t.dim.Render(note), inner)
		if !ok {
			line, ok = spread(t.dim.Render(about), t.dim.Render(note), inner)
		}
		if !ok {
			line = t.dim.Render(fit(about, inner))
		}
		if inner < 20 {
			// Too narrow for every title to have its rate beside it, so
			// it goes on the line over the chart instead, on both of a pair.
			line, s.badge = fit(s.badge, inner), ""
		}
		s.body = append([]string{line}, t.chart(past, inner, 4+m.grow+m.pad, max(peak, 1024), func(float64) colorT { return c })...)
		if ids, l := m.items(tab), &m.lists[tab]; l.sel < len(ids) && ids[l.sel] == id {
			s.focused = true
			s.focus = [2]int{0, len(s.body) + 1}
		}
		return s
	}
}

// foldersSection is the biggest folders of the main disk, each with a
// bar for its share.
func (m *Model) foldersSection(inner int) section {
	t := m.th
	s := section{title: "Biggest folders"}
	e, ok := m.du["/"]
	switch {
	case !ok || e.loading:
		s.intro = wrap("Measuring every folder… This reads the whole disk at low priority, so it can take a minute.", inner)
		for i := range s.intro {
			s.intro[i] = t.dim.Render(s.intro[i])
		}
		return s
	case e.err != "":
		s.intro = wrap(e.err, inner)
		if m.admin == nil || !m.admin.Alive() {
			s.entries = append(s.entries, entry{id: "admin", lines: m.action("Unlock admin access", inner)})
		}
		return s
	}
	u := e.u
	s.badge = t.subtle.Render(human.Bytes(u.Size))
	const most = 8
	shown := 0
	for _, c := range u.Children {
		if shown == most || c.Size < 1<<20 {
			break
		}
		shown++
		line, band := m.shareRow(tildePath(c.Path), c.Size, u.Size, inner)
		s.entries = append(s.entries, entry{id: "du:" + c.Path, lines: []string{line}, band: band})
	}
	if shown == 0 {
		s.intro = []string{t.dim.Render("Nothing over a megabyte.")}
	}
	s.outro = []string{t.dim.Render(fit("of /, measured "+human.Duration(m.now.Sub(e.at))+" ago", inner))}
	return s
}

// cleanSection is what's safe to remove, and how much each would free.
func (m *Model) cleanSection(inner int) section {
	t := m.th
	s := section{title: "Free up space"}
	c := m.clean
	switch {
	case c.err != "":
		s.intro = wrap(c.err, inner)
		return s
	case c.at.IsZero() || c.loading:
		s.intro = []string{t.dim.Render("Looking for what can go…")}
		return s
	}
	var total uint64
	row := func(id, label string, size uint64, worth bool, note string) {
		right := t.subtle.Render(human.Bytes(size))
		if worth {
			right = t.warnText.Render(human.Bytes(size))
			total += size
		}
		if note != "" {
			right = t.dim.Render(note+" · ") + right
		}
		s.entries = append(s.entries, entry{id: id, lines: m.itemRow(label, right, inner)})
	}
	row("clean", "System log", c.c.Journal, c.c.Journal > 600<<20, "")
	row("clean:apt", "Downloaded packages", c.c.AptCache, c.c.AptCache > 10<<20, "")
	if n := len(c.c.Autoremove); n > 0 {
		row("clean:unneeded", "Unneeded packages", c.c.AutoremoveSize, true, fmt.Sprint(n))
	}
	for _, u := range m.dock.df {
		if u.Type != "Images" {
			continue
		}
		if size := priv.ParseSize(strings.Fields(u.Reclaimable + " 0")[0]); size > 0 {
			row("prune:unused", "Docker images not in use", size, size > 100<<20, "")
		}
	}
	if total > 0 {
		s.badge = t.warnText.Render("about " + human.Bytes(total))
	} else {
		s.badge = t.okText.Render("nothing to do")
	}
	return s
}

// dockerDiskSection is where Docker's share of the disk goes.
func (m *Model) dockerDiskSection(inner int) section {
	t := m.th
	s := section{title: "Docker"}
	var lines []string
	var total uint64
	for _, u := range m.dock.df {
		label := map[string]string{"Images": "Images", "Containers": "Containers", "Local Volumes": "Volumes", "Build Cache": "Build cache"}[u.Type]
		if label == "" {
			label = u.Type
		}
		total += priv.ParseSize(u.Size)
		lines = append(lines, m.itemRow(t.subtle.Render(label), dockerSize(u.Size), inner)...)
	}
	s.badge = t.subtle.Render(human.Bytes(total))
	s.entries = []entry{{id: "prune", lines: lines, block: true}}
	return s
}

// shareRow draws something's size as its share of a whole: its name, the
// size, the percentage, and a bar that long. band is where the bar
// starts, so a selection can stop short of it.
func (m *Model) shareRow(name string, size, whole uint64, inner int) (line string, band int) {
	t := m.th
	pct := 0.0
	if whole > 0 {
		pct = float64(size) / float64(whole) * 100
	}
	nums := fmt.Sprintf("%7s %4.0f%%", human.Bytes(size), pct)
	if pct > 0 && pct < 0.5 {
		// The bar still shows a sliver, so the number shouldn't say nothing.
		nums = fmt.Sprintf("%7s   <1%%", human.Bytes(size))
	}
	barW := min(max(inner/3, 6), 28)
	nameW := inner - len(nums) - 1 - barW
	if nameW < 8 {
		// No room for a bar: the numbers have to do.
		line, _ = spread(name, t.subtle.Render(strings.TrimSpace(nums)), inner)
		return line, 0
	}
	band = nameW + len(nums)
	return fit(name, nameW) + t.subtle.Render(nums) + " " + t.gauge(pct, barW, t.disk), band
}

// share adds a line to a popup that draws an amount as its share of a
// whole, with the bar first so a column of them lines up.
func (b *detailBuilder) share(label string, pct float64, c colorT, text string) {
	if b.para || len(b.body) == 0 {
		if len(b.body) > 0 {
			b.body = append(b.body, "")
		}
		b.para = false
	}
	const keyW = 12
	barW := min(max((b.inner-keyW)/3, 6), 20)
	b.body = append(b.body, b.t.subtle.Render(fit(label, keyW-1))+" "+b.t.gauge(pct, barW, c)+" "+fit(text, b.inner-keyW-barW-1))
}

// volumeDetail is one disk in full, with its history.
func (m *Model) volumeDetail(mount string, b *detailBuilder) detail {
	t := m.th
	th := m.env.Config.Thresholds
	for _, d := range m.snap.Disks {
		if d.Mount != mount {
			continue
		}
		st, word := health.OK, "ok"
		switch {
		case d.Percent >= th.DiskCrit:
			st, word = health.Crit, "nearly full"
		case d.Percent >= th.DiskWarn:
			st, word = health.Warn, "filling up"
		}
		b.body = append(b.body, t.gauge(d.Percent, b.inner, m.diskLevel(d.Percent)))
		used := t.bold.Render(human.Bytes(d.Used)) + t.subtle.Render(" used of "+human.Bytes(d.Total))
		line, _ := spread(used, t.bold.Render(human.Bytes(d.Free))+t.subtle.Render(" free"), b.inner)
		b.body = append(b.body, line)
		b.para = true
		if d.FSType != "" {
			b.kv("Filesystem", d.FSType)
		}
		if d.Device != "" {
			b.kv("Device", d.Device)
		}
		if d.InodePercent > 0 {
			b.share("File slots", d.InodePercent, t.disk, fmt.Sprintf("%.0f%% used", d.InodePercent))
			b.text("File slots are inodes: a disk can run out of them before it runs out of space.", t.dim)
		}
		b.text(fmt.Sprintf("It's a warning from %.0f%% and a problem from %.0f%%; Settings changes that.", th.DiskWarn, th.DiskCrit), t.subtle)
		m.historyCharts("disk:"+mount, b)
		return detail{title: "Disk " + mount, headline: headline(t, st, word, fmt.Sprintf("%.0f%% used · %s free", d.Percent, human.Bytes(d.Free))), body: b.body,
			fixes: []health.Fix{
				{Label: "What's using the space", Special: "explore:" + mount},
				{Label: "Free up space", Special: "clean"},
			}}
	}
	b.text("This disk isn't mounted anymore.", t.subtle)
	return detail{title: "Disk " + filepath.Base(mount), body: b.body}
}
