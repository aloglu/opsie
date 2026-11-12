package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/audit"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/priv"
	"github.com/aloglu/opsie/internal/sites"
)

// --- tunnels ---

func (m *Model) tunnel(name string) (priv.Tunnel, bool) {
	for _, t := range m.facts.Tunnels {
		if t.Name == name {
			return t, true
		}
	}
	return priv.Tunnel{}, false
}

func routeID(r priv.Route) string { return "route:" + r.Host + r.Path }

func (m *Model) route(id string) (priv.Tunnel, priv.Route, bool) {
	for _, t := range m.facts.Tunnels {
		for _, r := range t.Routes {
			if routeID(r) == id {
				return t, r, true
			}
		}
	}
	return priv.Tunnel{}, priv.Route{}, false
}

// routeContainer finds the container a route leads to, by the name
// cloudflared uses to reach it.
func (m *Model) routeContainer(r priv.Route) (priv.Container, bool) {
	target := r.Target()
	if target == "" {
		return priv.Container{}, false
	}
	for _, c := range m.facts.Containers {
		if c.Name == target {
			return c, true
		}
	}
	for _, c := range m.facts.Containers {
		if c.Service == target {
			return c, true
		}
	}
	return priv.Container{}, false
}

// routesTo lists the public hostnames that lead into a project, or to a
// container when the project is "".
func (m *Model) routesTo(project, container string) []priv.Route {
	var out []priv.Route
	for _, t := range m.facts.Tunnels {
		for _, r := range t.Routes {
			c, ok := m.routeContainer(r)
			if ok && ((project != "" && c.Project == project) || (container != "" && c.Name == container)) {
				out = append(out, r)
			}
		}
	}
	return out
}

func (m *Model) originError(t priv.Tunnel, host string) (priv.OriginError, bool) {
	for _, o := range t.Origins {
		if o.Host == host {
			return o, true
		}
	}
	return priv.OriginError{}, false
}

// tunnelLook is a tunnel's status, word and connection summary.
func tunnelLook(t priv.Tunnel) (health.Status, string) {
	switch {
	case !t.Running:
		return health.Crit, "stopped"
	case t.HaveReady && t.Ready == 0:
		return health.Crit, "not connected"
	case !t.HaveReady:
		return health.Info, "running"
	}
	return health.OK, "connected"
}

