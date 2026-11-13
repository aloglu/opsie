package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/aloglu/opsie/internal/health"
)

type tabID int

const (
	tabOverview tabID = iota
	tabDocker
	tabServices
	tabProcs
	tabStorage
	tabNetwork
	tabSecurity
	tabUpdates
	tabAgents
	tabSettings
	tabCount
)

// tabs are the sections, in the order of their keys: 1 to 9, then 0.
// short is the name where the full ones don't fit.
var tabs = []struct {
	id          tabID
	name, short string
}{
	{tabOverview, "Overview", "Home"},
	{tabDocker, "Docker", "Docker"},
	{tabServices, "Services", "Svc"},
	{tabProcs, "Processes", "Procs"},
	{tabStorage, "Storage", "Disk"},
	{tabNetwork, "Network", "Net"},
	{tabSecurity, "Security", "Sec"},
	{tabUpdates, "Updates", "Upd"},
	{tabAgents, "Agents", "AI"},
	{tabSettings, "Settings", "Set"},
}

// tabKey is the key that opens the i-th tab.
func tabKey(i int) string { return string(rune('0' + (i+1)%10)) }

// list is one tab's selection and scroll position.
type list struct {
	sel int
	id  string
	// moved is set once you pick an item yourself; until then the
	// selection stays on the first (on Overview, the worst) one.
	moved  bool
	scroll int
	follow bool
}

// sync keeps the same item selected when the list changes under it.
func (l *list) sync(ids []string) {
	l.sel = 0
	if l.moved {
		for i, id := range ids {
			if id == l.id {
				l.sel = i
				break
			}
		}
	}
	if len(ids) > 0 {
		l.id = ids[l.sel]
	}
}

func (l *list) move(delta int, ids []string) {
	if len(ids) == 0 {
		return
	}
	l.sel = min(max(l.sel+delta, 0), len(ids)-1)
	l.id = ids[l.sel]
	l.moved, l.follow = true, true
}

func (l *list) pick(id string, ids []string) {
	for i, x := range ids {
		if x == id {
			l.sel, l.id, l.moved, l.follow = i, id, true, true
		}
	}
}

// items are the selectable things on a tab, in order.
func (m *Model) items(tab tabID) []string {
	switch tab {
	case tabDocker:
		return m.dockerItems()
	case tabSecurity, tabUpdates, tabServices, tabSettings, tabStorage, tabNetwork, tabAgents:
		return m.sectionItems(tab)
	case tabProcs:
		return m.procItems()
	}
	ids := make([]string, len(m.results))
	for i, r := range m.results {
		ids[i] = r.ID
	}
	return ids
}

func (m *Model) syncLists() {
	for tab := tabID(0); tab < tabCount; tab++ {
		m.lists[tab].sync(m.items(tab))
	}
}

func (m *Model) move(delta int) {
	m.lists[m.tab].move(delta, m.items(m.tab))
}

// openItem shows an item's detail view.
func (m *Model) openItem(tab tabID, id string) tea.Cmd {
	m.lists[tab].pick(id, m.items(tab))
	key := id
	// The Overview's results, and the Processes tab's charts, which open
	// the same ones.
	result := tab == tabOverview || id == "admin" || (tab == tabProcs && !strings.HasPrefix(id, "pid:"))
	if result && !strings.HasPrefix(id, "history:") {
		key = "result:" + id
	}
	m.modal = &modal{key: key}
	// Rows that only do one thing, like "Add a key", go straight to it.
	if strings.HasPrefix(id, "do:") {
		if fixes := m.detailFor(key, 40).fixes; len(fixes) == 1 {
			m.modal.direct = true
			return m.chooseFix(fixes[0])
		}
	}
	return m.modalOpened()
}

func (m *Model) openSelected() tea.Cmd {
	ids := m.items(m.tab)
	l := &m.lists[m.tab]
	if l.sel < 0 || l.sel >= len(ids) {
		return nil
	}
	return m.openItem(m.tab, ids[l.sel])
}

func (m *Model) switchTab(tab tabID) tea.Cmd {
	m.tab = tab
	m.modal, m.logs, m.help = nil, nil, false
	m.lists[tab].follow = true
	return m.tabPoll()
}

