// Package health turns readings into a checklist: what's fine, what needs
// a look, and what's broken, each with the actions that can fix it.
package health

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/facts"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/metrics"
	"github.com/aloglu/opsie/internal/priv"
)

// Status is how a check came out. Higher is worse.
type Status int

const (
	OK Status = iota
	Info
	Unknown
	Warn
	Crit
)

// Result is one line of the checklist.
type Result struct {
	// ID stays the same across runs so the selection can follow a result.
	ID      string
	Status  Status
	Title   string
	Summary string
	// Detail explains the result in a few sentences.
	Detail []string
	Fixes  []Fix
}

// Fix is an action that addresses a result.
type Fix struct {
	Label string
	// Op and Args are sent to the admin helper. Special fixes ("unlock",
	// "update") are handled by the dashboard itself instead.
	Op      string
	Args    map[string]string
	Special string
	// Danger fixes ask for confirmation first, and Countdown more seconds
	// of grace after that.
	Danger    bool
	Confirm   string
	Countdown int
	// Done is the message shown when the fix worked.
	Done string
	// Input, when set, asks for a value first, which goes into Args.
	Input *Input
}

// Input is a value a fix asks for before running.
type Input struct {
	Prompt      string
	Arg         string
	Placeholder string
	// Kind checks and describes the value as it's typed: "key", "port",
	// "ip", "url", "hours", "chat", "text", or "number:MIN:MAX".
	Kind string
	// Value is what the field starts with.
	Value string
	// Secret hides what's typed.
	Secret bool
	// The fix's Confirm and Done may say {Arg}, which becomes the value.
}

// Readings is everything the checks look at.
type Readings struct {
	Snap       metrics.Snapshot
	HaveSnap   bool
	Facts      facts.Facts
	HaveFacts  bool
	Thresholds config.Thresholds
	// Update is a newer opsie version, if one is out.
	Update string
	// Watcher is the background service, when the dashboard asks; the
	// service itself leaves it out.
	Watcher     Watcher
	HaveWatcher bool
	// Sites are the site check settings, for what counts as slow.
	Sites config.Sites
	// PublicPorts are published container ports meant to be public.
	PublicPorts []string
	// Nightly is the nightly backup setup, when it's known.
	Nightly     Nightly
	HaveNightly bool
}