func (m *Model) tunnelSection(tn priv.Tunnel, inner int) section {
	t := m.th
	st, word := tunnelLook(tn)
	title := "Tunnel"
	if len(m.facts.Tunnels) > 1 {
		title = "Tunnel " + tn.Name
	}
	s := section{title: title}
	style := lipgloss.NewStyle().Foreground(t.statusColor(st))
	right := ""
	if st == health.OK {
		right = human.Plural(tn.Ready, "connection", "connections")
		s.badge = t.okText.Render(word)
	} else {
		s.badge = style.Render(word)
	}
	if tn.Drops > 0 && inner >= 40 {
		right += " · " + human.Plural(tn.Drops, "drop", "drops") + " in 24h"
	}
	s.entries = append(s.entries, entry{id: "tunnel:" + tn.Name, lines: m.itemRow(t.statusIcon(st)+" "+style.Bold(true).Render(capitalize(word)), t.subtle.Render(right), inner)})
	if !tn.Remote {
		s.outro = wrap(t.dim.Render("Its hostnames are set in a local config file, which opsie doesn't read."), inner)
		return s
	}
	for _, r := range tn.Routes {
		icon := t.navText.Render("↗")
		right := t.dim.Render(r.Service)
		if c, ok := m.routeContainer(r); ok {
			name := c.Project
			if name == "" {
				name = c.Name
			}
			right = t.subtle.Render(name)
			if c.State != "running" {
				icon, right = t.statusIcon(health.Crit), t.dangerText.Render(name+" stopped")
			}
		}
		if o, ok := m.originError(tn, r.Host); ok && o.Recent > 0 {
			icon, right = t.statusIcon(health.Warn), t.warnText.Render("down")
		}
		if site, ok := m.site("https://" + r.Host + r.Path); ok {
			switch {
			case !site.Up():
				icon, right = t.statusIcon(health.Crit), t.dangerText.Render("down")
			case inner >= 40:
				right += t.dim.Render(" · " + human.Seconds(site.Took))
			}
		}
		s.entries = append(s.entries, entry{id: routeID(r), lines: m.itemRow(icon+" "+r.Host+r.Path, right, inner)})
	}
	if len(tn.Routes) == 0 {
		s.outro = []string{t.dim.Render("No public hostnames yet.")}
	}
	return s
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (m *Model) tunnelDetail(name string, b *detailBuilder) detail {
	t := m.th
	tn, ok := m.tunnel(name)
	if !ok {
		b.text("This tunnel isn't running here anymore.", t.subtle)
		return detail{title: "Tunnel", headline: headline(t, health.Info, "gone", ""), body: b.body}
	}
	st, word := tunnelLook(tn)
	summary := ""
	if tn.Running && !tn.Since.IsZero() {
		summary = "for " + human.Duration(m.now.Sub(tn.Since))
	}
	switch {
	case !tn.Running:
		b.text("cloudflared isn't running, so every site behind the tunnel is unreachable from the internet.", t.dangerText)
	case st == health.Crit:
		b.text("cloudflared runs but has no connection to Cloudflare. Its log usually says why.", t.dangerText)
	case !tn.HaveReady:
		b.text("Its status page didn't answer, so opsie can't tell whether it's connected.", t.subtle)
	}
	if tn.HaveReady {
		conn := fmt.Sprintf("%d", tn.Ready)
		if len(tn.Locations) > 0 {
			conn += t.dim.Render(" · " + strings.Join(tn.Locations, ", "))
		}
		b.kv("Connections", conn)
	}
	if tn.Requests > 0 {
		b.kv("Requests", humanCount(tn.Requests)+t.dim.Render(" since it started"))
		if n := tn.Statuses["5xx"]; n > 0 {
			b.kv("Errors", t.warnText.Render(humanCount(n))+t.dim.Render(" answered with a server error"))
		}
	}
	if tn.Drops > 0 {
		b.kv("Drops", fmt.Sprintf("%d in 24h", tn.Drops)+t.dim.Render(" · last "+human.Duration(m.now.Sub(tn.LastDrop))+" ago"))
	}
	if tn.Version != "" {
		b.kv("Version", tn.Version)
	}
	kind := "Container"
	if tn.Kind == "service" {
		kind = "Service"
	}
	b.kv(kind, tn.Name)
	if tn.TunnelID != "" {
		b.kv("Tunnel ID", tn.TunnelID)
	}
	if tn.Drops > 0 && st == health.OK {
		b.text("Short drops are normal: cloudflared keeps several connections and reconnects on its own.", t.subtle)
	}
	for _, o := range tn.Origins {
		if o.Recent > 0 {
			b.text(o.Host+": "+o.Error+".", t.warnText)
		}
	}
	if tn.Remote && len(tn.Routes) > 0 {
		b.para = true
		for _, r := range tn.Routes {
			if b.para {
				b.body, b.para = append(b.body, ""), false
			}
			b.body = append(b.body, m.itemRow(t.navText.Render("↗ ")+r.Host+r.Path, t.dim.Render("→ "+r.Service), b.inner)...)
		}
	}
	fixes := health.TunnelFixes(tn)[1:] // not "Open tunnel": it's open
	if tn.Kind == "service" {
		fixes = append(fixes, health.Fix{Label: "Open service", Special: "open:services:unit:" + tn.Name})
	}
	return detail{title: "Tunnel", headline: headline(t, st, word, summary), body: b.body, fixes: fixes}
}

func humanCount(n uint64) string {
	switch {
	case n >= 10_000_000:
		return fmt.Sprintf("%.0fM", float64(n)/1e6)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}

func (m *Model) routeDetail(id string, b *detailBuilder) detail {
	t := m.th
	tn, r, ok := m.route(id)
	if !ok {
		b.text("The tunnel doesn't have this hostname anymore.", t.subtle)
		return detail{title: "Hostname", headline: headline(t, health.Info, "gone", ""), body: b.body}
	}
	st, word := health.OK, "public"
	b.kv("Address", "https://"+r.Host+r.Path)
	b.kv("Leads to", r.Service)
	var fixes []health.Fix
	c, found := m.routeContainer(r)
	if found {
		app := c.Name
		if c.Project != "" {
			app = c.Project
		}
		b.kv("App", app+t.dim.Render(" · "+health.ContainerStatus(c, m.now)))
		target := "ctr:" + c.Name
		for _, p := range m.projects() {
			if p.name == c.Project && p.name != "" && (p.single() || len(p.containers) > 0) {
				target = "proj:" + p.name
			}
		}
		fixes = append(fixes, health.Fix{Label: "Open " + app, Special: "open:docker:" + target})
		if c.State != "running" {
			st, word = health.Crit, "app stopped"
			b.text(app+" isn't running, so this address shows an error page.", t.dangerText)
		}
	} else if r.Target() != "" {
		b.text("opsie can't tell which app this is; it isn't a container name here.", t.subtle)
	}
	if site, ok := m.site("https://" + r.Host + r.Path); ok {
		m.siteFacts(site, b)
		if !site.Up() {
			st, word = health.Crit, "down"
		}
	}
	if o, ok := m.originError(tn, r.Host); ok {
		when := human.Duration(m.now.Sub(o.Last)) + " ago"
		b.kv("Failed", fmt.Sprintf("%s in 24h · last %s", human.Plural(o.Count, "request", "requests"), when))
		style := t.subtle
		if o.Recent > 0 {
			style = t.warnText
			if st == health.OK {
				st, word = health.Warn, "down"
			}
		}
		b.text("cloudflared couldn't reach it: "+o.Error+".", style)
	}
	fixes = append(fixes, health.Fix{Label: "Tunnel logs", Special: "logs", Args: tunnelLogArgs(tn)})
	return detail{title: r.Host, headline: headline(t, st, word, ""), body: b.body, fixes: fixes}
}

func tunnelLogArgs(tn priv.Tunnel) map[string]string {
	if tn.Kind == "service" {
		return map[string]string{"unit": tn.Name}
	}
	return map[string]string{"container": tn.Name}
}

// --- app folders ---

// appFolder is a folder in the apps directory with a compose file.
type appFolder struct {
	name, dir, compose string
}

// readApps lists the app folders in dir.
func readApps(dir string) ([]appFolder, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []appFolder
	for _, e := range entries {
		// Hidden folders, and an app's old data a restore set aside.
		if strings.HasPrefix(e.Name(), ".") || strings.Contains(e.Name(), ".before-restore-") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			continue
		}
		if f := priv.FindCompose(p); f != "" {
			out = append(out, appFolder{name: e.Name(), dir: p, compose: f})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// idleApps are the app folders nothing is running from.
func (m *Model) idleApps() []appFolder {
	var out []appFolder
	for _, a := range m.dock.apps {
		running := false
		for _, c := range m.facts.Containers {
			if (c.WorkingDir != "" && sameDir(c.WorkingDir, a.dir)) || (c.WorkingDir == "" && c.Project == strings.ToLower(a.name)) {
				running = true
				break
			}
		}
		if !running {
			out = append(out, a)
		}
	}
	return out
}

func (m *Model) idleSection(apps []appFolder, inner int) section {
	t := m.th
	s := section{title: "Not started", badge: t.subtle.Render(fmt.Sprintf("%d", len(apps)))}
	for _, a := range apps {
		s.entries = append(s.entries, entry{id: "app:" + a.dir, lines: m.itemRow(t.dim.Render("○")+" "+a.name, t.dim.Render(filepath.Base(a.compose)), inner)})
	}
	s.outro = wrap(t.dim.Render("Folders in "+tildePath(m.appsDir())+" with a compose file and nothing running."), inner)
	return s
}

func (m *Model) appsDir() string {
	home, _ := os.UserHomeDir()
	return m.env.Config.Apps(home)
}

func (m *Model) appDetail(dir string, b *detailBuilder) detail {
	t := m.th
	for _, a := range m.dock.apps {
		if a.dir != dir {
			continue
		}
		b.kv("Folder", tildePath(a.dir))
		b.kv("Compose", filepath.Base(a.compose))
		running := true
		for _, x := range m.idleApps() {
			if x.dir == dir {
				running = false
			}
		}
		if running {
			b.text("This app is running now; it's on the Docker tab.", t.subtle)
			return detail{title: a.name, headline: headline(t, health.OK, "running", ""), body: b.body, fixes: []health.Fix{folderFix(a.dir)}}
		}
		b.text("Nothing from this app is running. Starting it runs docker compose up in its folder, which downloads or builds its images first if it needs to.", lipgloss.NewStyle())
		return detail{title: a.name, headline: headline(t, health.Info, "not started", ""), body: b.body, fixes: []health.Fix{
			{Label: "Start", Op: "compose.up", Args: map[string]string{"dir": a.dir}, Done: "Started " + a.name},
			folderFix(a.dir),
		}}
	}
	b.text("This folder isn't there anymore.", t.subtle)
	return detail{title: filepath.Base(dir), body: b.body}
}

func folderFix(dir string) health.Fix {
	return health.Fix{Label: "Open folder", Special: "folder", Args: map[string]string{"dir": dir}}
}

// folderShell opens your own shell in a folder, handing over the terminal
// until it exits. It runs as you, not as root.
func (m *Model) folderShell(dir string) tea.Cmd {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return m.setFlash(tildePath(dir)+" isn't there anymore", flashError, 5*time.Second)
	}
	m.modal = nil
	_ = audit.Log(m.env.Paths.AuditLog(), audit.Entry{Action: "folder.shell", Args: map[string]string{"dir": dir}, Result: "requested"})
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	script := `printf '\n  In %s. Type exit to go back to opsie.\n\n' "$1"; cd "$2" && exec "$3"`
	cmd := exec.Command("sh", "-c", script, "opsie-folder", tildePath(dir), dir, shell)
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return shellDoneMsg{container: tildePath(dir), err: err} })
}

// --- site checks ---

func (m *Model) site(url string) (sites.Result, bool) {
	for _, s := range m.facts.Sites {
		if s.URL == url {
			return s, true
		}
	}
	return sites.Result{}, false
}

// siteFacts writes what the last visit found.
func (m *Model) siteFacts(s sites.Result, b *detailBuilder) {
	t := m.th
	ago := human.Duration(m.now.Sub(s.At)) + " ago"
	if s.Up() {
		b.kv("Visited", fmt.Sprintf("%d in %s", s.Status, human.Seconds(s.Took))+t.dim.Render(" · "+ago))
	} else {
		b.kv("Visited", t.dangerText.Render(s.Problem())+t.dim.Render(" · "+ago))
	}
	if !s.CertExpires.IsZero() {
		b.kv("Certificate", "until "+s.CertExpires.Local().Format("2 Jan 2006"))
	}
}

// extraSites are the sites from the settings, which aren't a tunnel's.
func (m *Model) extraSites() []sites.Result {
	var out []sites.Result
	for _, s := range m.facts.Sites {
		if !s.Route {
			out = append(out, s)
		}
	}
	return out
}

func (m *Model) sitesSection(list []sites.Result, inner int) section {
	t := m.th
	s := section{title: "Sites"}
	down := 0
	for _, r := range list {
		icon, right := t.okText.Render("●"), t.subtle.Render(fmt.Sprintf("%d · %s", r.Status, human.Seconds(r.Took)))
		if !r.Up() {
			icon, right = t.statusIcon(health.Crit), t.dangerText.Render("down")
			down++
		}
		s.entries = append(s.entries, entry{id: "site:" + r.URL, lines: m.itemRow(icon+" "+r.Host, right, inner)})
	}
	s.badge = t.okText.Render("all up")
	if down > 0 {
		s.badge = t.dangerText.Render(fmt.Sprintf("%d down", down))
	}
	return s
}

func (m *Model) siteDetail(url string, b *detailBuilder) detail {
	t := m.th
	s, ok := m.site(url)
	if !ok {
		b.text("This site isn't checked anymore.", t.subtle)
		return detail{title: "Site", body: b.body}
	}
	b.kv("Address", s.URL)
	m.siteFacts(s, b)
	st, word := health.OK, "up"
	if !s.Up() {
		st, word = health.Crit, "down"
		b.text("Visitors can't use it right now. opsie checks again every "+human.Duration(sites.Every)+".", t.dangerText)
	}
	b.text("You added this one in Settings. opsie visits it like a browser would and only looks at whether it answers.", t.subtle)
	return detail{title: s.Host, headline: headline(t, st, word, ""), body: b.body,
		fixes: []health.Fix{{Label: "Check again", Special: "sites:recheck"}}}
}