// tabMark is a small status next to a tab's name, so trouble shows from
// any tab.
func (m *Model) tabMark(tab tabID) string {
	if tab == tabOverview || !m.haveFacts {
		return ""
	}
	worst := health.OK
	for _, r := range m.results {
		if resultTab(r.ID) == tab && r.Status > worst {
			worst = r.Status
		}
	}
	if worst >= health.Warn {
		return m.th.statusIcon(worst)
	}
	return ""
}

// resultTab is the tab where a checklist result's subject lives.
func resultTab(id string) tabID {
	kind, _, _ := strings.Cut(id, ":")
	switch kind {
	case "ctr", "containers", "images":
		return tabDocker
	case "tunnel", "site", "sites":
		return tabNetwork
	case "unit", "units":
		return tabServices
	case "ssh", "keys", "firewall", "sudo", "logins", "ports":
		return tabSecurity
	case "updates", "reboot":
		return tabUpdates
	}
	return tabOverview
}

// spot is where the selected item was last drawn, or nil if it wasn't.
func (m *Model) spot() *hit {
	ids := m.items(m.tab)
	l := &m.lists[m.tab]
	if l.sel >= len(ids) {
		return nil
	}
	for i := range m.spots {
		if m.spots[i].id == ids[l.sel] {
			return &m.spots[i]
		}
	}
	return nil
}

// moveAlong selects the item above (dir -1) or below (dir 1) the selected
// one as the page was last drawn, whatever its place in the list. Past the
// end of a column it carries on in the next: from the bottom of one to the
// top of the one on its right, and back the same way. However many columns
// the width gives the page, holding the key walks through all of it in the
// order it reads.
func (m *Model) moveAlong(dir int) {
	cur := m.spot()
	if cur == nil {
		// Not drawn yet: there's only the list's order to go by.
		m.move(dir)
		return
	}
	// ahead is how far s is from cur going in dir; it's negative for items
	// level with cur or behind it.
	ahead := func(s hit) int {
		if dir > 0 {
			return s.y - (cur.y + cur.h)
		}
		return cur.y - (s.y + s.h)
	}
	side := func(s hit) int { return dir * (s.x - cur.x) }
	abs := func(n int) int { return max(n, -n) }

	// First the nearest item ahead in this column: any that's above or
	// below this one, so a card across the whole page counts too.
	best, found := hit{}, false
	for _, s := range m.spots {
		over := s.x < cur.x+cur.w && cur.x < s.x+s.w
		if !over || ahead(s) < 0 {
			continue
		}
		if !found || ahead(s) < ahead(best) || (ahead(s) == ahead(best) && abs(s.x-cur.x) < abs(best.x-cur.x)) {
			best, found = s, true
		}
	}
	if !found {
		// Then the nearest column on that side, from its near end.
		for _, s := range m.spots {
			if side(s) <= 0 {
				continue
			}
			if !found || side(s) < side(best) || (side(s) == side(best) && dir*(s.y-best.y) < 0) {
				best, found = s, true
			}
		}
	}
	if found {
		m.lists[m.tab].pick(best.id, m.items(m.tab))
	}
}

// moveAcross selects the nearest item in the column to the left (dir -1)
// or right (dir 1) of the selected one, as last drawn.
func (m *Model) moveAcross(dir int) {
	ids := m.items(m.tab)
	l := &m.lists[m.tab]
	cur := m.spot()
	if cur == nil {
		return
	}
	mid := cur.y + cur.h/2
	best, bestScore := "", 0
	for _, s := range m.spots {
		beside := (dir < 0 && s.x+s.w <= cur.x) || (dir > 0 && s.x >= cur.x+cur.w)
		if !beside {
			continue
		}
		// Prefer the item level with this one, then the nearest column.
		dy := 0
		switch {
		case mid < s.y:
			dy = s.y - mid
		case mid >= s.y+s.h:
			dy = mid - (s.y + s.h - 1)
		}
		dx := s.x - cur.x
		if dx < 0 {
			dx = -dx
		}
		if score := dy*1000 + dx; best == "" || score < bestScore {
			best, bestScore = s.id, score
		}
	}
	if best != "" {
		l.pick(best, ids)
	}
}

// selectKeys is the hint for moving the selection: the arrows across too
// when the page has columns.
func (m *Model) selectKeys() string {
	for _, s := range m.spots {
		if len(m.spots) > 0 && s.x != m.spots[0].x {
			return "↑↓←→"
		}
	}
	return "↑↓"
}
