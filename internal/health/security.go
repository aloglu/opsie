package health

import (
	"fmt"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aloglu/opsie/internal/facts"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/priv"
)

// The fixes here are shared by the checklist and the Security and Updates
// tabs, so an action looks and behaves the same wherever it's offered.

// PasswordsOn reports whether SSH accepts passwords in any form.
func PasswordsOn(f facts.Facts) bool {
	return f.SSHD["passwordauthentication"] == "yes" || f.SSHD["kbdinteractiveauthentication"] == "yes"
}

// MyKeys is how many keys can log in to the account running opsie.
func MyKeys(f facts.Facts) int {
	for _, k := range f.Keys {
		if k.User == f.User {
			return len(k.Keys)
		}
	}
	return 0
}

// PasswordFixes turn SSH password login on or off.
func PasswordFixes(f facts.Facts) []Fix {
	if f.SSHDErr != "" {
		return nil
	}
	if PasswordsOn(f) {
		if MyKeys(f) == 0 {
			return nil // turning passwords off would lock you out
		}
		return []Fix{{
			Label: "Turn off password login", Op: "ssh.passwords", Args: map[string]string{"enabled": "no"}, Danger: true,
			Confirm: "Only SSH keys will work after this. You have " + human.Plural(MyKeys(f), "key", "keys") + ", so you'll still get in; anyone who logs in with a password won't. Sessions already open stay open.",
			Done:    "Password login is off",
		}}
	}
	return []Fix{{
		Label: "Allow password login", Op: "ssh.passwords", Args: map[string]string{"enabled": "yes"}, Danger: true,
		Confirm: "Anyone will be able to try passwords over SSH again. Allow it?",
		Done:    "Password login is on",
	}}
}

// RootFixes change how root may log in over SSH.
func RootFixes(f facts.Facts) []Fix {
	if f.SSHDErr != "" {
		return nil
	}
	v := f.SSHD["permitrootlogin"]
	var out []Fix
	if v != "prohibit-password" && v != "without-password" {
		out = append(out, Fix{Label: "Root: keys only", Op: "ssh.rootlogin", Args: map[string]string{"value": "prohibit-password"},
			Done: "Root can only log in with a key"})
	}
	if v != "no" && f.User != "root" {
		out = append(out, Fix{Label: "Turn off root login", Op: "ssh.rootlogin", Args: map[string]string{"value": "no"}, Danger: true,
			Confirm: "Nobody will be able to log in as root over SSH, even with a key. You log in as " + f.User + " and use sudo, so that doesn't affect you.",
			Done:    "Root login is off"})
	}
	return out
}

// SudoFixes switch passwordless sudo for the account running opsie.
func SudoFixes(f facts.Facts) []Fix {
	if f.SudoErr != "" || f.User == "root" {
		return nil
	}
	if f.PasswordlessSudo {
		if !f.Sudo.HasPassword {
			return nil // sudo couldn't ask for a password that doesn't exist
		}
		return []Fix{{
			Label: "Require a password", Op: "sudo.passwordless", Args: map[string]string{"user": f.User, "enabled": "no"}, Danger: true,
			Confirm: "sudo will ask for " + f.User + "'s password from now on. opsie will ask for it once each time it starts. Make sure you know the password.",
			Done:    "sudo asks for a password now",
		}}
	}
	return []Fix{{
		Label: "Allow without a password", Op: "sudo.passwordless", Args: map[string]string{"user": f.User, "enabled": "yes"}, Danger: true,
		Confirm: "Anyone who gets into " + f.User + " will get root without knowing its password. Allow it?",
		Done:    "sudo no longer asks for a password",
	}}
}

