package tui

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/metrics"
)

// procsShown is how many processes the list shows; the rest are idle.
const procsShown = 60

type procState struct {
	sampler *metrics.ProcSampler
	list    []metrics.Proc
	loaded  bool
	byMem   bool
	busy    bool
	gen     int
}

type (
	procsMsg    []metrics.Proc
	procsDueMsg int
)

func (m *Model) pollProcs() tea.Cmd {
	p := &m.procs
	if p.busy {
		return nil
	}
	if p.sampler == nil {
		p.sampler = metrics.NewProcSampler()
	}
	p.busy = true
	sampler := p.sampler
	first := !p.loaded
	return func() tea.Msg {
		if first {
			// CPU use needs two readings; take the first one now so the
			// list doesn't open with everything at zero.
			sampler.Sample()
			time.Sleep(500 * time.Millisecond)
		}
		return procsMsg(sampler.Sample())
	}
}

func (m *Model) procsUpdated(msg procsMsg) tea.Cmd {
	p := &m.procs
	p.busy, p.loaded, p.list = false, true, []metrics.Proc(msg)
	m.syncLists()
	p.gen++
	gen := p.gen
	every := time.Duration(m.env.Config.RefreshSeconds) * time.Second
	return tea.Tick(every, func(time.Time) tea.Msg { return procsDueMsg(gen) })
}

// shownProcs is the list in display order: user processes only, busiest
// first.
func (m *Model) shownProcs() (shown []metrics.Proc, hidden int) {
	for _, p := range m.procs.list {
		if !p.Kernel {
			shown = append(shown, p)
		}
	}
	byMem := m.procs.byMem
	sort.SliceStable(shown, func(i, j int) bool {
		a, b := shown[i], shown[j]
		if byMem && a.RSS != b.RSS {
			return a.RSS > b.RSS
		}
		// CPU rounded to a tenth, so the order doesn't flicker over noise.
		ca, cb := int(a.CPU*10), int(b.CPU*10)
		if ca != cb {
			return ca > cb
		}
		if a.RSS != b.RSS {
			return a.RSS > b.RSS
		}
		return a.PID < b.PID
	})
	if len(shown) > procsShown {
		hidden = len(shown) - procsShown
		shown = shown[:procsShown]
	}
	return shown, hidden
}

// procItems is what can be selected on the tab: the two charts, and then
// the processes.
func (m *Model) procItems() []string {
	shown, _ := m.shownProcs()
	ids := []string{"load", "memory"}
	for _, p := range shown {
		ids = append(ids, "pid:"+strconv.Itoa(p.PID))
	}
	return ids
}

func (m *Model) proc(pid int) (metrics.Proc, bool) {
	for _, p := range m.procs.list {
		if p.PID == pid {
			return p, true
		}
	}
	return metrics.Proc{}, false
}

// mine reports whether a process is opsie or one of its ancestors, which
// can't be stopped from here without cutting off this session.
func (m *Model) mine(pid int) bool {
	parent := map[int]int{}
	for _, p := range m.procs.list {
		parent[p.PID] = p.PPID
	}
	for p := os.Getpid(); p > 1; p = parent[p] {
		if p == pid {
			return true
		}
		if _, ok := parent[p]; !ok {
			break
		}
	}
	return false
}

// where names the service or container a process belongs to.
func (m *Model) where(p metrics.Proc) string {
	switch {
	case p.Unit != "":
		return strings.TrimSuffix(p.Unit, ".service")
	case p.Container != "":
		for _, c := range m.facts.Containers {
			if strings.HasPrefix(c.ID, p.Container) || strings.HasPrefix(p.Container, c.ID) {
				return c.Name
			}
		}
		return "container"
	}
	return ""
}

func (m *Model) containerFor(p metrics.Proc) string {
	if p.Container == "" {
		return ""
	}
	for _, c := range m.facts.Containers {
		if c.ID != "" && (strings.HasPrefix(c.ID, p.Container) || strings.HasPrefix(p.Container, c.ID)) {
			return c.Name
		}
	}
	return ""
}

// --- page ---

func (m *Model) procsPage(w int) block {
	l := &m.lists[tabProcs]
	ids := m.procItems()
	selected := ""
	if l.sel < len(ids) {
		selected = ids[l.sel]
	}
	// What the processes add up to sits above them. Here the charts are
	// items like the rest: each opens its history, and is lit when it's
	// the selection.
	item := func(id string, card func(inner int) cardSpec) func(inner int) cardSpec {
		return func(inner int) cardSpec {
			s := card(inner)
			s.open, s.openTab = id, tabProcs
			if id == selected {
				s.focused = true
				s.focus = [2]int{0, len(s.body) + 1}
			}
			return s
		}
	}
	return vstack(w, m.pair(w, item("load", m.cpuCard), item("memory", m.memCard)), m.procsCard(w, selected))
}

