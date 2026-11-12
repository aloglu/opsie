package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/priv"
	"github.com/aloglu/opsie/internal/registry"
)

// dockerEvery is how often the Docker tab refreshes while it's open.
// `docker stats` takes about two seconds to measure, so faster wouldn't
// help.
const dockerEvery = 5 * time.Second

type dockerState struct {
	stats map[string]priv.Stats
	cpu   map[string]*series
	// net turns each container's network counts into speeds.
	net  map[string]netRate
	df   []priv.DiskUsage
	dfAt time.Time
	// unused are the images no container uses, as measured with df.
	unused []priv.UnusedImage
	err    string

	busy bool
	gen  int

	preflight map[string]preflightState

	// apps are the folders in the apps directory with a compose file.
	apps []appFolder
}

type preflightState struct {
	loading bool
	p       priv.Preflight
	err     string
}

type (
	dockerMsg struct {
		containers []priv.Container
		listErr    error
		stats      []priv.Stats
		statsErr   error
		df         []priv.DiskUsage
		unused     []priv.UnusedImage
		haveDF     bool
		apps       []appFolder
		haveApps   bool
	}
	dockerDueMsg int
	preflightMsg struct {
		project string
		p       priv.Preflight
		err     error
	}
)

func (m *Model) pollDocker() tea.Cmd {
	if m.dock.busy || m.admin == nil || !m.admin.Alive() || m.facts.NoDocker {
		return nil
	}
	m.dock.busy = true
	admin := m.admin
	wantDF := time.Since(m.dock.dfAt) > time.Minute
	appsDir := m.appsDir()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var msg dockerMsg
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); msg.listErr = admin.Call(ctx, "docker.list", nil, &msg.containers) }()
		go func() { defer wg.Done(); msg.statsErr = admin.Call(ctx, "docker.stats", nil, &msg.stats) }()
		if wantDF {
			wg.Add(1)
			go func() {
				defer wg.Done()
				msg.haveDF = admin.Call(ctx, "docker.df", nil, &msg.df) == nil
				_ = admin.Call(ctx, "docker.unused", nil, &msg.unused)
			}()
		}
		var appsErr error
		msg.apps, appsErr = readApps(appsDir)
		msg.haveApps = appsErr == nil
		wg.Wait()
		return msg
	}
}

func (m *Model) dockerUpdated(msg dockerMsg) tea.Cmd {
	d := &m.dock
	d.busy = false
	if msg.listErr == nil {
		m.facts.Containers, m.facts.DockerErr = msg.containers, ""
	} else {
		d.err = msg.listErr.Error()
	}
	if msg.statsErr == nil {
		if d.stats == nil {
			d.stats, d.cpu = map[string]priv.Stats{}, map[string]*series{}
		}
		seen := map[string]bool{}
		for _, s := range msg.stats {
			d.stats[s.Name] = s
			seen[s.Name] = true
			if d.cpu[s.Name] == nil {
				d.cpu[s.Name] = &series{}
			}
			d.cpu[s.Name].push(s.CPU)
		}
		for name := range d.stats {
			if !seen[name] {
				delete(d.stats, name) // stopped since
			}
		}
		d.trackNet(msg.stats, time.Now())
	}
	if msg.haveDF {
		d.df, d.dfAt, d.unused = msg.df, time.Now(), msg.unused
	}
	if msg.haveApps {
		d.apps = msg.apps
	} else {
		d.apps = nil
	}
	m.evaluate()
	if m.tab != tabDocker {
		return nil
	}
	d.gen++
	gen := d.gen
	return tea.Tick(dockerEvery, func(time.Time) tea.Msg { return dockerDueMsg(gen) })
}

func (m *Model) preflight(project string) tea.Cmd {
	if m.admin == nil || !m.admin.Alive() {
		return nil
	}
	if m.dock.preflight == nil {
		m.dock.preflight = map[string]preflightState{}
	}
	m.dock.preflight[project] = preflightState{loading: true}
	admin := m.admin
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var p priv.Preflight
		err := admin.Call(ctx, "docker.preflight", map[string]string{"project": project}, &p)
		return preflightMsg{project: project, p: p, err: err}
	}
}