// FirewallFixes install, turn on or turn off ufw.
func FirewallFixes(f facts.Facts) []Fix {
	if f.RulesErr != "" {
		return nil
	}
	r := f.Rules
	switch {
	case !r.Installed && f.Firewall.Tool == "nftables":
		return nil // someone runs nftables by hand; leave it be
	case !r.Installed:
		return []Fix{{Label: "Install ufw", Op: "ufw.install", Done: "ufw is installed; turn it on next"}}
	case !r.Active:
		return []Fix{{
			Label: "Turn on firewall", Op: "ufw.enable", Danger: true,
			Confirm: "Everything coming in will be blocked except SSH, which is allowed first so this session stays up, and any ports you've already allowed." + blockedNote(f),
			Done:    "The firewall is on",
		}}
	}
	return []Fix{{
		Label: "Turn off firewall", Op: "ufw.disable", Danger: true,
		Confirm: "Every listening port will be reachable from the internet. Turn the firewall off?",
		Done:    "The firewall is off",
	}}
}

// blockedNote names the public ports that turning ufw on would close.
func blockedNote(f facts.Facts) string {
	var ports []string
	for _, l := range f.Listen {
		if l.Public && !l.Docker && l.Process != "sshd" && !priv.AllowsPort(f.Rules.Rules, l.Port, l.Proto) {
			ports = append(ports, fmt.Sprintf("%d/%s (%s)", l.Port, l.Proto, l.Process))
		}
	}
	if len(ports) == 0 {
		return ""
	}
	return " These would be closed: " + strings.Join(ports, ", ") + "."
}

// Fail2banFixes install or start fail2ban.
func Fail2banFixes(f facts.Facts) []Fix {
	switch {
	case f.Fail2banErr != "":
		return nil
	case !f.Fail2ban.Installed:
		return []Fix{{Label: "Install fail2ban", Op: "fail2ban.install", Done: "fail2ban is watching SSH"}}
	case !f.Fail2ban.Running:
		return []Fix{{Label: "Start fail2ban", Op: "unit.restart", Args: map[string]string{"unit": "fail2ban.service"}, Done: "fail2ban is running"}}
	}
	return nil
}

// BlockFix blocks an address in the firewall.
func BlockFix(f facts.Facts, ip string) []Fix {
	if !f.Rules.Active || ip == f.Session.IP {
		return nil
	}
	return []Fix{{Label: "Block this address", Op: "ufw.deny", Args: map[string]string{"ip": ip, "caller_ip": f.Session.IP},
		Done: "Blocked " + ip}}
}

// AutoFixes turn on automatic security updates.
func AutoFixes(f facts.Facts) []Fix {
	if f.AptErr != "" || f.Apt.AutoOn {
		return nil
	}
	return []Fix{{Label: "Turn on automatic updates", Op: "apt.auto-on", Done: "Security updates will install themselves"}}
}

// CheckFix downloads fresh package lists.
func CheckFix() Fix {
	return Fix{Label: "Check for updates", Op: "apt.update", Done: "Package lists are up to date"}
}

// InstallAllFix installs every pending update.
func InstallAllFix(f facts.Facts) []Fix {
	n := len(f.Apt.Packages)
	if n == 0 {
		return nil
	}
	return []Fix{{
		Label: "Install " + human.Plural(n, "update", "updates"), Op: "apt.upgrade", Danger: true,
		Confirm: "Install " + human.Plural(n, "update", "updates") + " now? Services that get updated restart on their own; this can take a few minutes. Nothing is removed.",
		Done:    "Updates installed",
	}}
}

// RebootFix restarts the server, after a countdown.
// RestartAt is when "Restart tonight" restarts the server.
const RestartAt = "04:00"

// RebootFixes are the ways to restart: now, tonight, or at a chosen time,
// or cancelling a restart that's already set.
func RebootFixes(f facts.Facts) []Fix {
	if !f.Scheduled.At.IsZero() {
		return []Fix{
			{Label: "Cancel the restart", Op: "system.reboot-cancel", Done: "The restart is cancelled"},
			RebootFix(),
		}
	}
	later := "Restart at " + RestartAt
	return []Fix{
		RebootFix(),
		{Label: later, Op: "system.reboot-at", Args: map[string]string{"at": RestartAt}, Danger: true, Done: "The server restarts at " + RestartAt,
			Confirm: "Restart the server at " + RestartAt + "? New logins are refused for the five minutes before, and everyone connected then gets disconnected. You can cancel it until then."},
		{Label: "Restart at another time", Op: "system.reboot-at", Done: "The server restarts at {at}", Danger: true,
			Confirm: "Restart the server at {at}? New logins are refused for the five minutes before, and everyone connected then gets disconnected. You can cancel it until then.",
			Input:   &Input{Prompt: "Time", Arg: "at", Placeholder: "04:00", Kind: "clock"}},
	}
}