func (m *Model) procsCard(w int, selected string) block {
	t := m.th
	inner := w - 4
	s := cardSpec{title: "Processes", selected: -1, focus: noFocus()}
	if !m.procs.loaded {
		s.body = []string{t.dim.Render("Measuring…")}
		return m.cardBlock(s, w, 0)
	}
	shown, hidden := m.shownProcs()
	total := 0
	for _, p := range m.procs.list {
		if !p.Kernel {
			total++
		}
	}
	s.badge = t.subtle.Render(strconv.Itoa(total))

	// The sort switch is the first line; tap either word.
	cpuLabel, memLabel := t.subtle.Render("CPU"), t.subtle.Render("memory")
	if m.procs.byMem {
		memLabel = t.tab.Render("memory")
	} else {
		cpuLabel = t.tab.Render("CPU")
	}
	sortLine := t.dim.Render("Busiest by ") + cpuLabel + t.dim.Render(" · ") + memLabel
	s.body = append(s.body, fit(sortLine, inner))
	cpuX := lipgloss.Width("Busiest by ")
	memX := cpuX + 3 + 3
	s.hits = append(s.hits,
		hit{x: cpuX - 1, y: 0, w: 5, h: 1, click: func(m *Model) tea.Cmd { m.setProcSort(false); return nil }},
		hit{x: memX - 1, y: 0, w: 8, h: 1, click: func(m *Model) tea.Cmd { m.setProcSort(true); return nil }})
	if inner >= 50 {
		s.body = append(s.body, t.dim.Render(m.procHeader(inner)))
	}
	for _, p := range shown {
		id := "pid:" + strconv.Itoa(p.PID)
		start := len(s.body)
		s.body = append(s.body, m.procRow(p, inner)...)
		if id == selected {
			s.selected = start
			s.focus = [2]int{start, len(s.body)}
		}
		s.hits = append(s.hits, hit{x: -2, y: start, w: inner + 4, h: len(s.body) - start, id: id, click: func(m *Model) tea.Cmd { return m.openItem(tabProcs, id) }})
	}
	if hidden > 0 {
		s.body = append(s.body, t.dim.Render(fmt.Sprintf("%d more, using next to nothing", hidden)))
	}
	if s.focus[0] >= 0 && s.focus[1] == len(s.body) {
		s.focus[1]++
	}
	return m.cardBlock(s, w, 0)
}

func (m *Model) setProcSort(byMem bool) {
	m.procs.byMem = byMem
	l := &m.lists[tabProcs]
	onProc := strings.HasPrefix(l.id, "pid:")
	l.moved, l.scroll, l.follow = false, 0, true
	m.syncLists()
	// From a process, the selection goes to the one that's now first;
	// from a chart, back to the start.
	if shown, _ := m.shownProcs(); onProc && len(shown) > 0 {
		l.pick("pid:"+strconv.Itoa(shown[0].PID), m.items(tabProcs))
	}
}

// Column widths for the process table.
const (
	pidW  = 8
	userW = 10
	cpuW  = 7
	memW  = 7
	unitW = 16
)

func (m *Model) procHeader(inner int) string {
	h := fit("PID", pidW) + fmt.Sprintf("%*s%*s ", cpuW, "CPU", memW, "MEM")
	if inner >= 80 {
		h = fit("PID", pidW) + fit("USER", userW) + fmt.Sprintf("%*s%*s ", cpuW, "CPU", memW, "MEM") + fit("WHERE", unitW)
	}
	return fit(h+"COMMAND", inner)
}

func (m *Model) procRow(p metrics.Proc, inner int) []string {
	t := m.th
	cpu := fmt.Sprintf("%.1f%%", p.CPU)
	cpuStyle := t.subtle
	switch {
	case p.CPU >= 80:
		cpuStyle = t.dangerText
	case p.CPU >= 30:
		cpuStyle = t.warnText
	case p.CPU >= 1:
		cpuStyle = lipgloss.NewStyle()
	}
	mem := human.Bytes(p.RSS)
	if inner < 50 {
		right := cpuStyle.Render(cpu) + t.subtle.Render(" · "+mem)
		return m.itemRow(t.bold.Render(p.Name), right, inner)
	}
	cmd := p.Cmd
	if cmd == "" {
		cmd = "[" + p.Name + "]"
	}
	cols := cpuStyle.Render(fmt.Sprintf("%*s", cpuW, cpu)) + t.subtle.Render(fmt.Sprintf("%*s ", memW, mem))
	line := t.dim.Render(fit(strconv.Itoa(p.PID), pidW))
	if inner >= 80 {
		line += t.subtle.Render(fit(p.User, userW)) + cols + t.dim.Render(fit(m.where(p), unitW-1)+" ")
	} else {
		line += cols
	}
	rest := inner - lipgloss.Width(line)
	return []string{line + fit(t.bold.Render(p.Name)+" "+t.subtle.Render(argsOf(cmd)), rest)}
}