// room says whether there's space to pull or build a project's images.
// blocked means it would almost certainly fail; warn means it might.
func room(p priv.Preflight, build bool) (blocked, warn string) {
	const gib = 1 << 30
	free, images := human.Bytes(p.Free), human.Bytes(p.Images)
	switch {
	case p.Free < p.Images+gib:
		blocked = fmt.Sprintf("Only %s is free where Docker keeps images, and this project's images take %s. New versions need about that much again until the old ones are cleaned up. Free some space first, for example with Clean up on the Docker tab.", free, images)
	case p.Free < 2*p.Images+2*gib:
		warn = fmt.Sprintf("Space is tight: %s free, and this project's images take %s.", free, images)
	}
	if build && blocked == "" && p.MemAvailable > 0 {
		mem := human.Bytes(p.MemAvailable)
		switch {
		case p.MemAvailable < 512<<20:
			blocked = "Only " + mem + " of memory is available. Building would likely fail or push the server into swapping."
		case p.MemAvailable < 1536<<20:
			warn = strings.TrimSpace(warn + " Only " + mem + " of memory is available, which may not be enough to build.")
		}
	}
	return blocked, warn
}

// --- grouping ---

type project struct {
	name       string
	containers []priv.Container
}

// projects groups containers by Compose project, with containers that
// don't belong to one last under an empty name.
func (m *Model) projects() []project {
	byName := map[string][]priv.Container{}
	for _, c := range m.facts.Containers {
		byName[c.Project] = append(byName[c.Project], c)
	}
	var out []project
	for name, list := range byName {
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		out = append(out, project{name: name, containers: list})
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].name == "") != (out[j].name == "") {
			return out[j].name == ""
		}
		return out[i].name < out[j].name
	})
	return out
}

func (m *Model) dockerItems() []string {
	switch {
	case m.facts.NoDocker:
		return nil
	case m.facts.DockerErr != "" && len(m.facts.Containers) == 0:
		return []string{"admin"}
	}
	ids := []string{"prune"}
	for _, p := range m.projects() {
		if p.name != "" {
			ids = append(ids, "proj:"+p.name)
		}
		if p.single() {
			continue // the project is the container
		}
		for _, c := range p.containers {
			ids = append(ids, "ctr:"+c.Name)
		}
	}
	for _, a := range m.idleApps() {
		ids = append(ids, "app:"+a.dir)
	}
	return ids
}

func (m *Model) tunnelSections(inner int) []section {
	var out []section
	for _, tn := range m.facts.Tunnels {
		out = append(out, m.tunnelSection(tn, inner))
	}
	return out
}

// single reports whether a project has just one container, in which case
// the project and its container are one item, with both their actions.
func (p project) single() bool { return p.name != "" && len(p.containers) == 1 }

func (m *Model) container(name string) (priv.Container, bool) {
	for _, c := range m.facts.Containers {
		if c.Name == name {
			return c, true
		}
	}
	return priv.Container{}, false
}

// containerLook is a container's status icon and its color.
func (m *Model) containerLook(c priv.Container) (health.Status, string) {
	t := m.th
	switch {
	case c.State == "running" && c.Health == "unhealthy", c.State == "restarting", c.State == "dead":
		return health.Crit, t.dangerText.Bold(true).Render("✗")
	case c.State == "exited" && c.Exit != 0:
		return health.Warn, t.warnText.Bold(true).Render("▲")
	case c.State == "running" && c.Restarts >= 3:
		return health.Warn, t.warnText.Render("●")
	case c.State == "running" && c.Health == "starting":
		return health.Info, t.warnText.Render("◌")
	case c.State == "running":
		return health.OK, t.okText.Render("●")
	case c.State == "paused":
		return health.Info, t.infoText.Render("‖")
	}
	return health.Info, t.dim.Render("○")
}

// --- page ---