func RebootFix() Fix {
	return Fix{
		Label: "Restart server", Op: "system.reboot", Danger: true, Countdown: 10,
		Confirm: "Restart the server now? Every session, including this one, will disconnect.",
		Done:    "Restarting…",
	}
}

// StaleFixes restart a service so it loads its updated libraries.
func StaleFixes(unit string) []Fix {
	switch unit {
	case "systemd":
		return []Fix{{Label: "Reload systemd", Op: "systemd.reexec", Done: "systemd reloaded itself"}}
	case "dbus.service", "dbus-broker.service", "systemd-logind.service":
		return nil // restarting these upsets everything; a reboot is the way
	case "docker.service", "containerd.service":
		return []Fix{{Label: "Restart " + strings.TrimSuffix(unit, ".service"), Op: "unit.restart", Args: map[string]string{"unit": unit}, Danger: true,
			Confirm: "Restarting " + unit + " stops every container for a moment; ones set to restart come back on their own.",
			Done:    "Restarted " + unit}}
	}
	return []Fix{{Label: "Restart " + strings.TrimSuffix(unit, ".service"), Op: "unit.restart", Args: map[string]string{"unit": unit}, Done: "Restarted " + unit}}
}

// --- checks ---

func updates(f facts.Facts, now time.Time) []Result {
	if f.AptErr != "" {
		return nil
	}
	a := f.Apt
	var out []Result
	sec := 0
	for _, p := range a.Packages {
		if p.Security {
			sec++
		}
	}
	tab := Fix{Label: "Open Updates tab", Special: "tab:updates"}
	switch {
	case sec > 0:
		out = append(out, Result{ID: "updates", Status: Warn, Title: "Security updates", Summary: fmt.Sprintf("%d waiting", sec),
			Detail: []string{fmt.Sprintf("%s fix security problems, and %s waiting in total.", human.Plural(sec, "update", "updates"), human.Plural(len(a.Packages), "is", "are")),
				"Install them soon; most take effect right away, a kernel update after a restart."},
			Fixes: append(InstallAllFix(f), tab)})
	case len(a.Packages) > 0:
		out = append(out, Result{ID: "updates", Status: Info, Title: "Updates", Summary: fmt.Sprintf("%d waiting", len(a.Packages)),
			Detail: []string{human.Plural(len(a.Packages), "package has", "packages have") + " a newer version. None of them are security fixes."},
			Fixes:  append(InstallAllFix(f), tab)})
	default:
		out = append(out, Result{ID: "updates", Status: OK, Title: "Updates", Summary: "up to date",
			Detail: []string{"Every installed package is the newest version apt knows about."}, Fixes: []Fix{CheckFix()}})
	}
	if !a.ListsAt.IsZero() && now.Sub(a.ListsAt) > 7*24*time.Hour {
		out = append(out, Result{ID: "updates:lists", Status: Warn, Title: "Package lists", Summary: human.Duration(now.Sub(a.ListsAt)) + " old",
			Detail: []string{"apt hasn't checked for updates in " + human.Duration(now.Sub(a.ListsAt)) + ", so new security fixes won't show up."},
			Fixes:  []Fix{CheckFix()}})
	}
	if !a.AutoOn {
		out = append(out, Result{ID: "updates:auto", Status: Warn, Title: "Automatic updates", Summary: "off",
			Detail: []string{"Security updates only get installed when you do it. unattended-upgrades can install them every day on its own; it only installs security fixes from Debian and never restarts the server."},
			Fixes:  AutoFixes(f)})
	}
	if n := len(f.Stale); n > 0 && f.StaleErr == "" {
		var units []string
		for _, s := range f.Stale {
			units = append(units, strings.TrimSuffix(s.Unit, ".service"))
		}
		out = append(out, Result{ID: "updates:stale", Status: Info, Title: "Services to restart", Summary: strconv.Itoa(n),
			Detail: []string{"These still run code from before an update replaced it, so they don't have its fixes yet: " + strings.Join(units, ", ") + ".",
				"Restart them from the Updates tab, or restart the server."},
			Fixes: []Fix{tab}})
	}
	return out
}