// Evaluate runs every check, worst results first.
func Evaluate(in Readings) []Result {
	var out []Result
	add := func(r ...Result) { out = append(out, r...) }

	if in.HaveFacts {
		add(admin(in.Facts)...)
	}
	if in.HaveSnap {
		add(load(in.Snap, in.Thresholds), memory(in.Snap, in.Thresholds))
		add(swap(in.Snap, in.Thresholds)...)
		add(disks(in.Snap, in.Thresholds)...)
	}
	if in.HaveFacts {
		f := in.Facts
		add(services(f)...)
		add(containers(f)...)
		add(reboot(f))
		add(sshLogin(f)...)
		add(sshKeys(f))
		add(firewall(f))
		add(clock(f))
		add(sudo(f))
		add(updates(f, f.At)...)
		add(logins(f)...)
		add(dockerPorts(f, in.PublicPorts)...)
		add(dockerLogs(f)...)
		add(tunnels(f)...)
		add(sitesCheck(f, in.Sites)...)
		add(images(f)...)
	}
	if in.HaveNightly {
		now := time.Now()
		if in.HaveFacts {
			now = in.Facts.At
		}
		add(nightlyBackups(in.Nightly, now)...)
	}
	if in.HaveWatcher && in.HaveFacts {
		add(watcher(in.Watcher, in.Facts.At))
	}
	if in.Update != "" {
		add(Result{
			ID: "update", Status: Info, Title: "opsie " + in.Update + " is out",
			Summary: "update",
			Detail:  []string{"A new version of opsie is available. Updating shows what changed, then restarts opsie."},
			Fixes:   []Fix{{Label: "Update opsie", Special: "update"}},
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Status > out[j].Status })
	return out
}

// Issues counts the results that need attention.
func Issues(results []Result) (crit, warn int) {
	for _, r := range results {
		switch r.Status {
		case Crit:
			crit++
		case Warn:
			warn++
		}
	}
	return crit, warn
}

func admin(f facts.Facts) []Result {
	if f.Admin.Available {
		return nil
	}
	r := Result{ID: "admin", Status: Warn, Title: "Admin access", Summary: "locked"}
	if f.Admin.NeedsPassword {
		r.Detail = []string{
			"Some checks and every action need root. opsie gets it through sudo, and sudo wants your password.",
			"Unlocking asks for it once; sudo remembers it for a few minutes, and opsie keeps its admin helper running until you quit.",
		}
		r.Fixes = []Fix{{Label: "Unlock", Special: "unlock"}}
	} else {
		r.Summary = "unavailable"
		r.Detail = []string{"opsie couldn't start its admin helper: " + f.Admin.Err, "Until it runs, checks that need root are skipped and actions are off."}
		r.Fixes = []Fix{{Label: "Try again", Special: "unlock"}}
	}
	return []Result{r}
}

func load(s metrics.Snapshot, t config.Thresholds) Result {
	cores := max(s.Cores, 1)
	perCore := s.Load[0] / float64(cores)
	r := Result{
		ID: "load", Status: OK, Title: "CPU load",
		Summary: fmt.Sprintf("%.2f on %s", s.Load[0], human.Plural(cores, "core", "cores")),
		Detail: []string{
			fmt.Sprintf("Load averages over 1, 5 and 15 minutes: %.2f, %.2f, %.2f.", s.Load[0], s.Load[1], s.Load[2]),
			fmt.Sprintf("That's %.2f per core. Above %.1f per core, work starts waiting for the CPU.", perCore, t.LoadWarn),
		},
	}
	switch {
	case perCore >= t.LoadCrit:
		r.Status = Crit
	case perCore >= t.LoadWarn:
		r.Status = Warn
	}
	return r
}

func memory(s metrics.Snapshot, t config.Thresholds) Result {
	r := Result{
		ID: "memory", Status: OK, Title: "Memory",
		Summary: fmt.Sprintf("%.0f%% used", s.Mem.Percent),
		Detail: []string{fmt.Sprintf("%s of %s in use, %s available. Caches the kernel can free aren't counted as used.",
			human.Bytes(s.Mem.Used), human.Bytes(s.Mem.Total), human.Bytes(s.Mem.Available))},
	}
	switch {
	case s.Mem.Percent >= t.MemoryCrit:
		r.Status = Crit
		r.Detail = append(r.Detail, "The kernel will soon start killing processes to free memory.")
	case s.Mem.Percent >= t.MemoryWarn:
		r.Status = Warn
	}
	return r
}

func swap(s metrics.Snapshot, t config.Thresholds) []Result {
	if s.Swap.Total == 0 || s.Swap.Percent < t.SwapWarn {
		return nil
	}
	return []Result{{
		ID: "swap", Status: Warn, Title: "Swap",
		Summary: fmt.Sprintf("%.0f%% used", s.Swap.Percent),
		Detail: []string{fmt.Sprintf("%s of %s swap in use. Heavy swapping makes everything slow; something may be using more memory than the server has.",
			human.Bytes(s.Swap.Used), human.Bytes(s.Swap.Total))},
	}}
}

func disks(s metrics.Snapshot, t config.Thresholds) []Result {
	var out []Result
	for _, d := range s.Disks {
		r := Result{
			ID: "disk:" + d.Mount, Status: OK, Title: "Disk " + d.Mount,
			Summary: fmt.Sprintf("%.0f%% · %s free", d.Percent, human.Bytes(d.Free)),
			Detail: []string{fmt.Sprintf("%s of %s used on %s (%s).",
				human.Bytes(d.Used), human.Bytes(d.Total), d.Device, d.FSType)},
			Fixes: []Fix{{Label: "What's using space", Special: "explore:" + d.Mount}, {Label: "Free up space", Special: "clean"}},
		}
		switch {
		case d.Percent >= t.DiskCrit:
			r.Status = Crit
			r.Detail = append(r.Detail, "When a disk fills up, databases, logs and containers start failing.")
		case d.Percent >= t.DiskWarn:
			r.Status = Warn
		}
		if d.InodePercent >= 90 && r.Status < Warn {
			r.Status = Warn
			r.Summary = fmt.Sprintf("%.0f%% of inodes", d.InodePercent)
			r.Detail = append(r.Detail, "Almost every inode is used, so no new files can be made even though there's space. Usually that's millions of tiny files somewhere.")
		}
		out = append(out, r)
	}
	return out
}

func services(f facts.Facts) []Result {
	if f.FailedUnitsErr != "" {
		return []Result{{ID: "units", Status: Unknown, Title: "Services", Summary: "unknown", Detail: []string{f.FailedUnitsErr}}}
	}
	if len(f.FailedUnits) == 0 {
		return []Result{{ID: "units", Status: OK, Title: "Services", Summary: "none failed",
			Detail: []string{"No systemd unit is in the failed state."}}}
	}
	var out []Result
	for _, unit := range f.FailedUnits {
		out = append(out, Result{
			ID: "unit:" + unit, Status: Crit, Title: unit, Summary: "failed",
			Detail: []string{
				"This systemd unit stopped with an error.",
				"Restarting tries it again. Clearing forgets the failure without starting it, for units you don't need.",
			},
			Fixes: []Fix{
				{Label: "Restart", Op: "unit.restart", Args: map[string]string{"unit": unit}, Done: "Restarted " + unit},
				{Label: "Logs", Special: "logs", Args: map[string]string{"unit": unit}},
				{Label: "Clear failure", Op: "unit.reset-failed", Args: map[string]string{"unit": unit}, Done: "Cleared " + unit},
			},
		})
	}
	return out
}

// ContainerStatus describes a container's state in a few words, like
// "up 3d", "exited (1) 2h ago" or "restarting".
func ContainerStatus(c priv.Container, now time.Time) string {
	switch c.State {
	case "running":
		if c.Started.IsZero() {
			return "up"
		}
		return "up " + human.Duration(now.Sub(c.Started))
	case "exited":
		s := fmt.Sprintf("exited (%d)", c.Exit)
		if !c.Finished.IsZero() && c.Finished.Year() > 1 {
			s += " " + human.Duration(now.Sub(c.Finished)) + " ago"
		}
		return s
	}
	return c.State
}

// restartWarn is how many automatic restarts mark a container as flaky.
const restartWarn = 3

func containers(f facts.Facts) []Result {
	if f.NoDocker {
		return nil
	}
	if f.DockerErr != "" {
		return []Result{{ID: "docker", Status: Unknown, Title: "Containers", Summary: f.DockerErr, Detail: []string{"Docker couldn't be checked: " + f.DockerErr + "."}}}
	}
	now := time.Now()
	var out []Result
	running := 0
	for _, c := range f.Containers {
		restart := Fix{Label: "Restart", Op: "docker.restart", Args: map[string]string{"container": c.Name}, Done: "Restarted " + c.Name}
		logs := Fix{Label: "Logs", Special: "logs", Args: map[string]string{"container": c.Name}}
		about := []string{"Image: " + c.Image + ". Status: " + ContainerStatus(c, now) + "."}
		if c.State == "running" {
			running++
		}
		switch {
		case c.State == "running" && c.Health == "unhealthy":
			out = append(out, Result{ID: "ctr:" + c.Name, Status: Crit, Title: c.Name, Summary: "unhealthy",
				Detail: append([]string{"The container is running but failing its health check."}, about...),
				Fixes:  []Fix{logs, restart}})
		case c.State == "restarting":
			out = append(out, Result{ID: "ctr:" + c.Name, Status: Crit, Title: c.Name, Summary: "restart loop",
				Detail: append([]string{fmt.Sprintf("The container keeps crashing and Docker keeps restarting it (%d times so far). Its logs usually say why.", c.Restarts)}, about...),
				Fixes:  []Fix{logs, restart}})
		case c.State == "exited" && c.Exit != 0:
			why := fmt.Sprintf("The container stopped with exit code %d, which usually means an error.", c.Exit)
			if c.OOM {
				why = "The container was killed for using too much memory."
			}
			out = append(out, Result{ID: "ctr:" + c.Name, Status: Warn, Title: c.Name, Summary: fmt.Sprintf("exited (%d)", c.Exit),
				Detail: append([]string{why}, about...),
				Fixes:  []Fix{logs, {Label: "Start", Op: "docker.start", Args: map[string]string{"container": c.Name}, Done: "Started " + c.Name}}})
		case c.State == "dead":
			out = append(out, Result{ID: "ctr:" + c.Name, Status: Crit, Title: c.Name, Summary: "dead",
				Detail: append([]string{"Docker couldn't stop or remove this container cleanly."}, about...)})
		case c.State == "running" && c.Restarts >= restartWarn:
			out = append(out, Result{ID: "ctr:" + c.Name, Status: Warn, Title: c.Name, Summary: fmt.Sprintf("restarted %d times", c.Restarts),
				Detail: append([]string{fmt.Sprintf("Docker has restarted this container %d times since it was created, so it's crashing now and then even though it's up at the moment. The count resets when you restart it yourself.", c.Restarts)}, about...),
				Fixes:  []Fix{logs, restart}})
		}
	}
	summary := human.Plural(running, "running", "running")
	if stopped := len(f.Containers) - running; stopped > 0 {
		summary += fmt.Sprintf(" · %d stopped", stopped)
	}
	status := OK
	if len(out) > 0 {
		status = Info
	}
	out = append(out, Result{ID: "docker", Status: status, Title: "Containers", Summary: summary,
		Detail: []string{fmt.Sprintf("%s in total. Containers that exited cleanly, like one-off setup jobs, aren't problems. The Docker tab has the details.", human.Plural(len(f.Containers), "container", "containers"))},
		Fixes:  []Fix{{Label: "Open Docker tab", Special: "tab:docker"}}})
	return out
}

func reboot(f facts.Facts) Result {
	if at := f.Scheduled.At; !at.IsZero() && f.Scheduled.Mode != "poweroff" {
		r := Result{ID: "reboot", Status: Info, Title: "Restart", Summary: "set for " + at.Local().Format("15:04"),
			Detail: []string{"The server restarts at " + at.Local().Format("15:04 on Mon 2 Jan") + ". New logins are refused for the five minutes before."},
			Fixes:  RebootFixes(f)}
		if at.Sub(f.At) > 24*time.Hour {
			r.Summary = "set for " + at.Local().Format("Mon 15:04")
		}
		return r
	}
	if !f.RebootRequired {
		return Result{ID: "reboot", Status: OK, Title: "Restart", Summary: "not needed",
			Detail: []string{"No installed update is waiting for a restart."}}
	}
	detail := []string{"Updates were installed that only take effect after a restart, usually a new kernel or system library."}
	if len(f.RebootPackages) > 0 {
		detail = append(detail, "Waiting: "+strings.Join(f.RebootPackages, ", ")+".")
	}
	detail = append(detail, "Restarting disconnects everyone, including this session. Containers set to restart come back on their own.")
	summary := "needed"
	if n := len(f.RebootPackages); n > 0 {
		summary = "needed · " + human.Plural(n, "package", "packages")
	}
	return Result{
		ID: "reboot", Status: Warn, Title: "Restart", Summary: summary, Detail: detail,
		Fixes: RebootFixes(f),
	}
}

func sshLogin(f facts.Facts) []Result {
	if f.SSHDErr != "" {
		return []Result{{ID: "ssh", Status: Unknown, Title: "SSH login", Summary: f.SSHDErr, Detail: []string{"SSH settings couldn't be read: " + f.SSHDErr + "."}}}
	}
	cfg := f.SSHD
	passwords := cfg["passwordauthentication"] == "yes" || cfg["kbdinteractiveauthentication"] == "yes"
	rootLogin := cfg["permitrootlogin"]
	detail := []string{
		fmt.Sprintf("Port %s. Password login: %s. Root login: %s.", orDash(cfg["port"]), yesNo(passwords), describeRootLogin(rootLogin)),
	}
	var out []Result
	if passwords {
		why := "Anyone can try guessing passwords over SSH. With a key set up, there's no need for them."
		if MyKeys(f) == 0 {
			why += " Add an SSH key for " + f.User + " first (on the Security tab), then passwords can be turned off without locking you out."
		}
		out = append(out, Result{ID: "ssh:passwords", Status: Warn, Title: "SSH allows passwords", Summary: "on",
			Detail: append([]string{why}, detail...), Fixes: PasswordFixes(f)})
	}
	if rootLogin == "yes" {
		out = append(out, Result{ID: "ssh:root", Status: Warn, Title: "SSH root password login", Summary: "allowed",
			Detail: append([]string{"Root can log in with a password. Allowing root only with a key, or not at all, is safer."}, detail...), Fixes: RootFixes(f)})
	}
	if len(out) == 0 {
		out = append(out, Result{ID: "ssh", Status: OK, Title: "SSH login", Summary: "keys only", Detail: detail})
	}
	return out
}

func describeRootLogin(v string) string {
	switch v {
	case "yes":
		return "allowed with a password"
	case "without-password", "prohibit-password":
		return "keys only"
	case "forced-commands-only":
		return "forced commands only"
	case "no":
		return "off"
	case "":
		return "—"
	}
	return v
}

func sshKeys(f facts.Facts) Result {
	if f.KeysErr != "" {
		return Result{ID: "keys", Status: Unknown, Title: "SSH keys", Summary: f.KeysErr, Detail: []string{"Authorized keys couldn't be counted: " + f.KeysErr + "."}}
	}
	total := 0
	var parts []string
	for _, k := range f.Keys {
		if len(k.Keys) > 0 {
			total += len(k.Keys)
			parts = append(parts, fmt.Sprintf("%s %d", k.User, len(k.Keys)))
		}
	}
	r := Result{ID: "keys", Status: Info, Title: "SSH keys", Summary: human.Plural(total, "key", "keys"),
		Detail: []string{"Keys in authorized_keys files, which can log in without a password."}}
	if len(parts) > 0 {
		r.Summary += " · " + strings.Join(parts, ", ")
	}
	return r
}

func firewall(f facts.Facts) Result {
	fw := f.Firewall
	switch {
	case f.FirewallErr != "":
		return Result{ID: "firewall", Status: Unknown, Title: "Firewall", Summary: f.FirewallErr, Detail: []string{"The firewall couldn't be checked: " + f.FirewallErr + "."}}
	case fw.Tool == "":
		return Result{ID: "firewall", Status: Warn, Title: "Firewall", Summary: "none",
			Detail: []string{"Neither ufw nor nftables is installed, so every listening port is reachable from the internet unless your provider filters it."}, Fixes: FirewallFixes(f)}
	case !fw.Active:
		return Result{ID: "firewall", Status: Warn, Title: "Firewall", Summary: fw.Tool + " is off",
			Detail: []string{fw.Tool + " is installed but not active, so every listening port is reachable from the internet."}, Fixes: FirewallFixes(f)}
	}
	detail := []string{fmt.Sprintf("%s is active with %s.", fw.Tool, human.Plural(fw.Rules, "rule", "rules"))}
	if fw.Default != "" {
		detail = append(detail, "Default: "+fw.Default+".")
	}
	return Result{ID: "firewall", Status: OK, Title: "Firewall", Summary: fmt.Sprintf("%s · %s", fw.Tool, human.Plural(fw.Rules, "rule", "rules")), Detail: detail}
}

func clock(f facts.Facts) Result {
	t := f.Time
	r := Result{ID: "clock", Status: OK, Title: "Clock", Summary: "synced · " + t.Timezone}
	switch {
	case !t.Known:
		r.Status, r.Summary = Unknown, "unknown"
		r.Detail = []string{"timedatectl isn't available, so time sync couldn't be checked."}
	case !t.NTP:
		r.Status, r.Summary = Warn, "sync off"
		r.Detail = []string{"Network time sync is off, so the clock will drift. TLS, logs and scheduled jobs all depend on the right time."}
		r.Fixes = []Fix{{Label: "Turn on time sync", Op: "time.ntp-on", Done: "Time sync is on"}}
	case !t.Synced:
		r.Status, r.Summary = Warn, "not synced"
		r.Detail = []string{"Time sync is on but hasn't reached a time server yet. If this lasts, check that outgoing NTP (UDP 123) isn't blocked."}
	default:
		r.Detail = []string{"The clock is synchronized with network time. Timezone: " + t.Timezone + "."}
	}
	return r
}

func sudo(f facts.Facts) Result {
	if f.PasswordlessSudo {
		return Result{ID: "sudo", Status: Info, Title: "Passwordless sudo", Summary: "on",
			Detail: []string{
				"Your account can run anything as root without a password. That's convenient, but anyone who gets into this account gets root too.",
				"opsie works either way: without it, it asks for your password once when you open it.",
			}, Fixes: SudoFixes(f)}
	}
	return Result{ID: "sudo", Status: OK, Title: "Passwordless sudo", Summary: "off",
		Detail: []string{"sudo asks for your password, so a stolen SSH key alone doesn't give root."}, Fixes: SudoFixes(f)}
}

func yesNo(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