func (m *Model) dockerPage(w int) block {
	t := m.th
	l := &m.lists[tabDocker]
	ids := m.dockerItems()
	selected := ""
	if l.sel < len(ids) {
		selected = ids[l.sel]
	}

	switch {
	case m.facts.NoDocker:
		return m.cardBlock(cardSpec{title: "Docker", body: []string{t.subtle.Render("Docker isn't installed on this server.")}, selected: -1, focus: noFocus()}, w, 0)
	case !m.haveFacts:
		return m.cardBlock(cardSpec{title: "Docker", body: []string{t.dim.Render("Looking for containers…")}, selected: -1, focus: noFocus()}, w, 0)
	case m.facts.DockerErr != "" && len(m.facts.Containers) == 0:
		body := wrap("Docker can only be read with admin access, which is locked.", w-4)
		body = append(body, "", t.navText.Render("Unlock admin access ›"))
		s := cardSpec{title: "Docker", body: body, selected: -1, focus: noFocus(), open: "admin", openTab: tabDocker, focused: selected == "admin"}
		return m.cardBlock(s, w, 0)
	}

	builds := []func(inner int) cardSpec{func(inner int) cardSpec { return m.dockerSummary(inner, selected == "prune") }}
	for _, p := range m.projects() {
		builds = append(builds, func(inner int) cardSpec { return m.projectCard(p, inner, selected) })
	}
	if idle := m.idleApps(); len(idle) > 0 {
		builds = append(builds, func(inner int) cardSpec { return m.sectionCard(tabDocker, m.idleSection(idle, inner), inner, selected) })
	}
	return masonry(m, w, builds)
}

func (m *Model) dockerSummary(inner int, selected bool) cardSpec {
	t := m.th
	running := 0
	for _, c := range m.facts.Containers {
		if c.State == "running" {
			running++
		}
	}
	s := cardSpec{
		title: "Docker", badge: t.subtle.Render(fmt.Sprintf("%d of %d running", running, len(m.facts.Containers))),
		selected: -1, focus: noFocus(), open: "prune", openTab: tabDocker, focused: selected,
	}
	if selected {
		s.focus = [2]int{0, 1}
	}
	if len(m.dock.df) == 0 {
		s.body = []string{t.dim.Render("Measuring disk use…")}
	}
	for _, u := range m.dock.df {
		label := map[string]string{"Images": "Images", "Containers": "Containers", "Local Volumes": "Volumes", "Build Cache": "Build cache"}[u.Type]
		if label == "" {
			label = u.Type
		}
		value := dockerSize(u.Size)
		if r := reclaimable(u); r != "" && u.Type != "Local Volumes" {
			value += t.dim.Render(" · ") + t.warnText.Render(r+" not in use")
		}
		s.body = append(s.body, t.subtle.Render(fit(label, 12))+value)
	}
	return s
}

func (m *Model) projectCard(p project, inner int, selected string) cardSpec {
	t := m.th
	title := p.name
	if title == "" {
		title = "Other containers"
	}
	up := 0
	worst := health.OK
	for _, c := range p.containers {
		if c.State == "running" {
			up++
		}
		if st, _ := m.containerLook(c); st > worst {
			worst = st
		}
	}
	badgeStyle := t.subtle
	if worst >= health.Warn {
		badgeStyle = lipgloss.NewStyle().Foreground(t.statusColor(worst))
	}
	badge := badgeStyle.Render(fmt.Sprintf("%d/%d up", up, len(p.containers)))
	for _, c := range p.containers {
		if _, ok := m.newImage(c.Name); ok {
			badge = t.infoText.Render("new image") + t.dim.Render(" · ") + badge
			break
		}
	}
	s := cardSpec{title: title, badge: badge, selected: -1, focus: noFocus()}
	if p.name != "" {
		id := "proj:" + p.name
		s.focused = selected == id
		if s.focused {
			s.focus = [2]int{0, 1}
		}
		// The title row opens the project.
		s.hits = append(s.hits, hit{x: -2, y: -1, w: inner + 4, h: 1, id: id, click: func(m *Model) tea.Cmd { return m.openItem(tabDocker, id) }})
	}
	for _, c := range p.containers {
		id := "ctr:" + c.Name
		if p.single() {
			id = "proj:" + p.name
		}
		start := len(s.body)
		s.body = append(s.body, m.containerRow(c, inner)...)
		if p.single() {
			if s.focused {
				s.selected = start
				s.focus = [2]int{0, len(s.body)}
			}
			// The title hit already names this item for the arrow keys.
			s.hits = append(s.hits, hit{x: -2, y: start, w: inner + 4, h: len(s.body) - start, click: func(m *Model) tea.Cmd { return m.openItem(tabDocker, id) }})
			continue
		}
		if id == selected {
			s.selected = start
			s.focus = [2]int{start, len(s.body)}
		}
		s.hits = append(s.hits, hit{x: -2, y: start, w: inner + 4, h: len(s.body) - start, id: id, click: func(m *Model) tea.Cmd { return m.openItem(tabDocker, id) }})
	}
	if p.name != "" {
		start := len(s.body)
		for _, r := range m.routesTo(p.name, "") {
			s.body = append(s.body, fit(t.navText.Render("↗ ")+t.subtle.Render(r.Host+r.Path), inner))
		}
		if n := len(s.body) - start; n > 0 {
			id := "proj:" + p.name
			s.hits = append(s.hits, hit{x: -2, y: start, w: inner + 4, h: n, click: func(m *Model) tea.Cmd { return m.openItem(tabDocker, id) }})
		}
		if p.single() && s.focused {
			s.focus[1] = len(s.body)
		}
	}
	if s.focus[0] >= 0 && s.focus[1] == len(s.body) {
		s.focus[1]++ // keep the bottom border in view too
	}
	return s
}

