package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aloglu/opsie/internal/audit"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
)

// auditShown is how many actions the history lists.
const auditShown = 60

// describeAction says what a logged action did, in words.
func describeAction(e audit.Entry) string {
	a := e.Args
	unit := strings.TrimSuffix(a["unit"], ".service")
	words := map[string]string{
		"unit.restart": "Restarted " + unit, "unit.start": "Started " + unit, "unit.stop": "Stopped " + unit,
		"unit.reload": "Reloaded " + unit, "unit.enable": unit + " starts at boot", "unit.disable": unit + " doesn't start at boot",
		"unit.reset-failed": "Cleared " + unit + "'s failure",
		"docker.restart":    "Restarted " + a["container"], "docker.start": "Started " + a["container"],
		"docker.stop": "Stopped " + a["container"], "docker.remove": "Removed " + a["container"],
		"docker.project":      map[string]string{"start": "Started", "stop": "Stopped", "restart": "Restarted"}[a["action"]] + " all of " + a["project"],
		"compose.update":      map[string]string{"pull": "Pulled new images for ", "build": "Rebuilt ", "recreate": "Recreated "}[a["mode"]] + a["project"],
		"compose.up":          "Started the app in " + tildePath(a["dir"]),
		"compose.bindlocal":   "Made port " + a["port"] + "/" + a["proto"] + " of " + a["project"] + " local only",
		"docker.prune":        "Docker clean-up",
		"docker.log-limit":    "Limited logs for new containers",
		"docker.log-truncate": "Emptied " + a["container"] + "'s log",
		"backup.create":       "Backed up " + a["project"], "backup.restore": "Restored " + a["project"] + " from " + filepath.Base(a["file"]),
		"backup.delete": "Deleted backup " + filepath.Base(a["file"]), "backup.verify": "Checked backup " + filepath.Base(a["file"]),
		"backup.discard": "Deleted " + a["project"] + "'s set-aside data",
		"journal.vacuum": "Trimmed the system log", "apt.clean": "Cleared downloaded packages", "apt.autoremove": "Removed unneeded packages",
		"apt.update": "Checked for updates", "apt.upgrade": "Installed updates", "apt.auto-on": "Turned on automatic security updates",
		"system.reboot": "Restarted the server", "system.reboot-at": "Set a restart for " + a["at"], "system.reboot-cancel": "Cancelled the restart",
		"systemd.reexec": "Restarted systemd", "time.ntp-on": "Turned on time sync",
		"ssh.key-add": "Added an SSH key", "ssh.key-remove": "Removed an SSH key",
		"ssh.passwords": "SSH passwords: " + a["enabled"], "ssh.rootlogin": "SSH root login: " + a["value"],
		"session.terminate": "Ended a login session", "sudo.passwordless": "Passwordless sudo: " + a["enabled"],
		"ufw.enable": "Turned the firewall on", "ufw.disable": "Turned the firewall off", "ufw.install": "Installed ufw",
		"ufw.allow": "Allowed " + a["port"] + " in the firewall", "ufw.deny": "Blocked " + a["from"] + " in the firewall",
		"ufw.delete": "Removed a firewall rule", "fail2ban.install": "Installed fail2ban",
		"proc.signal": "Sent " + a["signal"] + " to process " + a["pid"], "proc.renice": "Changed process " + a["pid"] + "'s priority",
		"service.install": "Turned on background checks", "service.remove": "Turned off background checks",
	}
	if w, ok := words[e.Action]; ok && strings.TrimSpace(w) != "" {
		return w
	}
	// Anything else: the operation and its arguments.
	var parts []string
	for k, v := range a {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.TrimSpace(e.Action + " " + strings.Join(parts, " "))
}

// auditCache is the parsed log, read again only when the file changes.
type auditCache struct {
	mod  time.Time
	size int64
	list []audit.Done
}

func (m *Model) auditList() []audit.Done {
	path := m.env.Paths.AuditLog()
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if c := m.audit; c.mod.Equal(info.ModTime()) && c.size == info.Size() {
		return c.list
	}
	m.audit = auditCache{mod: info.ModTime(), size: info.Size(), list: audit.Read(path, auditShown)}
	return m.audit.list
}

func (m *Model) auditDetail(b *detailBuilder) detail {
	t := m.th
	list := m.auditList()
	if len(list) == 0 {
		b.text("Nothing yet. Every change opsie makes to the server is listed here once it's made.", t.subtle)
		return detail{title: "Actions taken", body: b.body}
	}
	b.text("Every change opsie made to the server from this dashboard, newest first. It's all in "+tildePath(m.env.Paths.AuditLog())+".", t.subtle)
	b.para = true
	failed := 0
	for _, e := range list {
		icon, rest := t.okText.Render("✓"), ""
		switch {
		case e.Unfinished:
			icon, rest = t.warnText.Render("?"), t.dim.Render(" · no result: opsie closed while it ran")
		case e.Result == "failed":
			failed++
			icon, rest = t.dangerText.Render("✗"), t.dangerText.Render(" · "+e.Error)
		}
		when := t.dim.Render(e.Time.Local().Format("2 Jan 15:04"))
		b.row(when + " " + icon + " " + describeAction(e.Entry) + rest)
	}
	summary := ""
	if failed > 0 {
		summary = human.Plural(failed, "failed", "failed")
	}
	return detail{title: "Actions taken", headline: headline(t, health.Info, "history", summary), body: b.body}
}
