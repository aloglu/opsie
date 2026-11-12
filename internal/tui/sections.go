package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// A section is a card of selectable rows, the building block of the
// Security and Updates tabs.
type section struct {
	title, badge string
	// intro and outro are text above and below the rows that can't be
	// selected.
	intro   []string
	entries []entry
	outro   []string
}

// entry is one selectable thing; its lines are already laid out for the
// card's width.
type entry struct {
	id    string
	lines []string
	// block entries are the card's whole subject: a chart, a disk, an
	// agent. Selecting one lights the card's border rather than covering
	// what it shows with the band.
	block bool
	// band is how far into the row the band goes, for a row that ends in
	// a bar; 0 is all the way.
	band int
}

// sectionsFor builds a tab's sections for an inner card width.
func (m *Model) sectionsFor(tab tabID, inner int) []section {
	switch tab {
	case tabSecurity:
		return m.securitySections(inner)
	case tabUpdates:
		return m.updatesSections(inner)
	case tabServices:
		return m.servicesSections(inner)
	case tabSettings:
		return m.settingsSections(inner)
	case tabStorage:
		return m.storageSections(inner)
	case tabNetwork:
		return m.networkSections(inner)
	case tabAgents:
		return m.agentsSections(inner)
	}
	return nil
}

func (m *Model) sectionItems(tab tabID) []string {
	var ids []string
	for _, s := range m.sectionsFor(tab, 60) {
		for _, e := range s.entries {
			ids = append(ids, e.id)
		}
	}
	return ids
}

func (m *Model) sectionsPage(tab tabID, w int) block {
	ids := m.items(tab)
	l := &m.lists[tab]
	selected := ""
	if l.sel < len(ids) {
		selected = ids[l.sel]
	}
	// Every card is built at its column's width; the column count doesn't
	// depend on the content, so the section list is the same for each.
	n := columnsFor(w)
	widths := split(w, n, 1)
	var builds []func(inner int) cardSpec
	for i := range m.sectionsFor(tab, widths[0]-4) {
		builds = append(builds, func(inner int) cardSpec {
			secs := m.sectionsFor(tab, inner)
			if i >= len(secs) {
				return cardSpec{selected: -1, focus: noFocus()}
			}
			return m.sectionCard(tab, secs[i], inner, selected)
		})
	}
	return masonry(m, w, builds)
}

func columnsFor(w int) int {
	switch {
	case w >= 170:
		return 3
	case w >= wideFrom:
		return 2
	}
	return 1
}

// masonry lays cards out in columns, each card going to the column that's
// shortest so far.
func masonry(m *Model, w int, builds []func(inner int) cardSpec) block {
	n := columnsFor(w)
	widths := split(w, n, 1)
	cols := make([][]block, n)
	heights := make([]int, n)
	for _, build := range builds {
		i := 0
		for c := range heights {
			if heights[c] < heights[i] {
				i = c
			}
		}
		b := m.cardBlock(build(widths[i]-4), widths[i], 0)
		cols[i] = append(cols[i], b)
		heights[i] += b.height()
	}
	stacks := make([]block, n)
	for i := range cols {
		stacks[i] = vstack(widths[i], cols[i]...)
	}
	return hstack(1, stacks...)
}

func (m *Model) sectionCard(tab tabID, s section, inner int, selected string) cardSpec {
	c := cardSpec{title: s.title, badge: s.badge, selected: -1, focus: noFocus()}
	c.body = append(c.body, s.intro...)
	for _, e := range s.entries {
		start := len(c.body)
		c.body = append(c.body, e.lines...)
		switch {
		case e.id != selected:
		case e.block:
			c.focused = true
			c.focus = [2]int{0, len(c.body)}
		default:
			c.selected, c.selEnd, c.band = start, len(c.body), e.band
			c.focus = [2]int{start, len(c.body)}
		}
		id := e.id
		c.hits = append(c.hits, hit{x: -2, y: start, w: inner + 4, h: len(c.body) - start, id: id, click: func(m *Model) tea.Cmd { return m.openItem(tab, id) }})
	}
	c.body = append(c.body, s.outro...)
	if len(c.body) == 0 {
		c.body = []string{""}
	}
	if c.focus[0] >= 0 {
		if c.focused {
			c.focus[1] = len(c.body) // all of the card
		}
		if c.focus[0] == len(s.intro) {
			c.focus[0] = 0 // keep the title in view with the first row
		}
		if c.focus[1] == len(c.body) {
			c.focus[1]++
		}
	}
	return c
}

// itemRow puts left and right on one line when they fit, and right under
// left otherwise.
func (m *Model) itemRow(left, right string, inner int) []string {
	if right == "" {
		return []string{fit(left, inner)}
	}
	if line, ok := spread(left, right, inner); ok {
		return []string{line}
	}
	return []string{fit(left, inner), "  " + fit(right, inner-2)}
}

// action is a row that does something rather than shows something.
func (m *Model) action(label string, inner int) []string {
	return []string{fit(m.th.navText.Render(label+" ›"), inner)}
}

func (m *Model) styled(c colorT, s string) string {
	return lipgloss.NewStyle().Foreground(c).Render(s)
}