// netRate is a container's network speed, from two readings of its counts.
type netRate struct {
	rx, tx         uint64
	at             time.Time
	rxRate, txRate float64
	ready          bool
}

// trackNet works out each container's speed since the last reading.
func (d *dockerState) trackNet(list []priv.Stats, now time.Time) {
	if d.net == nil {
		d.net = map[string]netRate{}
	}
	seen := map[string]bool{}
	for _, s := range list {
		if !s.HaveNet {
			continue
		}
		seen[s.Name] = true
		next := netRate{rx: s.NetRx, tx: s.NetTx, at: now}
		if prev, ok := d.net[s.Name]; ok && s.NetRx >= prev.rx && s.NetTx >= prev.tx {
			if dt := now.Sub(prev.at).Seconds(); dt >= 1 {
				next.rxRate = float64(s.NetRx-prev.rx) / dt
				next.txRate = float64(s.NetTx-prev.tx) / dt
				next.ready = true
			} else {
				next = prev // too soon to tell
			}
		}
		d.net[s.Name] = next
	}
	for name := range d.net {
		if !seen[name] {
			delete(d.net, name)
		}
	}
}

// netSpeed is a container's speed as "↓ in ↑ out", or "".
func (m *Model) netSpeed(name string) string {
	n, ok := m.dock.net[name]
	if !ok || !n.ready {
		return ""
	}
	return "↓" + human.Rate(n.rxRate) + " ↑" + human.Rate(n.txRate)
}

// containerRow lays out one container for the width: a table row on wide
// screens, two lines on phones so nothing is cut off.
func (m *Model) containerRow(c priv.Container, inner int) []string {
	t := m.th
	_, icon := m.containerLook(c)
	st, haveStats := m.dock.stats[c.Name]
	status := health.ContainerStatus(c, m.now)
	if c.Health != "" && c.State == "running" {
		status += " · " + c.Health
	}
	statusStyle := t.subtle
	if lvl, _ := m.containerLook(c); lvl >= health.Warn {
		statusStyle = lipgloss.NewStyle().Foreground(t.statusColor(lvl))
	}
	cpu, mem := "", ""
	if haveStats {
		cpu = fmt.Sprintf("%.1f%%", st.CPU)
		mem = human.Bytes(st.MemUsed)
	}

	if inner < 44 {
		left := icon + " " + t.bold.Render(c.Name)
		right := ""
		if haveStats {
			right = t.subtle.Render(cpu + " · " + mem)
		}
		first, ok := spread(left, right, inner)
		if !ok {
			first = fit(left, inner)
		}
		return []string{first, "  " + fit(statusStyle.Render(status)+t.dim.Render(" · "+c.Image), inner-2)}
	}

	longest := 0
	for _, x := range m.facts.Containers {
		longest = max(longest, lipgloss.Width(x.Name))
	}
	nameW := min(longest, max(inner/3, 12))
	line := icon + " " + t.bold.Render(fit(c.Name, nameW)) + " "
	cols := fmt.Sprintf("%6s %6s", cpu, mem)
	if inner >= 80 {
		spark := ""
		if h := m.dock.cpu[c.Name]; h != nil && haveStats {
			spark = t.chart(*h, 10, 1, max(100, 1), func(v float64) colorT { return t.level(t.cpu, v, 80, 95) })[0]
		} else {
			spark = strings.Repeat(" ", 10)
		}
		statusW := 26
		rest := inner - lipgloss.Width(line) - statusW - 1 - 14 - 11
		net := ""
		if rest >= 40 {
			net = fmt.Sprintf("%17s ", m.netSpeed(c.Name))
			rest -= 18
		}
		return []string{line + fit(statusStyle.Render(status), statusW) + " " + spark + " " + cols + " " + t.subtle.Render(net) + t.dim.Render(fit(c.Image, max(rest, 0)))}
	}
	statusW := inner - lipgloss.Width(line) - 14
	return []string{line + fit(statusStyle.Render(status), statusW) + " " + cols}
}