func logins(f facts.Facts) []Result {
	if f.FailuresErr != "" {
		return nil
	}
	n := f.Failures.Total
	tab := Fix{Label: "Open Security tab", Special: "tab:security"}
	r := Result{ID: "logins", Status: OK, Title: "Failed logins", Summary: "none today",
		Detail: []string{"Nobody has failed to log in over SSH in the last day."}}
	if n > 0 {
		r.Status, r.Summary = Info, fmt.Sprintf("%d today", n)
		r.Detail = []string{fmt.Sprintf("%s to log in over SSH failed in the last day, from %s. That's normal for a server on the internet: bots try common usernames all day.",
			human.Plural(n, "attempt", "attempts"), human.Plural(f.Failures.IPs, "address", "addresses"))}
		r.Fixes = []Fix{tab}
		if PasswordsOn(f) && !f.Fail2ban.Running {
			r.Status = Warn
			r.Detail = append(r.Detail, "Password login is on and nothing slows the guessing down. Turn passwords off, or install fail2ban to ban addresses that keep failing.")
			r.Fixes = append(append(PasswordFixes(f), Fail2banFixes(f)...), tab)
		} else {
			r.Detail = append(r.Detail, "Password login is off, so guessing can't get in.")
		}
	}
	return []Result{r}
}

// dockerPorts flags ports Docker publishes to the internet, which ufw
// doesn't filter because Docker writes its own rules.
// PublishedPort is a container port published on the server's network.
type PublishedPort struct {
	Host     string // "*" for every address
	HostPort string
	Port     string // in the container, like "5432/tcp"
	Proto    string
}

// Key names it for the public_ports setting, like "game-server:27015/udp".
func (p PublishedPort) Key(container string) string {
	return container + ":" + p.HostPort + "/" + p.Proto
}

// PublicPorts are the ports a container publishes to the internet: on
// every address, or on a public one. Ports on 127.0.0.1 or a private
// network aren't.
func PublicPorts(c priv.Container) []PublishedPort {
	var out []PublishedPort
	seen := map[string]bool{}
	for _, s := range c.Ports {
		left, port, ok := strings.Cut(s, " → ")
		i := strings.LastIndex(left, ":")
		if !ok || i < 0 {
			continue
		}
		p := PublishedPort{Host: left[:i], HostPort: left[i+1:], Port: port, Proto: "tcp"}
		if _, proto, ok := strings.Cut(port, "/"); ok {
			p.Proto = proto
		}
		if p.Host != "*" {
			ip := net.ParseIP(strings.Trim(p.Host, "[]"))
			if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
				continue
			}
		}
		if k := p.HostPort + "/" + p.Proto; !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	return out
}

// riskyPorts are what databases and admin interfaces listen on: things
// that shouldn't face the internet.
var riskyPorts = map[string]string{
	"5432": "PostgreSQL", "3306": "MySQL", "6379": "Redis", "27017": "MongoDB", "9200": "Elasticsearch",
	"5984": "CouchDB", "2375": "the Docker API", "2376": "the Docker API", "11211": "Memcached", "8086": "InfluxDB",
	"5672": "RabbitMQ", "15672": "RabbitMQ's admin page", "9090": "Prometheus", "1433": "SQL Server",
}

