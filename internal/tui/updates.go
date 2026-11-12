package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
)

func (m *Model) updatesSections(inner int) []section {
	t := m.th
	f := m.facts
	if !m.haveFacts {
		return []section{{title: "Updates", intro: []string{t.dim.Render("Checking…")}}}
	}
	if !f.Admin.Available {
		return []section{{title: "Updates", intro: wrap("Updates can only be checked with admin access, which is locked.", inner),
			entries: []entry{{id: "admin", lines: m.action("Unlock admin access", inner)}}}}
	}
	return []section{m.packagesSection(inner), m.autoSection(inner), m.restartSection(inner)}
}

func (m *Model) packagesSection(inner int) section {
	t := m.th
	f := m.facts
	s := section{title: "System packages (apt)"}
	if f.AptErr != "" {
		s.intro = wrap(f.AptErr, inner)
		return s
	}
	a := f.Apt
	sec := 0
	for _, p := range a.Packages {
		if p.Security {
			sec++
		}
	}
	switch {
	case sec > 0:
		s.badge = t.warnText.Render(fmt.Sprintf("%d · %d security", len(a.Packages), sec))
	case len(a.Packages) > 0:
		s.badge = t.subtle.Render(fmt.Sprintf("%d waiting", len(a.Packages)))
	default:
		s.badge = t.okText.Render("up to date")
	}
	checked := "never"
	if !a.ListsAt.IsZero() {
		checked = human.Duration(m.now.Sub(a.ListsAt)) + " ago"
	}
	s.entries = append(s.entries, entry{id: "apt:lists", lines: m.itemRow("Last checked", t.subtle.Render(checked), inner)})
	if len(a.Packages) == 0 {
		s.outro = []string{t.dim.Render("Every package is the newest version."), t.dim.Render("Docker images are on the Docker tab.")}
		return s
	}
	s.entries = append(s.entries, entry{id: "apt:all", lines: m.action("Install "+human.Plural(len(a.Packages), "update", "updates"), inner)})
	for _, p := range a.Packages {
		icon := t.dim.Render("○")
		if p.Security {
			icon = t.statusIcon(health.Warn)
		}
		right := t.dim.Render(p.To)
		s.entries = append(s.entries, entry{id: "pkg:" + p.Name, lines: m.itemRow(icon+" "+t.bold.Render(p.Name), right, inner)})
	}
	return s
}

func (m *Model) autoSection(inner int) section {
	t := m.th
	a := m.facts.Apt
	s := section{title: "Automatic updates"}
	if m.facts.AptErr != "" {
		return s
	}
	word := t.warnText.Render("off")
	icon := t.statusIcon(health.Warn)
	if a.AutoOn {
		icon, word = t.statusIcon(health.OK), t.okText.Render("on")
		if !a.AutoAt.IsZero() {
			word += t.subtle.Render(" · ran " + human.Duration(m.now.Sub(a.AutoAt)) + " ago")
		}
	}
	s.entries = []entry{{id: "auto", lines: m.itemRow(icon+" Security updates", word, inner)}}
	return s
}

func (m *Model) restartSection(inner int) section {
	t := m.th
	f := m.facts
	s := section{title: "Waiting for a restart"}
	n := len(f.Stale)
	switch {
	case !f.Scheduled.At.IsZero():
		n++
		s.entries = append(s.entries, entry{id: "reboot", lines: m.itemRow(t.statusIcon(health.Info)+" "+t.bold.Render("Server"), t.infoText.Render("restarts at "+f.Scheduled.At.Local().Format("15:04")), inner)})
	case f.RebootRequired:
		n++
		s.entries = append(s.entries, entry{id: "reboot", lines: m.itemRow(t.statusIcon(health.Warn)+" "+t.bold.Render("Server"), t.warnText.Render(rebootWhy(f.RebootPackages)), inner)})
	}
	for _, st := range f.Stale {
		right := t.subtle.Render(human.Plural(len(st.Libs), "library", "libraries"))
		s.entries = append(s.entries, entry{id: "stale:" + st.Unit, lines: m.itemRow(t.dim.Render("↻")+" "+strings.TrimSuffix(st.Unit, ".service"), right, inner)})
	}
	if f.StaleErr != "" {
		s.intro = wrap(f.StaleErr, inner)
	} else if n == 0 {
		s.intro = []string{t.dim.Render("Nothing is waiting for a restart.")}
	}
	s.badge = t.subtle.Render(fmt.Sprintf("%d", n))
	return s
}

func rebootWhy(pkgs []string) string {
	for _, p := range pkgs {
		if strings.HasPrefix(p, "linux-image") {
			return "new kernel"
		}
	}
	if len(pkgs) > 0 {
		return human.Plural(len(pkgs), "package", "packages")
	}
	return "needed"
}