// --- details ---

func (m *Model) containerDetail(name string, b *detailBuilder) detail {
	t := m.th
	c, ok := m.container(name)
	if !ok {
		b.text("This container doesn't exist anymore.", t.subtle)
		return detail{title: name, headline: headline(t, health.Info, "gone", ""), body: b.body}
	}
	word, summary := m.containerInfo(c, b)
	m.publicAt(m.routesTo("", c.Name), b)
	if n, ok := m.newImage(c.Name); ok {
		b.text("A newer "+n.Ref+" is out. Pull & recreate on its project installs it.", t.infoText)
	}
	lvl, _ := m.containerLook(c)
	fixes := containerFixes(c)
	return detail{title: c.Name, headline: headline(t, lvl, word, summary), body: b.body, fixes: fixes}
}

// containerInfo writes a container's facts and returns its state for the
// headline.
func (m *Model) containerInfo(c priv.Container, b *detailBuilder) (word, summary string) {
	t := m.th
	word = c.State
	if c.State == "exited" && c.Exit == 0 {
		word = "stopped"
	}
	summary = health.ContainerStatus(c, m.now)
	if c.Health != "" {
		summary += " · " + c.Health
	}

	if c.OOM {
		b.text("Docker killed this container for using too much memory.", t.warnText)
	}
	if c.Error != "" {
		b.text(c.Error, t.dangerText)
	}
	b.kv("Image", c.Image)
	if c.Project != "" {
		b.kv("Project", c.Project+t.dim.Render(" · service "+c.Service))
	}
	restarts := fmt.Sprintf("%d", c.Restarts)
	if c.Policy != "" && c.Policy != "no" {
		restarts += t.dim.Render(" · policy " + c.Policy)
	}
	b.kv("Restarts", restarts)
	if len(c.Ports) > 0 {
		b.kv("Ports", strings.Join(c.Ports, ", "))
	}
	b.kv("Created", c.Created.Local().Format("2 Jan 2006 15:04"))
	if c.LogPath != "" {
		log := human.Bytes(c.LogSize)
		if c.LogUnlimited() {
			log += t.dim.Render(" · no limit")
		} else if c.LogMaxSize != "" {
			log += t.dim.Render(" · trimmed at " + c.LogMaxSize)
		}
		b.kv("Log", log)
	}
	if st, ok := m.dock.stats[c.Name]; ok {
		// Bars for what's a share of something: of all the cores, and of
		// the memory it may use.
		cores := float64(max(m.snap.Cores, 1))
		b.share("CPU", st.CPU/cores, t.level(t.cpu, st.CPU/cores, 80, 95), fmt.Sprintf("%.1f%%", st.CPU)+t.dim.Render(" of "+human.Plural(m.snap.Cores, "core", "cores")))
		if st.MemLimit > 0 {
			th := m.env.Config.Thresholds
			b.share("Memory", st.MemPct, t.level(t.mem, st.MemPct, th.MemoryWarn, th.MemoryCrit), human.Bytes(st.MemUsed)+t.dim.Render(" of "+human.Bytes(st.MemLimit)))
		} else {
			b.kv("Memory", human.Bytes(st.MemUsed))
		}
		switch {
		case st.HostNet:
			b.kv("Network", "the server's own"+t.dim.Render(" · counted with the server"))
		case st.HaveNet:
			if n := m.dock.net[c.Name]; n.ready {
				b.kv("Network", "↓ "+human.Rate(n.rxRate)+"  ↑ "+human.Rate(n.txRate))
			}
			b.kv("Since start", "↓ "+human.Bytes(st.NetRx)+"  ↑ "+human.Bytes(st.NetTx))
		default:
			b.kv("Network", st.NetIO+t.dim.Render(" in / out"))
		}
		b.kv("Disk I/O", st.BlockIO+t.dim.Render(" read / written"))
		b.kv("Processes", fmt.Sprintf("%d", st.PIDs))
	}
	return word, summary
}