func dockerPorts(f facts.Facts, ok []string) []Result {
	var out []Result
	var meant []string
	for _, c := range f.Containers {
		if c.State != "running" {
			continue
		}
		var open []PublishedPort
		for _, p := range PublicPorts(c) {
			if slices.Contains(ok, p.Key(c.Name)) {
				meant = append(meant, c.Name+" "+p.HostPort+"/"+p.Proto)
			} else {
				open = append(open, p)
			}
		}
		if len(open) == 0 {
			continue
		}
		var names []string
		risky := ""
		for _, p := range open {
			names = append(names, p.HostPort+"/"+p.Proto)
			if what, ok := riskyPorts[strings.TrimSuffix(strings.TrimSuffix(p.Port, "/tcp"), "/udp")]; ok && risky == "" {
				risky = what
			}
		}
		r := Result{ID: "ports:" + c.Name, Status: Warn, Title: c.Name + " is open to the internet", Summary: strings.Join(names, ", ")}
		r.Detail = append(r.Detail, c.Name+" publishes "+strings.Join(names, ", ")+" so anyone on the internet can connect.")
		if f.Rules.Active {
			r.Detail = append(r.Detail, "The firewall doesn't stop it: Docker adds its own rules ahead of ufw's, so ufw's list doesn't apply to container ports.")
		}
		if risky != "" {
			r.Status = Crit
			r.Detail = append(r.Detail, "That's "+risky+", which should only be reached from this server. Exposed databases are found and attacked within hours.")
		}
		r.Detail = append(r.Detail, "If only this server (or the tunnel) needs it, publish it on 127.0.0.1. If it's meant to be public, like a game server, say so and opsie stops warning.")
		for _, p := range open {
			label := "Only this server"
			if len(open) > 1 {
				label += " for " + p.HostPort + "/" + p.Proto
			}
			if c.Project != "" && c.ConfigFiles != "" {
				r.Fixes = append(r.Fixes, Fix{Label: label, Op: "compose.bindlocal",
					Args: map[string]string{"project": c.Project, "service": c.Service, "port": p.HostPort, "proto": p.Proto}, Danger: true,
					Done:    "Port " + p.HostPort + " is only open to this server now",
					Confirm: "Change port " + p.HostPort + " in " + c.Project + "'s compose file to 127.0.0.1, then recreate " + c.Name + "? It's down for a few seconds, and from then on only this server and the tunnel reach it. The old file is kept next to it."})
			}
		}
		for _, p := range open {
			label := "It's meant to be public"
			if len(open) > 1 {
				label += ": " + p.HostPort + "/" + p.Proto
			}
			r.Fixes = append(r.Fixes, Fix{Label: label, Special: "config:add", Args: map[string]string{"key": "docker.public_ports", "value": p.Key(c.Name)},
				Done: "opsie won't warn about " + p.HostPort + "/" + p.Proto + " anymore"})
		}
		if c.WorkingDir != "" {
			r.Fixes = append(r.Fixes, Fix{Label: "Open folder", Special: "folder", Args: map[string]string{"dir": c.WorkingDir}})
		}
		out = append(out, r)
	}
	if len(meant) > 0 {
		out = append(out, Result{ID: "ports:public", Status: Info, Title: "Public on purpose", Summary: strings.Join(meant, ", "),
			Detail: []string{"You marked these container ports as meant to be public: " + strings.Join(meant, ", ") + ". Anyone on the internet can reach them; the firewall doesn't apply to them."},
			Fixes:  []Fix{{Label: "Warn about them again", Special: "config:unset", Args: map[string]string{"key": "docker.public_ports"}}}})
	}
	return out
}

// Container logs bigger than these are worth a warning.
const (
	bigLog      = 1 << 30
	bigLogTotal = 5 << 30
	emptyLogAt  = 100 << 20
)