// argsOf is a command line without its program, which the name already
// shows.
func argsOf(cmd string) string {
	_, args, _ := strings.Cut(cmd, " ")
	return args
}

// --- details ---

func (m *Model) procDetail(arg string, b *detailBuilder) detail {
	t := m.th
	pid, _ := strconv.Atoi(arg)
	p, ok := m.proc(pid)
	if !ok {
		b.text("This process has exited.", t.subtle)
		return detail{title: "Process " + arg, headline: headline(t, health.OK, "exited", ""), body: b.body}
	}
	b.kv("PID", strconv.Itoa(p.PID)+t.dim.Render(" · parent "+strconv.Itoa(p.PPID)))
	b.kv("User", p.User)
	b.kv("State", stateWord(p.State))
	b.kv("CPU", fmt.Sprintf("%.1f%% of a core", p.CPU))
	b.kv("Memory", human.Bytes(p.RSS))
	b.kv("Threads", strconv.Itoa(p.Threads))
	b.kv("Priority", niceWord(p.Nice))
	b.kv("Started", p.Started.Local().Format("Mon 2 Jan 15:04")+" · "+human.Duration(m.now.Sub(p.Started))+" ago")
	if w := m.where(p); w != "" {
		label := "Service"
		if p.Container != "" {
			label = "Container"
		}
		b.kv(label, w)
	}
	if p.Cmd != "" {
		b.text(p.Cmd, t.subtle)
	}

	var fixes []health.Fix
	args := func(extra ...string) map[string]string {
		a := map[string]string{"pid": strconv.Itoa(p.PID), "start": strconv.FormatUint(p.Start, 10)}
		for i := 0; i+1 < len(extra); i += 2 {
			a[extra[i]] = extra[i+1]
		}
		return a
	}
	switch {
	case p.PID <= 1 || p.Kernel:
		b.text("This is part of the system itself.", t.subtle)
	case m.mine(p.PID):
		b.text("This is part of your own session, so stopping it would end opsie and disconnect you.", t.subtle)
	default:
		note := ""
		if p.Unit != "" {
			note = " It belongs to " + p.Unit + ", which may start it again; stopping the service is usually what you want."
		}
		if c := m.containerFor(p); c != "" {
			note = " It runs in the container " + c + "; stopping the container is usually what you want."
		}
		fixes = append(fixes,
			health.Fix{Label: "Stop", Op: "proc.signal", Args: args("signal", "TERM"), Danger: true,
				Confirm: "Ask " + p.Name + " (" + strconv.Itoa(p.PID) + ") to stop? Most programs save what they're doing and exit." + note,
				Done:    "Asked " + p.Name + " to stop"},
			health.Fix{Label: "Force stop", Op: "proc.signal", Args: args("signal", "KILL"), Danger: true,
				Confirm: "End " + p.Name + " (" + strconv.Itoa(p.PID) + ") immediately? It gets no chance to save anything." + note,
				Done:    "Ended " + p.Name})
		if p.Nice < 10 {
			fixes = append(fixes, health.Fix{Label: "Lower priority", Op: "proc.renice", Args: args("nice", "10"),
				Done: p.Name + " now yields to everything else"})
		}
		if p.Nice != 0 {
			fixes = append(fixes, health.Fix{Label: "Normal priority", Op: "proc.renice", Args: args("nice", "0"), Done: p.Name + " is back to normal priority"})
		}
	}
	if p.Unit != "" {
		fixes = append(fixes, health.Fix{Label: "Open service", Special: "open:services:unit:" + p.Unit})
	}
	if c := m.containerFor(p); c != "" {
		fixes = append(fixes, health.Fix{Label: "Open container", Special: "open:docker:ctr:" + c})
	}
	return detail{title: p.Name, headline: headline(t, health.Info, stateWord(p.State), fmt.Sprintf("%.1f%% CPU · %s", p.CPU, human.Bytes(p.RSS))), body: b.body, fixes: fixes}
}

func stateWord(s string) string {
	switch s {
	case "R":
		return "running"
	case "S":
		return "sleeping"
	case "D":
		return "waiting on disk"
	case "Z":
		return "zombie"
	case "T", "t":
		return "stopped"
	case "I":
		return "idle"
	}
	return s
}

func niceWord(n int) string {
	switch {
	case n == 0:
		return "normal"
	case n > 0:
		return fmt.Sprintf("low (nice %d)", n)
	}
	return fmt.Sprintf("high (nice %d)", n)
}