func containerFixes(c priv.Container) []health.Fix {
	args := map[string]string{"container": c.Name}
	logs := health.Fix{Label: "Logs", Special: "logs", Args: args}
	var fixes []health.Fix
	if c.State == "running" || c.State == "restarting" {
		stopNote := "It stays stopped until you start it again."
		if c.Policy == "always" || c.Policy == "unless-stopped" {
			stopNote = "Docker won't restart it on its own, until you start it again."
		}
		fixes = []health.Fix{
			logs,
			{Label: "Shell", Special: "shell", Args: args},
			{Label: "Restart", Op: "docker.restart", Args: args, Done: "Restarted " + c.Name},
			{Label: "Stop", Op: "docker.stop", Args: args, Danger: true, Confirm: "Stop " + c.Name + "? " + stopNote, Done: "Stopped " + c.Name},
		}
	} else {
		fixes = []health.Fix{
			{Label: "Start", Op: "docker.start", Args: args, Done: "Started " + c.Name},
			logs,
			{Label: "Remove", Op: "docker.remove", Args: args, Danger: true, Done: "Removed " + c.Name,
				Confirm: "Remove the container " + c.Name + "? Its image and named volumes stay, but anything written inside the container itself is gone. Compose makes a new one on its next up."},
		}
	}
	return fixes
}