func dockerLogs(f facts.Facts) []Result {
	if f.NoDocker || len(f.Containers) == 0 {
		return nil
	}
	var total uint64
	var unlimited, big []priv.Container
	for _, c := range f.Containers {
		total += c.LogSize
		if c.LogUnlimited() {
			unlimited = append(unlimited, c)
		}
		if c.LogSize >= emptyLogAt {
			big = append(big, c)
		}
	}
	sort.Slice(big, func(i, j int) bool { return big[i].LogSize > big[j].LogSize })
	limited := f.HaveLogDefault && f.LogDefault.Limited
	if len(unlimited) == 0 && total < bigLogTotal && (len(big) == 0 || big[0].LogSize < bigLog) {
		return nil
	}
	r := Result{ID: "docker-logs", Status: Info, Title: "Container logs", Summary: human.Bytes(total)}
	if len(big) > 0 && big[0].LogSize >= bigLog || total >= bigLogTotal {
		r.Status = Warn
		r.Summary = human.Bytes(total) + " and growing"
	} else if len(unlimited) > 0 {
		r.Summary = "no size limit"
	}
	r.Detail = append(r.Detail, "Docker keeps everything containers print in log files, "+human.Bytes(total)+" so far.")
	if len(unlimited) > 0 {
		var names []string
		for _, c := range unlimited {
			names = append(names, c.Name)
		}
		what := human.Plural(len(unlimited), "container has", "containers have") + " no limit on its log (" + strings.Join(names, ", ") + "), so it grows until the disk is full."
		if len(unlimited) > 1 {
			what = human.Plural(len(unlimited), "container has", "containers have") + " no limit on their logs (" + strings.Join(names, ", ") + "), so they grow until the disk is full."
		}
		r.Detail = append(r.Detail, what+" Docker's default is to never trim them.")
	}
	// Docker can't change an existing container's log settings, so the
	// limit takes three steps: save it, restart Docker, recreate the
	// containers. Each step is offered once the one before is done.
	switch {
	case !limited && f.HaveLogDefault:
		r.Detail = append(r.Detail, "Limiting logs makes Docker keep at most 30 MB per container, the newest lines. Docker can't change the containers there now, so it takes three steps: save the limit, restart Docker, then recreate each app's containers. This offers each step in turn.")
		r.Fixes = append(r.Fixes, Fix{Label: "Limit logs", Op: "docker.log-limit",
			Done: "The limit is saved; Docker uses it once it restarts"})
	case limited && f.LogDefault.Pending:
		r.Detail = append(r.Detail, "The limit is saved, but Docker reads it only when it starts. Restarting Docker stops every container for a moment; ones set to restart come back on their own. After that, recreating each app gives its containers the limit.")
		r.Fixes = append(r.Fixes, Fix{Label: "Restart Docker", Op: "unit.restart", Args: map[string]string{"unit": "docker.service"}, Danger: true,
			Confirm: "Restart Docker now? Every container stops for a moment; ones set to restart come back on their own.",
			Done:    "Docker restarted with the log limit"})
	case limited && len(unlimited) > 0:
		r.Detail = append(r.Detail, "Docker gives new containers the limit. The ones above were made before it and keep growing until they're recreated, which makes new containers from the same images and keeps their volumes.")
		seen := map[string]bool{}
		var loose []string
		for _, c := range unlimited {
			if c.Project == "" || c.ConfigFiles == "" {
				loose = append(loose, c.Name)
				continue
			}
			if seen[c.Project] {
				continue
			}
			seen[c.Project] = true
			r.Fixes = append(r.Fixes, Fix{Label: "Recreate " + c.Project, Op: "compose.update", Args: map[string]string{"project": c.Project, "mode": "recreate"}, Danger: true,
				Confirm: "Recreate " + c.Project + "'s containers? It's down for a few seconds. Its volumes and files stay as they are.",
				Done:    c.Project + "'s logs are limited now"})
		}
		if len(loose) > 0 {
			r.Detail = append(r.Detail, strings.Join(loose, ", ")+" weren't started with docker compose, so opsie can't recreate them; make them again the way you first did.")
		}
	}
	for _, c := range big {
		r.Fixes = append(r.Fixes, Fix{Label: "Empty " + c.Name + "'s log (" + human.Bytes(c.LogSize) + ")", Op: "docker.log-truncate",
			Args: map[string]string{"container": c.Name}, Danger: true, Done: "Emptied " + c.Name + "'s log",
			Confirm: "Delete everything in " + c.Name + "'s log? `docker logs` and the log viewer start from now on. It keeps running."})
	}
	r.Fixes = append(r.Fixes, Fix{Label: "Docker service", Special: "open:services:unit:docker.service"})
	return []Result{r}
}