func (m *Model) updatesDetail(key string, b *detailBuilder) (detail, bool) {
	t := m.th
	f := m.facts
	plain := lipgloss.NewStyle()
	kind, arg, _ := strings.Cut(key, ":")
	switch kind {
	case "apt":
		if arg == "lists" {
			b.text("apt keeps lists of the newest packages, and only knows about an update after downloading them again.", plain)
			if !f.Apt.ListsAt.IsZero() {
				b.kv("Checked", f.Apt.ListsAt.Local().Format("Mon 2 Jan 15:04")+" · "+human.Duration(m.now.Sub(f.Apt.ListsAt))+" ago")
			}
			if f.Apt.AutoOn {
				b.text("Automatic updates check every day on their own.", t.subtle)
			}
			return detail{title: "Package lists", body: b.body, fixes: []health.Fix{health.CheckFix()}}, true
		}
		var names []string
		for _, p := range f.Apt.Packages {
			names = append(names, p.Name)
		}
		b.text("Installs every update with apt-get upgrade, keeping your changes to config files. Packages are never removed.", plain)
		b.text(strings.Join(names, ", "), t.subtle)
		if f.Apt.Free > 0 {
			b.kv("Free", human.Bytes(f.Apt.Free)+" for downloads")
		}
		return detail{title: "Install updates", body: b.body, fixes: health.InstallAllFix(f)}, true

	case "pkg":
		for _, p := range f.Apt.Packages {
			if p.Name != arg {
				continue
			}
			st, word := health.Info, "update"
			if p.Security {
				st, word = health.Warn, "security update"
			}
			b.kv("Installed", p.From)
			b.kv("New", p.To)
			b.kv("From", p.Suite)
			if strings.HasPrefix(p.Name, "linux-image") || p.Name == "libc6" || strings.HasPrefix(p.Name, "systemd") {
				b.text("This one takes full effect after restarting the server.", t.subtle)
			}
			return detail{title: p.Name, headline: headline(t, st, word, ""), body: b.body, fixes: []health.Fix{{
				Label: "Install this update", Op: "apt.upgrade", Args: map[string]string{"package": p.Name}, Done: "Updated " + p.Name,
			}}}, true
		}

	case "auto":
		a := f.Apt
		st, word := health.Warn, "off"
		if a.AutoOn {
			st, word = health.OK, "on"
			b.text("unattended-upgrades installs security updates from Debian every day. It never restarts the server; restarts it needs show up here.", plain)
			if !a.AutoAt.IsZero() {
				b.kv("Last ran", a.AutoAt.Local().Format("Mon 2 Jan 15:04"))
			}
		} else {
			b.text("Security fixes only get installed when you do it. unattended-upgrades can install them every day on its own, only from Debian's security updates, and never restarts the server.", plain)
			if !a.AutoInstalled {
				b.text("It isn't installed yet; turning it on installs it.", t.subtle)
			}
		}
		return detail{title: "Automatic updates", headline: headline(t, st, word, ""), body: b.body, fixes: health.AutoFixes(f)}, true

	case "reboot":
		if !f.Scheduled.At.IsZero() {
			b.text("The server restarts at "+f.Scheduled.At.Local().Format("15:04 on Mon 2 Jan")+", in "+human.Duration(f.Scheduled.At.Sub(m.now))+". New logins are refused for the five minutes before.", plain)
			return detail{title: "Restart server", headline: headline(t, health.Info, "set", f.Scheduled.At.Local().Format("15:04")), body: b.body, fixes: health.RebootFixes(f)}, true
		}
		if !f.RebootRequired {
			break
		}
		b.text("Updates were installed that only take effect after a restart.", plain)
		if len(f.RebootPackages) > 0 {
			b.kv("Waiting", strings.Join(f.RebootPackages, ", "))
		}
		b.text("Restarting disconnects everyone, including this session. Containers set to restart come back on their own.", t.subtle)
		return detail{title: "Restart server", headline: headline(t, health.Warn, "needed", rebootWhy(f.RebootPackages)), body: b.body, fixes: health.RebootFixes(f)}, true

	case "stale":
		for _, st := range f.Stale {
			if st.Unit != arg {
				continue
			}
			b.text("This still runs code from before an update replaced it, so it doesn't have the update's fixes yet. Restarting it loads the new version.", plain)
			for i, lib := range st.Libs {
				label := ""
				if i == 0 {
					label = "Replaced"
				}
				b.kv(label, lib)
			}
			fixes := health.StaleFixes(st.Unit)
			if len(fixes) == 0 {
				b.text("Restarting this one would disrupt the whole system; restart the server instead when it suits you.", t.subtle)
			}
			return detail{title: strings.TrimSuffix(st.Unit, ".service"), headline: headline(t, health.Info, "restart to update", ""), body: b.body, fixes: fixes}, true
		}
	}
	return detail{}, false
}