func (m *Model) projectDetail(name string, b *detailBuilder) detail {
	t := m.th
	var p project
	for _, x := range m.projects() {
		if x.name == name {
			p = x
		}
	}
	if len(p.containers) == 0 {
		b.text("This project has no containers anymore.", t.subtle)
		return detail{title: name, headline: headline(t, health.Info, "gone", ""), body: b.body}
	}
	up, worst := 0, health.OK
	for _, c := range p.containers {
		if c.State == "running" {
			up++
		}
		if lvl, _ := m.containerLook(c); lvl > worst {
			worst = lvl
		}
	}
	first := p.containers[0]
	for _, c := range p.containers {
		if c.ConfigFiles != "" {
			first = c // the compose labels; every container should have them
			break
		}
	}
	if first.WorkingDir != "" {
		b.kv("Directory", tildePath(first.WorkingDir))
	}
	m.publicAt(m.routesTo(name, ""), b)
	for _, c := range p.containers {
		if n, ok := m.newImage(c.Name); ok {
			b.text("A newer "+n.Ref+" is out. Pull & recreate installs it; "+c.Name+" restarts for a moment.", t.infoText)
		}
	}
	if first.ConfigFiles != "" {
		var files []string
		for _, f := range strings.Split(first.ConfigFiles, ",") {
			files = append(files, tildePath(f))
		}
		b.kv("Compose", strings.Join(files, ", "))
	}
	var word, summary string
	if p.single() {
		b.para = true
		word, summary = m.containerInfo(first, b)
	} else {
		b.para = true // a gap between the files and the containers
		for _, c := range p.containers {
			_, icon := m.containerLook(c)
			b.row(icon + " " + t.bold.Render(c.Name) + t.subtle.Render(" · "+health.ContainerStatus(c, m.now)))
		}
	}

	args := map[string]string{"project": name}
	action := func(a string) map[string]string { return map[string]string{"project": name, "action": a} }
	var fixes, last []health.Fix
	if p.single() {
		// The container's own actions, with stopping or removing it last.
		for _, f := range containerFixes(first) {
			if f.Danger {
				last = append(last, f)
			} else {
				fixes = append(fixes, f)
			}
		}
	} else {
		fixes = []health.Fix{{Label: "Restart all", Op: "docker.project", Args: action("restart"), Done: "Restarted " + name}}
	}
	if up < len(p.containers) && !p.single() {
		fixes = append(fixes, health.Fix{Label: "Start all", Op: "docker.project", Args: action("start"), Done: "Started " + name})
	}

	if first.ConfigFiles == "" {
		b.text("This project wasn't started from a compose file opsie can find, so it can't pull or rebuild it.", t.subtle)
	} else {
		pf := m.dock.preflight[name]
		switch {
		case pf.loading:
			b.text("Checking there's room to update…", t.dim)
		case pf.err != "":
			b.text("Couldn't check free space ("+pf.err+"); updating may run out of room.", t.warnText)
			fixes = append(fixes, m.updateFixes(name, args, "", "")...)
		default:
			pullBlocked, pullWarn := room(pf.p, false)
			buildBlocked, buildWarn := room(pf.p, true)
			if pullBlocked != "" {
				b.text(pullBlocked, t.dangerText)
				fixes = append(fixes, health.Fix{Label: "Free up space", Special: "open:prune"})
			} else {
				if pullWarn != "" {
					b.text(pullWarn, t.warnText)
				}
				fixes = append(fixes, m.updateFixes(name, args, pullWarn, buildWarn)...)
				if buildBlocked != "" {
					b.text(buildBlocked, t.warnText)
					fixes = fixes[:len(fixes)-1] // no rebuild
				}
			}
		}
	}
	if first.WorkingDir != "" {
		fixes = append(fixes, folderFix(first.WorkingDir), health.Fix{Label: "Backups", Special: "backup:" + name})
	}
	if p.single() {
		return detail{title: name, headline: headline(t, worst, word, summary), body: b.body, fixes: append(fixes, last...)}
	}
	fixes = append(fixes, health.Fix{Label: "Stop all", Op: "docker.project", Args: action("stop"), Danger: true,
		Confirm: "Stop every container in " + name + "? They stay stopped until you start them again.", Done: "Stopped " + name})

	summary = fmt.Sprintf("%d of %d up", up, len(p.containers))
	word = "running"
	if up == 0 {
		word = "stopped"
	}
	return detail{title: "Project " + name, headline: headline(t, worst, word, summary), body: b.body, fixes: fixes}
}

// updateFixes are the pull and rebuild actions, with any space warning in
// their confirmation.
func (m *Model) updateFixes(name string, args map[string]string, pullWarn, buildWarn string) []health.Fix {
	with := func(mode string) map[string]string {
		out := map[string]string{"mode": mode}
		for k, v := range args {
			out[k] = v
		}
		return out
	}
	join := func(parts ...string) string { return strings.TrimSpace(strings.Join(parts, " ")) }
	return []health.Fix{
		{Label: "Pull & recreate", Op: "compose.update", Args: with("pull"), Danger: true, Done: "Updated " + name,
			Confirm: join("Pull newer images for "+name+" and recreate its containers? Services restart, so expect a short outage.", pullWarn)},
		{Label: "Rebuild & recreate", Op: "compose.update", Args: with("build"), Danger: true, Done: "Rebuilt " + name,
			Confirm: join("Rebuild "+name+"'s images from their Dockerfiles and recreate its containers? Building can take a while; services restart at the end.", buildWarn)},
	}
}

// dockerSize rewrites Docker's sizes (7.203GB) the way the rest of opsie
// shows them (6.7G).
func dockerSize(s string) string {
	if n := priv.ParseSize(s); n > 0 || strings.HasPrefix(s, "0") {
		return human.Bytes(n)
	}
	return s
}

// reclaimable is the space a prune could free, or "" for none.
func reclaimable(u priv.DiskUsage) string {
	f := strings.Fields(u.Reclaimable)
	if len(f) == 0 || priv.ParseSize(f[0]) == 0 {
		return ""
	}
	return dockerSize(f[0])
}

func (m *Model) pruneDetail(b *detailBuilder) detail {
	t := m.th
	plain := lipgloss.NewStyle()
	if len(m.dock.df) == 0 {
		b.text("Measuring disk use…", t.dim)
	}
	for _, u := range m.dock.df {
		label := map[string]string{"Images": "Images", "Containers": "Containers", "Local Volumes": "Volumes", "Build Cache": "Build cache"}[u.Type]
		value := dockerSize(u.Size)
		if u.Type != "Build Cache" {
			value += t.dim.Render(fmt.Sprintf(" · %d, %d in use", u.Total, u.Active))
		}
		if r := reclaimable(u); r != "" {
			value += t.dim.Render(" · ") + t.warnText.Render(r+" not in use")
		}
		b.kv(label, value)
	}

	// What each button would free, so none of them is a surprise.
	var leftover, unused uint64
	var named []string
	for _, img := range m.dock.unused {
		if img.Ref == "" {
			leftover += img.Size
			continue
		}
		unused += img.Size
		named = append(named, img.Ref)
	}
	var cache uint64
	for _, u := range m.dock.df {
		if u.Type == "Build Cache" {
			cache = priv.ParseSize(strings.Fields(u.Reclaimable + " 0")[0])
		}
	}
	var stopped []string
	for _, c := range m.facts.Containers {
		if c.State != "running" && c.State != "restarting" && c.State != "paused" {
			stopped = append(stopped, c.Name)
		}
	}

	b.text("Clean up removes leftover image layers and the build cache: about "+human.Bytes(leftover+cache)+" now. Nothing can use those again, so it's always safe.", plain)
	fixes := []health.Fix{{Label: "Clean up", Op: "docker.prune", Args: map[string]string{"mode": "safe"}, Done: "Cleaned up"}}
	if len(named) > 0 {
		b.text(fmt.Sprintf("Most of the space is in %s no container uses, about %s: older versions left behind by updates, and images of things that aren't set up anymore.", human.Plural(len(named), "image", "images"), human.Bytes(unused)), plain)
		for _, img := range m.dock.unused {
			if img.Ref == "" {
				continue
			}
			b.row(t.subtle.Render(fmt.Sprintf("%6s", human.Bytes(img.Size))) + "  " + img.Ref)
		}
		b.text("Removing them costs nothing until one is needed again: then it's downloaded again, or built again if it was built on this server.", t.subtle)
		confirm := fmt.Sprintf("Remove these %s? ", human.Plural(len(named), "image", "images")) + strings.Join(named, ", ") + ". One that's needed later is downloaded again; one that was built on this server has to be built again."
		fixes = append(fixes, health.Fix{Label: "Remove unused images", Op: "docker.prune", Args: map[string]string{"mode": "unused"}, Danger: true, Confirm: confirm, Done: "Cleaned up"})
	}
	deep := "Deep clean also removes stopped containers and unused networks, and then every image that leaves unused."
	deepConfirm := "Remove every unused image, the whole build cache, unused networks and every stopped container?"
	if len(stopped) > 0 {
		deep += " Right now the stopped containers are " + strings.Join(stopped, ", ") + "."
		deepConfirm = "Remove every unused image, the whole build cache, unused networks and these stopped containers: " + strings.Join(stopped, ", ") + "?"
	}
	b.text(deep+" Volumes, where containers keep their data, are never touched.", plain)
	fixes = append(fixes, health.Fix{Label: "Deep clean", Op: "docker.prune", Args: map[string]string{"mode": "all"}, Danger: true, Confirm: deepConfirm, Done: "Cleaned up"})
	return detail{title: "Docker disk space", body: b.body, fixes: fixes}
}

// publicAt lists the public hostnames that lead to something.
func (m *Model) publicAt(routes []priv.Route, b *detailBuilder) {
	for i, r := range routes {
		label := ""
		if i == 0 {
			label = "Public at"
		}
		b.kv(label, r.Host+r.Path)
	}
}

// newImage reports whether a registry has a newer image for a container.
func (m *Model) newImage(container string) (registry.Check, bool) {
	for _, c := range m.facts.ImageChecks {
		if c.Container == container && c.Newer {
			return c, true
		}
	}
	return registry.Check{}, false
}
