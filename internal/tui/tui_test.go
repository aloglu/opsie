package tui

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aloglu/opsie/internal/agents"
	"github.com/aloglu/opsie/internal/app"
	"github.com/aloglu/opsie/internal/backup"
	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/cron"
	"github.com/aloglu/opsie/internal/daemon"
	"github.com/aloglu/opsie/internal/facts"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/history"
	"github.com/aloglu/opsie/internal/metrics"
	"github.com/aloglu/opsie/internal/priv"
	"github.com/aloglu/opsie/internal/registry"
	"github.com/aloglu/opsie/internal/sites"
	"github.com/aloglu/opsie/internal/speedtest"
	"github.com/aloglu/opsie/internal/systemd"
	"github.com/aloglu/opsie/internal/watch"
)

func testModel(t *testing.T) *Model {
	t.Helper()
	env := &app.Env{Version: "dev", Dev: true, Config: config.Default(), Paths: app.Paths{State: t.TempDir(), Config: t.TempDir(), Backups: "/home/debian/opsie-backups-dev"}}
	m := &Model{env: env, th: newTheme(), now: time.Date(2026, 11, 10, 13, 20, 5, 0, time.UTC)}
	snap := metrics.Snapshot{
		CPU: 37, Cores: 2, Load: [3]float64{0.42, 0.38, 0.3},
		Mem:  metrics.Memory{Total: 4 << 30, Used: 3 << 30, Available: 1 << 30, Percent: 75},
		Swap: metrics.Memory{Total: 1 << 30, Used: 700 << 20, Percent: 68},
		Disks: []metrics.Disk{
			{Mount: "/", Device: "/dev/sda1", FSType: "ext4", Total: 40 << 30, Used: 37 << 30, Free: 3 << 30, Percent: 92},
			{Mount: "/var/lib/a-rather-long-mount-name", Device: "/dev/sdb1", Total: 100 << 30, Used: 10 << 30, Percent: 10},
		},
		Net:  metrics.Net{Ready: true, RxRate: 1.5e6, TxRate: 3e4, RxTotal: 12 << 30, TxTotal: 3 << 30, Interfaces: []string{"ens3"}},
		Host: metrics.Host{Hostname: "a-very-long-hostname.example.com", OS: "Debian GNU/Linux 13 (trixie)", Kernel: "6.12.115+deb13.1-cloud-amd64", Uptime: 147 * time.Hour, Boot: time.Now().Add(-147 * time.Hour)},
		Sessions: []metrics.Session{{ID: "60", User: "debian", Terminal: "pts/0", From: "203.0.113.50", Started: time.Now()},
			{ID: "12", User: "debian", Terminal: "pts/1", From: "198.51.100.24", Started: time.Now().Add(-6 * 24 * time.Hour)}},
		Procs: 213,
	}
	for i := 0; i < 200; i++ {
		m.cpu.push(float64(i % 100))
		m.mem.push(60)
		m.rx.push(float64(i * 1000))
		m.tx.push(500)
	}
	m.snap, m.haveSnap = snap, true
	m.facts = facts.Facts{
		RebootRequired: true, RebootPackages: []string{"linux-image-6.12.116+deb13-cloud-amd64"},
		FailedUnits:      []string{"a-service-with-a-really-long-name-for-testing.service"},
		Time:             facts.TimeSync{Known: true, NTP: true, Synced: false, Timezone: "Europe/Istanbul"},
		PasswordlessSudo: true,
		Admin:            facts.Admin{Available: true},
		SSHD:             map[string]string{"passwordauthentication": "yes", "permitrootlogin": "yes", "port": "22"},
		Firewall:         priv.Firewall{Tool: "ufw", Active: false},
		Containers: []priv.Container{
			{Name: "web", State: "running", Health: "unhealthy", Image: "nginx:1.27", Project: "site", Service: "web", Started: time.Now().Add(-2 * time.Hour),
				WorkingDir: "/home/debian/apps/site", ConfigFiles: "/home/debian/apps/site/compose.yaml", Ports: []string{"127.0.0.1:8080 → 80/tcp"}, Policy: "unless-stopped"},
			{Name: "site-database-with-a-long-name", State: "running", Health: "healthy", Image: "postgres:17", Project: "site", Service: "db", Started: time.Now().Add(-50 * time.Hour)},
			{Name: "worker", State: "exited", Exit: 1, Image: "app", Finished: time.Now().Add(-3 * time.Minute)},
			{Name: "init", State: "exited", Exit: 0, Image: "busybox", Project: "site", Service: "init"},
		},
		Keys: []priv.KeySet{
			{User: "debian", File: "/home/debian/.ssh/authorized_keys", Keys: []priv.AuthorizedKey{
				{Type: "ssh-ed25519", Comment: "phone-termius-with-a-long-comment", Fingerprint: "SHA256:24zRnbKRJ19wQ5mHB7jiO1axVccfHWsToUojIeLv2j0"},
				{Type: "ssh-rsa", Fingerprint: "SHA256:AAAAbbbbCCCCddddEEEEffffGGGGhhhhIIIIjjjjKKK"},
			}},
			{User: "root", File: "/root/.ssh/authorized_keys", Keys: []priv.AuthorizedKey{}},
		},
		SessionKey: "SHA256:24zRnbKRJ19wQ5mHB7jiO1axVccfHWsToUojIeLv2j0",
		User:       "debian",
		Session:    facts.Session{ID: "60", IP: "203.0.113.50", Port: "50532"},
		Rules: priv.RuleSet{Installed: true, Active: true, Default: "deny (incoming), allow (outgoing), deny (routed)", Rules: []priv.Rule{
			{Num: 1, To: "22/tcp", Action: "ALLOW IN", From: "Anywhere"},
			{Num: 2, To: "Anywhere", Action: "DENY IN", From: "198.51.100.0/24"},
			{Num: 3, To: "22/tcp (v6)", Action: "ALLOW IN", From: "Anywhere (v6)", V6: true},
		}},
		Listen: []priv.Listener{
			{Proto: "tcp", Port: 22, Addrs: []string{"0.0.0.0", "::"}, Process: "sshd", Public: true},
			{Proto: "udp", Port: 27015, Addrs: []string{"203.0.113.50"}, Process: "docker-proxy", Public: true, Docker: true},
			{Proto: "tcp", Port: 5355, Addrs: []string{"0.0.0.0"}, Process: "systemd-resolve", Public: true},
			{Proto: "tcp", Port: 8040, Addrs: []string{"127.0.0.1"}, Process: "docker-proxy", Docker: true},
		},
		Failures: priv.Failures{Total: 786, IPs: 120, Users: []string{"root", "admin", "ubuntu"}, Top: []priv.IPCount{
			{IP: "192.0.2.168", Count: 231, Last: time.Now().Add(-3 * time.Hour)},
			{IP: "2001:db8:aaaa:bbbb:cccc:dddd:eeee:ffff", Count: 12, Last: time.Now().Add(-time.Minute)},
		}},
		Sudo: priv.SudoState{Sources: []string{"/etc/sudoers.d/90-debian-nopasswd"}, HasPassword: true},
		Apt: priv.AptState{AutoInstalled: true, AutoOn: false, ListsAt: time.Now().Add(-10 * 24 * time.Hour), Free: 20 << 30, Packages: []priv.Package{
			{Name: "libssl3t64", From: "3.5.4-1~deb13u1", To: "3.5.4-1~deb13u2", Suite: "stable-security", Security: true},
			{Name: "linux-image-6.12.117+deb13-cloud-amd64-with-a-long-name", From: "", To: "6.12.117-1", Suite: "stable"},
		}},
		Stale: []priv.Stale{{Unit: "ssh.service", Libs: []string{"/usr/lib/x86_64-linux-gnu/libz.so.1.3.1"}, PIDs: []int{734}},
			{Unit: "systemd", Libs: []string{"/usr/lib/x86_64-linux-gnu/libz.so.1.3.1"}, PIDs: []int{1}}},
	}
	m.haveFacts = true
	m.admin = nil
	m.dock.stats = map[string]priv.Stats{
		"web":                            {Name: "web", CPU: 12.5, MemUsed: 120 << 20, MemLimit: 4 << 30, MemPct: 3, NetIO: "1MB / 2MB", BlockIO: "0B / 0B", PIDs: 4},
		"site-database-with-a-long-name": {Name: "site-database-with-a-long-name", CPU: 0.4, MemUsed: 300 << 20},
	}
	m.dock.cpu = map[string]*series{"web": {1, 5, 20, 12}}
	m.dock.df = []priv.DiskUsage{
		{Type: "Images", Total: 17, Active: 8, Size: "7.2GB", Reclaimable: "3.2GB (44%)"},
		{Type: "Build Cache", Total: 29, Size: "5.2GB", Reclaimable: "2.5GB"},
	}
	m.dock.preflight = map[string]preflightState{"site": {p: priv.Preflight{Free: 20 << 30, Images: 2 << 30, MemAvailable: 2 << 30}}}
	now := time.Now()
	m.svc = svcState{loaded: true, units: []systemd.Unit{
		{Name: "a-service-with-a-really-long-name-for-testing.service", Load: "loaded", Active: "failed", Sub: "failed", Result: "exit-code", FileState: "enabled", Since: now.Add(-time.Hour)},
		{Name: "ssh.service", Description: "OpenBSD Secure Shell server", Load: "loaded", Active: "active", Sub: "running", FileState: "enabled", MainPID: 734, Memory: 9 << 20, HaveMemory: true, CPU: 8 * time.Minute, Tasks: 1, Since: now.Add(-6 * 24 * time.Hour), CanReload: true, CanStop: true, Fragment: "/usr/lib/systemd/system/ssh.service"},
		{Name: "docker.service", Load: "loaded", Active: "active", Sub: "running", FileState: "enabled", Memory: 80 << 20, HaveMemory: true, Restarts: 4, CanStop: true, Since: now.Add(-time.Hour)},
		{Name: "cron.service", Load: "loaded", Active: "inactive", Sub: "dead", FileState: "enabled", CanStop: true},
		{Name: "apt-daily.service", Load: "loaded", Active: "inactive", Sub: "dead", FileState: "static", TriggeredBy: "apt-daily.timer"},
	}, timers: []systemd.Timer{
		{Name: "apt-daily.timer", Activates: "apt-daily.service", Next: now.Add(3 * time.Hour), Last: now.Add(-9 * time.Hour)},
		{Name: "never.timer", Activates: "never.service"},
	}}
	m.cron = cronState{at: now, tab: priv.CronTable{Installed: true,
		Jobs: []cron.Job{
			{Schedule: "30 3 * * 0", User: "root", Command: "test -e /run/systemd/system || SERVICE_MODE=1 /usr/libexec/e2fsprogs/e2scrub_all_cron", Source: "/etc/cron.d/e2scrub_all", Line: 1},
			{Schedule: "*/15 * * * *", User: "debian", Command: "$HOME/bin/a-backup-script-with-a-long-name.sh --quiet", Source: "/var/spool/cron/crontabs/debian", Line: 3},
		},
		Periodic: []priv.CronFolder{{Every: "daily", Path: "/etc/cron.daily", Scripts: []string{"apt-compat", "dpkg", "man-db"}}},
	}}
	m.procs = procState{loaded: true, list: []metrics.Proc{
		{PID: 1, Name: "systemd", Cmd: "/sbin/init", User: "root", State: "S", RSS: 12 << 20, Started: now.Add(-147 * time.Hour)},
		{PID: 2, Name: "kthreadd", Kernel: true, User: "root"},
		{PID: 734, PPID: 1, Name: "sshd", Cmd: "sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups", User: "root", State: "S", RSS: 9 << 20, Unit: "ssh.service", Start: 456},
		{PID: 4242, PPID: 1, Name: "postgres", Cmd: "postgres: checkpointer with a long command line to see how it wraps", User: "999", State: "R", CPU: 87.5, RSS: 300 << 20, Container: "abc123", Threads: 4, Start: 9999, Started: now.Add(-50 * time.Hour)},
		{PID: 5000, PPID: 1, Name: "node", Cmd: "node server.js", User: "debian", State: "S", CPU: 3.2, RSS: 1 << 30, Nice: 10, Start: 10000, Started: now.Add(-time.Hour)},
	}}
	m.facts.Containers[1].ID = "abc123def"
	m.watcher = health.Watcher{Installed: true, HaveStatus: true, Version: "v0.1.0", Mine: "dev", Every: 30, Checked: now.Add(-10 * time.Second),
		ConfigPath: "/home/debian/.config/opsie/config.toml", Notify: config.Notify{NtfyURL: "https://ntfy.sh/x"}}
	m.haveWatcher = true
	m.history = []watch.Event{
		{At: now.Add(-time.Hour), ID: "disk:/", Title: "Disk /", Summary: "96% · 1.2G free", Status: health.Crit, Kind: "problem"},
		{At: now.Add(-48 * time.Hour), ID: "ctr:web", Title: "web", Status: health.OK, Kind: "recovered"},
	}
	m.facts.Tunnels = []priv.Tunnel{{Kind: "container", Name: "cloudflared-tunnel-with-a-long-name", Running: true, Since: now.Add(-6 * 24 * time.Hour),
		TunnelID: "a3039a3f-329c-452f-a3ef-48afa025b95b", Version: "2026.10.7", Ready: 4, HaveReady: true,
		Locations: []string{"fra03", "fra08", "fra14", "fra17"}, Requests: 25713, Errors: 93, Statuses: map[string]uint64{"2xx": 20000, "5xx": 7},
		Remote: true, Routes: []priv.Route{
			{Host: "site.example.com", Service: "http://web:80"},
			{Host: "a-very-long-subdomain-name.example.com", Path: "/api", Service: "http://site-database-with-a-long-name:5432"},
			{Host: "elsewhere.example.com", Service: "http://10.0.0.5:8080"},
		},
		Origins: []priv.OriginError{{Host: "site.example.com", Count: 40, Recent: 3, Last: now.Add(-5 * time.Minute), Error: "nothing is listening on the app's port"}},
		Drops:   3, LastDrop: now.Add(-6 * time.Hour)}}
	m.facts.Sites = []sites.Result{
		{URL: "https://site.example.com", Host: "site.example.com", Route: true, Status: 502, At: now},
		{URL: "https://a-very-long-subdomain-name.example.com/api", Host: "a-very-long-subdomain-name.example.com", Route: true, Status: 200, Took: 180 * time.Millisecond, At: now, CertExpires: now.Add(60 * 24 * time.Hour)},
		{URL: "https://status.elsewhere-with-a-long-name.example.org/health", Host: "status.elsewhere-with-a-long-name.example.org", Status: 200, Took: 2 * time.Second, At: now},
	}
	m.hist.samples = make([]history.Sample, 0, 7*24*12)
	for i := 7*24*60 - 5; i >= 0; i -= 5 {
		at := m.now.Add(-time.Duration(i) * time.Minute)
		wave := float64((i / 60) % 24)
		m.hist.samples = append(m.hist.samples, history.Sample{At: at, CPU: 10 + wave*3, Mem: 60 + wave, Load: 0.2, Rx: 1e5 * wave, Tx: 2e4 * wave,
			Disks: map[string]float64{"/": 80 + float64(i)/10080*-10}})
	}
	m.hist.at = time.Now()
	m.facts.ImageChecks = []registry.Check{{Container: "web", Ref: "nginx:1.27", Newer: true, Remote: "sha256:abc", At: now}}
	m.dock.apps = []appFolder{
		{name: "site", dir: "/home/debian/apps/site", compose: "/home/debian/apps/site/compose.yaml"},
		{name: "a-sleeping-app-with-a-long-name", dir: "/home/debian/apps/a-sleeping-app-with-a-long-name", compose: "/home/debian/apps/a-sleeping-app-with-a-long-name/docker-compose.yml"},
	}
	m.facts.At = now
	m.updateVersion = "v9.9.9"
	m.evaluate()
	return m
}

// checkFrame fails unless every line is exactly as wide as the terminal
// and there are exactly as many lines as it is tall.
func checkFrame(t *testing.T, name string, frame string, w, h int) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if len(lines) != h {
		t.Errorf("%s at %dx%d: %d lines, want %d", name, w, h, len(lines), h)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != w {
			t.Errorf("%s at %dx%d: line %d is %d wide: %q", name, w, h, i, got, line)
			return
		}
	}
}

var sizes = [][2]int{
	{30, 10}, {34, 20}, {40, 24}, {45, 32}, {50, 40}, {60, 20}, {71, 30},
	{72, 30}, {90, 40}, {109, 30}, {110, 30}, {130, 45}, {200, 60},
}

// newTabs fills in what the Storage, Network and Agents tabs show.
func newTabs(m *Model) {
	now := time.Now()
	m.du = map[string]duEntry{"/": {at: now, u: priv.DirUsage{Path: "/", Size: 37 << 30, Children: []priv.DirSize{
		{Path: "/var", Size: 20 << 30}, {Path: "/home/a-user-with-a-very-long-name-indeed", Size: 9 << 30}, {Path: "/usr", Size: 3 << 30}, {Path: "/etc", Size: 4 << 10},
	}}}}
	m.clean = cleanState{at: now, c: priv.Cleanable{Journal: 2 << 30, AptCache: 300 << 20, Autoremove: []string{"libfoo1"}, AutoremoveSize: 40 << 20}}
	m.dock.unused = []priv.UnusedImage{{Ref: "ghcr.io/someone/an-app-with-a-long-name:1.0.0-beta.7", ID: "sha256:aa", Size: 400 << 20}, {ID: "sha256:bb", Size: 30 << 20}}
	m.net = netState{loaded: true,
		ifaces: []iface{{name: "ens3", up: true, addrs: []string{"203.0.113.50", "2001:db8:701:1100:aaaa:bbbb:cccc:45f"}, rx: 125 << 30, tx: 8 << 30}, {name: "veth1", virtual: true}},
		results: []speedtest.Result{
			{At: now.Add(-time.Hour), Down: 9.4e8, Up: 4.1e7, Ping: 12 * time.Millisecond, Jitter: 900 * time.Microsecond, Server: "FRA", Moved: 600 << 20},
			{At: now.Add(-30 * time.Hour), Down: 8.1e5, Up: 2.2e9, Ping: 140 * time.Millisecond, Jitter: 30 * time.Millisecond},
		}}
	m.agents = agentsState{at: now, list: []agents.Agent{
		{ID: "claude", Name: "Claude Code", Version: "2.9.1", Model: "claude-fable-5-2", LastActive: now.Add(-time.Minute),
			Today: agents.Tokens{Input: 1200, Output: 45000, CacheRead: 8 << 20, CacheWrite: 300000}, Week: agents.Tokens{Input: 9000, Output: 400000, CacheRead: 190 << 20},
			Daily: [agents.Days]uint64{0, 5 << 20, 90 << 20, 0, 60 << 20, 30 << 20, 8 << 20}, SessionsToday: 1, SessionsWeek: 7,
			Models: map[string]uint64{"claude-fable-5-2": 180 << 20, "claude-haiku-5-5": 10 << 20}, LimitsNote: "Claude Code doesn't write its plan limits to disk."},
		{ID: "codex", Name: "Codex", Version: "0.167.2", Plan: "plus", Model: "gpt-6.1-with-a-long-name", LastActive: now.Add(-9 * time.Hour),
			Week: agents.Tokens{Input: 5000, Output: 90000, CacheRead: 200 << 20}, Daily: [agents.Days]uint64{1 << 20, 0, 0, 0, 0, 0, 0}, SessionsWeek: 10,
			Models: map[string]uint64{"gpt-6.1-with-a-long-name": 200 << 20}, LimitsAt: now.Add(-9 * time.Hour),
			Limits: []agents.Window{{Name: "5 hours", Used: 0}, {Name: "Week", Used: 93, Resets: now.Add(50 * time.Hour)}}},
	}}
}

// services gives the Agents tab two services with keys.
func services(m *Model) {
	m.env.Config.Agents.OpenRouterKey, m.env.Config.Agents.DeepSeekKey = "sk-or-v1-test", "sk-test"
	m.agents.servicesAt = time.Now()
	m.agents.services = []agents.Service{
		{ID: "openrouter", Name: "OpenRouter", Label: "sk-or-v1-abc...123", HaveSpend: true, Today: 0.0042, Week: 3.1, Month: 12.4, AllTime: 25.75,
			HaveLimit: true, Limit: 50, Remaining: 4.25, Reset: "monthly", HaveCredits: true, Bought: 100.5, Used: 25.75, FreeUsed: 12, FreeLimit: 1000},
		{ID: "deepseek", Name: "DeepSeek", Balances: []agents.Balance{{Currency: "CNY", Total: 110, Granted: 10}}, Blocked: true},
	}
}

var screens = map[string]func(m *Model){
	"overview":          func(m *Model) {},
	"storage":           func(m *Model) { newTabs(m); m.switchTab(tabStorage) },
	"storage-measuring": func(m *Model) { m.switchTab(tabStorage) },
	"storage-last":      func(m *Model) { newTabs(m); m.switchTab(tabStorage); m.move(1 << 20) },
	"volume":            func(m *Model) { newTabs(m); m.switchTab(tabStorage); m.openItem(tabStorage, "vol:/") },
	"network":           func(m *Model) { newTabs(m); m.switchTab(tabNetwork) },
	"network-empty":     func(m *Model) { m.switchTab(tabNetwork) },
	"speedtest":         func(m *Model) { newTabs(m); m.switchTab(tabNetwork); m.openItem(tabNetwork, "speedtest") },
	"speedtest-running": func(m *Model) {
		newTabs(m)
		m.switchTab(tabNetwork)
		m.net.test, m.net.started = &speedtest.Test{}, time.Now()
	},
	"agents":          func(m *Model) { newTabs(m); m.switchTab(tabAgents) },
	"agents-none":     func(m *Model) { m.agents.at = time.Now(); m.switchTab(tabAgents) },
	"agents-services": func(m *Model) { newTabs(m); services(m); m.switchTab(tabAgents); m.move(1 << 20) },
	"service": func(m *Model) {
		newTabs(m)
		services(m)
		m.switchTab(tabAgents)
		m.openItem(tabAgents, "service:openrouter")
	},
	"service-refused": func(m *Model) {
		newTabs(m)
		services(m)
		m.agents.services[1] = agents.Service{ID: "deepseek", Name: "DeepSeek", Err: "the key was refused"}
		m.switchTab(tabAgents)
	},
	"agent":        func(m *Model) { newTabs(m); m.switchTab(tabAgents); m.openItem(tabAgents, "agent:claude") },
	"agent-limits": func(m *Model) { newTabs(m); m.switchTab(tabAgents); m.openItem(tabAgents, "agent:codex") },
	"prune-unused": func(m *Model) { newTabs(m); m.switchTab(tabStorage); m.openItem(tabStorage, "prune") },
	"detail":       func(m *Model) { m.openSelected() },
	"confirm": func(m *Model) {
		m.openItem(tabOverview, "reboot")
		m.chooseFix(m.detailFor(m.modal.key, 40).fixes[0])
	},
	"help":      func(m *Model) { m.help = true },
	"docker":    func(m *Model) { m.switchTab(tabDocker) },
	"container": func(m *Model) { m.switchTab(tabDocker); m.openItem(tabDocker, "ctr:web") },
	"project":   func(m *Model) { m.switchTab(tabDocker); m.openItem(tabDocker, "proj:site") },
	"prune":     func(m *Model) { m.switchTab(tabDocker); m.openItem(tabDocker, "prune") },
	"tunnel": func(m *Model) {
		m.switchTab(tabNetwork)
		m.openItem(tabNetwork, "tunnel:cloudflared-tunnel-with-a-long-name")
	},
	"route": func(m *Model) {
		m.switchTab(tabNetwork)
		m.openItem(tabNetwork, "route:a-very-long-subdomain-name.example.com/api")
	},
	"app": func(m *Model) {
		m.switchTab(tabDocker)
		m.openItem(tabDocker, "app:/home/debian/apps/a-sleeping-app-with-a-long-name")
	},
	"docker-last": func(m *Model) { m.switchTab(tabDocker); m.move(1 << 20) },
	"logs": func(m *Model) {
		m.switchTab(tabDocker)
		m.logs = &logView{container: "web", follow: true, wrap: true, loaded: true,
			lines: parseLogs("2026-11-10T13:00:00.000000000Z GET / 200 " + strings.Repeat("very long line ", 20) + "\n2026-11-10T13:00:01Z ERROR something broke\nno timestamp here\n")}
	},
	"security": func(m *Model) { m.switchTab(tabSecurity) },
	"settings": func(m *Model) { m.switchTab(tabSettings) },
	"settings-last": func(m *Model) {
		m.switchTab(tabSettings)
		m.move(1 << 20)
	},
	"setting-choice": func(m *Model) { m.switchTab(tabSettings); m.openItem(tabSettings, "set:theme") },
	"setting-input": func(m *Model) {
		m.switchTab(tabSettings)
		m.openItem(tabSettings, "set:notify.ntfy_url")
		m.chooseFix(m.detailFor(m.modal.key, 40).fixes[0])
	},
	"updates": func(m *Model) { m.switchTab(tabUpdates) },
	"security-last": func(m *Model) {
		m.switchTab(tabSecurity)
		m.move(1 << 20)
	},
	"key": func(m *Model) {
		m.switchTab(tabSecurity)
		m.openItem(tabSecurity, "key:debian:SHA256:AAAAbbbbCCCCddddEEEEffffGGGGhhhhIIIIjjjjKKK")
	},
	"key-add": func(m *Model) { m.switchTab(tabSecurity); m.openItem(tabSecurity, "do:keyadd:debian") },
	"key-typed": func(m *Model) {
		m.switchTab(tabSecurity)
		m.openItem(tabSecurity, "do:keyadd:debian")
		m.modal.input.SetValue("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGthEC3exNQSmVGJF5mrLIKNsnnnFtMLZKfIOeAzHOfD me@laptop")
	},
	"port":      func(m *Model) { m.switchTab(tabSecurity); m.openItem(tabSecurity, "port:tcp:5355:systemd-resolve") },
	"pkg":       func(m *Model) { m.switchTab(tabUpdates); m.openItem(tabUpdates, "pkg:libssl3t64") },
	"stale":     func(m *Model) { m.switchTab(tabUpdates); m.openItem(tabUpdates, "stale:systemd") },
	"services":  func(m *Model) { m.switchTab(tabServices) },
	"unit":      func(m *Model) { m.switchTab(tabServices); m.openItem(tabServices, "unit:ssh.service") },
	"timer":     func(m *Model) { m.switchTab(tabServices); m.openItem(tabServices, "timer:apt-daily.timer") },
	"procs":     func(m *Model) { m.switchTab(tabProcs) },
	"procs-mem": func(m *Model) { m.switchTab(tabProcs); m.setProcSort(true) },
	"proc":      func(m *Model) { m.switchTab(tabProcs); m.openItem(tabProcs, "pid:4242") },
	"unit-logs": func(m *Model) {
		m.logs = &logView{container: "ssh.service", unit: true, follow: true, wrap: true, loaded: true, lines: parseLogs("2026-11-10T13:00:00Z Accepted publickey\n")}
	},
	"logs-nowrap": func(m *Model) {
		m.logs = &logView{container: "web", follow: false, offset: 2, loaded: true, lines: parseLogs(strings.Repeat("2026-11-10T13:00:00Z line\n", 100))}
	},
}

func TestEveryScreenFitsEverySize(t *testing.T) {
	for _, size := range sizes {
		w, h := size[0], size[1]
		for name, setup := range screens {
			m := testModel(t)
			m.width, m.height = w, h
			setup(m)
			checkFrame(t, name, m.render(), w, h)
		}
	}
}

func TestTooSmallSaysSo(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 20, 8
	if !strings.Contains(m.render(), "bigger") {
		t.Fatal("tiny terminals should ask for more room")
	}
}

func TestWorstResultIsSelectedFirst(t *testing.T) {
	m := testModel(t)
	if got := m.results[m.lists[tabOverview].sel].Status; got != health.Crit {
		t.Fatalf("selected a %v result first, want the worst one", got)
	}
}

func TestSelectionFollowsResultAcrossUpdates(t *testing.T) {
	m := testModel(t)
	m.move(2)
	l := &m.lists[tabOverview]
	id := m.results[l.sel].ID
	m.facts.FailedUnits = nil // results shift
	m.evaluate()
	if m.results[l.sel].ID != id {
		t.Fatalf("selection moved to %s, want %s", m.results[l.sel].ID, id)
	}
}

func TestPhoneLayoutScrollsToSelection(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 40, 16
	m.render()
	m.move(len(m.results)) // last
	frame := m.render()
	if m.lists[tabOverview].scroll == 0 {
		t.Fatal("expected the page to scroll to the last result")
	}
	if on, _, _ := strings.Cut(m.th.selected.Render("x"), "x"); !strings.Contains(frame, on) {
		t.Fatal("the selected row should be on screen")
	}
}

func TestTapOpensResult(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 45, 40
	m.render()
	// The checklist is under the card of readings; its first row is where
	// it was drawn.
	y := -1
	for _, s := range m.spots {
		if s.id == m.results[0].ID {
			y = s.y
		}
	}
	m.handleMouse(tea.MouseClickMsg{X: 5, Y: headerRows + y, Button: tea.MouseLeft})
	if m.modal == nil {
		t.Fatal("tapping a checklist row should open it")
	}
	if m.modal.key != "result:"+m.results[0].ID {
		t.Fatalf("opened %s, want %s", m.modal.key, m.results[0].ID)
	}
}

func TestDangerousFixNeedsConfirmAndCountdown(t *testing.T) {
	m := testModel(t)
	m.openItem(tabOverview, "reboot")
	fixes := m.detailFor(m.modal.key, 40).fixes
	if len(fixes) == 0 {
		t.Fatal("reboot result missing")
	}
	if cmd := m.chooseFix(fixes[0]); cmd != nil {
		t.Fatal("a dangerous fix must not run before it's confirmed")
	}
	if m.modal.stage != stageConfirm {
		t.Fatalf("stage %v, want confirm", m.modal.stage)
	}
	if m.modal.focus != 1 {
		t.Fatal("Cancel should be focused so Enter alone doesn't reboot")
	}
	m.confirmFix()
	if m.modal.stage != stageCountdown || m.modal.count != 10 {
		t.Fatalf("stage %v count %d, want a 10s countdown", m.modal.stage, m.modal.count)
	}
	m.modalKey("esc")
	if m.modal.stage != stageView {
		t.Fatal("esc should cancel the countdown")
	}
	if cmd := m.countdownTick(m.modal.countID); cmd != nil {
		t.Fatal("a cancelled countdown must not keep going")
	}
}

func TestTabsSwitchByKeyAndTap(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 45, 30
	m.render()
	m.handleKey(tea.KeyPressMsg{Code: '2', Text: "2"})
	if m.tab != tabDocker {
		t.Fatal("2 should open the Docker tab")
	}
	m.render()
	// On a phone the tab bar is the second row from the bottom.
	m.handleMouse(tea.MouseClickMsg{X: 3, Y: m.height - 2, Button: tea.MouseLeft})
	if m.tab != tabOverview {
		t.Fatal("tapping the first tab should go back to Overview")
	}
	m.width = 120
	m.render()
	m.handleMouse(tea.MouseClickMsg{X: 15, Y: 1, Button: tea.MouseLeft})
	if m.tab != tabDocker {
		t.Fatal("on wide screens the tabs are in the header")
	}
}

func TestDockerGroupsByProject(t *testing.T) {
	m := testModel(t)
	ids := m.dockerItems()
	want := []string{"prune", "proj:site", "ctr:init", "ctr:site-database-with-a-long-name", "ctr:web", "ctr:worker",
		"app:/home/debian/apps/a-sleeping-app-with-a-long-name"}
	if strings.Join(ids, " ") != strings.Join(want, " ") {
		t.Fatalf("items %v, want %v", ids, want)
	}
}

func TestContainerActionsFitState(t *testing.T) {
	m := testModel(t)
	labels := func(key string) string {
		var out []string
		for _, f := range m.detailFor(key, 60).fixes {
			out = append(out, f.Label)
		}
		return strings.Join(out, ",")
	}
	if got := labels("ctr:web"); got != "Logs,Shell,Restart,Stop" {
		t.Errorf("running container: %s", got)
	}
	if got := labels("ctr:worker"); got != "Start,Logs,Remove" {
		t.Errorf("stopped container: %s", got)
	}
	for _, f := range m.detailFor("ctr:web", 60).fixes {
		if (f.Label == "Stop") != f.Danger {
			t.Errorf("%s: danger=%v", f.Label, f.Danger)
		}
	}
}

func TestProjectUpdateRespectsRoom(t *testing.T) {
	m := testModel(t)
	has := func(label string) bool {
		for _, f := range m.detailFor("proj:site", 60).fixes {
			if f.Label == label {
				return true
			}
		}
		return false
	}
	if !has("Pull & recreate") || !has("Rebuild & recreate") {
		t.Fatal("plenty of room should offer pull and rebuild")
	}
	m.dock.preflight["site"] = preflightState{p: priv.Preflight{Free: 1 << 30, Images: 2 << 30, MemAvailable: 2 << 30}}
	if has("Pull & recreate") || !has("Free up space") {
		t.Fatal("no room: don't offer the pull, offer to free space")
	}
	m.dock.preflight["site"] = preflightState{p: priv.Preflight{Free: 20 << 30, Images: 2 << 30, MemAvailable: 256 << 20}}
	if !has("Pull & recreate") || has("Rebuild & recreate") {
		t.Fatal("low memory: pulling is fine, building isn't")
	}
	m.dock.preflight["site"] = preflightState{loading: true}
	if has("Pull & recreate") {
		t.Fatal("wait for the check before offering an update")
	}
}

func TestRoom(t *testing.T) {
	const g = 1 << 30
	for _, c := range []struct {
		p              priv.Preflight
		build          bool
		blocked, warns bool
	}{
		{priv.Preflight{Free: 50 * g, Images: 2 * g}, false, false, false},
		{priv.Preflight{Free: 4 * g, Images: 2 * g}, false, false, true},
		{priv.Preflight{Free: 2 * g, Images: 2 * g}, false, true, false},
		{priv.Preflight{Free: 50 * g, Images: 2 * g, MemAvailable: 1 * g}, true, false, true},
		{priv.Preflight{Free: 50 * g, Images: 2 * g, MemAvailable: 100 << 20}, true, true, false},
	} {
		blocked, warn := room(c.p, c.build)
		if (blocked != "") != c.blocked || (warn != "") != c.warns {
			t.Errorf("%+v build=%v: blocked=%q warn=%q", c.p, c.build, blocked, warn)
		}
	}
}

func TestParseLogs(t *testing.T) {
	lines := parseLogs("2026-11-10T13:00:00.123456789Z \x1b[31mred\x1b[0m text\r\nplain\tline\n\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines", len(lines))
	}
	if lines[0].at.IsZero() || lines[0].text != "red text" {
		t.Errorf("first: %+v", lines[0])
	}
	if !lines[1].at.IsZero() || lines[1].text != "plain    line" {
		t.Errorf("second: %+v", lines[1])
	}
}

func TestOwnStamps(t *testing.T) {
	if !ownStamps(parseLogs("2026-11-10T13:00:00Z 2026-11-10 16:57:53.404 +03 LOG x\n2026-11-10T13:00:00Z [13:00:01] y\n")) {
		t.Error("lines with their own times")
	}
	if ownStamps(parseLogs("2026-11-10T13:00:00Z GET / 200\n2026-11-10T13:00:00Z ready\n")) {
		t.Error("lines without")
	}
}

func TestLogsFollowAndScroll(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 45, 20
	m.logs = &logView{container: "web", follow: true, wrap: true, loaded: true, lines: parseLogs(strings.Repeat("2026-11-10T13:00:00Z x\n", 100))}
	m.render()
	m.logsKey("up", 5)
	if m.logs.follow || m.logs.offset != 1 {
		t.Fatal("scrolling up should pause following")
	}
	m.logsKey("G", 5)
	if !m.logs.follow || m.logs.offset != 0 {
		t.Fatal("G should go back to live")
	}
	m.logsKey("esc", 5)
	if m.logs != nil {
		t.Fatal("esc should close the logs")
	}
}

func TestFooterHintsAreTappable(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 45, 30
	m.render()
	var found bool
	for _, h := range m.hits {
		if h.y == m.height-1 {
			found = true
		}
	}
	if !found {
		t.Fatal("expected tappable hints on the bottom row")
	}
}

func TestChartFitsWidth(t *testing.T) {
	th := newTheme()
	for _, w := range []int{1, 5, 40} {
		rows := th.chart([]float64{1, 50, 100, 0}, w, 3, 100, func(float64) colorT { return th.accent })
		for _, r := range rows {
			if lipgloss.Width(r) != w {
				t.Fatalf("chart row %d wide, want %d", lipgloss.Width(r), w)
			}
		}
	}
}

func TestAddKeyGoesStraightToInput(t *testing.T) {
	m := testModel(t)
	m.switchTab(tabSecurity)
	m.openItem(tabSecurity, "do:keyadd:debian")
	if m.modal == nil || m.modal.stage != stageInput {
		t.Fatal("Add a key should open the key field directly")
	}
	// Typing goes to the field, even keys that mean something elsewhere.
	for _, r := range "q1?" {
		m.handleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if m.modal == nil || m.modal.input.Value() != "q1?" || m.tab != tabSecurity {
		t.Fatalf("keys leaked out of the field: %+v", m.modal)
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.modal == nil || m.modal.err == "" || m.modal.stage != stageInput {
		t.Fatal("a bad key should be refused in place")
	}
	m.Update(tea.PasteMsg{Content: "ssh-ed25519 " + strings.Fields(testPubKey)[1] + " me@laptop"})
	m.modal.input.SetValue(strings.TrimPrefix(m.modal.input.Value(), "q1?"))
	if note, problem := checkInput("key", m.modal.input.Value()); problem != "" || !strings.Contains(note, "SHA256:") {
		t.Fatalf("pasted key not recognised: %q %q", note, problem)
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.modal != nil {
		t.Fatal("esc should close a direct action")
	}
}

func TestInputArgsReachTheFix(t *testing.T) {
	m := testModel(t)
	m.switchTab(tabSecurity)
	m.openItem(tabSecurity, "do:fw:deny")
	m.modal.input.SetValue("198.51.100.7")
	m.submitInput()
	// No helper in tests, so it stops at "locked", but the fix carries the value.
	if m.modal.fix.Args["ip"] != "198.51.100.7" || m.modal.fix.Args["caller_ip"] != "203.0.113.50" {
		t.Fatalf("args %v", m.modal.fix.Args)
	}
}

func TestOwnSessionAndKeyAreProtected(t *testing.T) {
	m := testModel(t)
	b := func() *detailBuilder { return &detailBuilder{t: m.th, inner: 40} }
	if d, _ := m.securityDetail("sess:60", b()); len(d.fixes) != 0 {
		t.Fatal("offered to end your own session")
	}
	if d, _ := m.securityDetail("sess:12", b()); len(d.fixes) != 1 || !d.fixes[0].Danger {
		t.Fatal("other sessions should be endable, with a confirmation")
	}
	if d, _ := m.securityDetail("key:debian:SHA256:24zRnbKRJ19wQ5mHB7jiO1axVccfHWsToUojIeLv2j0", b()); len(d.fixes) != 0 {
		t.Fatal("offered to remove the key you're logged in with")
	}
}

func TestResultsMarkTheirTab(t *testing.T) {
	for id, want := range map[string]tabID{"ctr:web": tabDocker, "ssh:passwords": tabSecurity, "updates:auto": tabUpdates, "reboot": tabUpdates, "load": tabOverview} {
		if got := resultTab(id); got != want {
			t.Errorf("%s: tab %d, want %d", id, got, want)
		}
	}
}

// On a phone the tabs are a strip around the one you're on; its ends lead
// to the rest.
func TestPhoneTabsAreAStrip(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 40, 30
	bar := func() string { return ansi.Strip(strings.Split(m.render(), "\n")[m.height-2]) }
	if b := bar(); !strings.Contains(b, "Home") || !strings.Contains(b, "›") || strings.Contains(b, "‹") {
		t.Fatalf("on the first tab the strip starts with it and leads right: %q", b)
	}
	// The arrow at the right end goes to the first tab that isn't showing.
	shown := strings.Count(bar(), "  ")
	m.handleMouse(tea.MouseClickMsg{X: 38, Y: m.height - 2, Button: tea.MouseLeft})
	if m.tab == tabOverview || shown == 0 {
		t.Fatalf("tapping › should move along, tab %d", m.tab)
	}
	if b := bar(); !strings.Contains(b, tabs[m.tab].short) {
		t.Fatalf("the strip should follow to %s: %q", tabs[m.tab].short, b)
	}
	// Whatever tab you're on, its name is in the strip, at every width.
	for _, w := range []int{30, 34, 40, 45, 60} {
		m.width = w
		for _, tb := range tabs {
			m.switchTab(tb.id)
			if b := bar(); !strings.Contains(b, tb.short) {
				t.Errorf("at %d the bar lacks %s: %q", w, tb.short, b)
			}
		}
	}
	// The gaps between tabs are all the same.
	for _, w := range []int{36, 45, 52, 60, 71} {
		m.width = w
		m.switchTab(tabOverview)
		// A tab's status mark is part of its label.
		line := strings.NewReplacer(" ✗", "", " ▲", "").Replace(bar())
		names := strings.Fields(strings.Trim(line, " ‹›"))
		gaps := map[int]bool{}
		rest := strings.Trim(line, " ‹›")
		for i, name := range names[:len(names)-1] {
			rest = rest[len(name):]
			gap := len(rest) - len(strings.TrimLeft(rest, " "))
			rest = rest[gap:]
			gaps[gap] = true
			_ = i
		}
		if len(gaps) > 1 {
			t.Errorf("at %d the gaps differ: %q", w, bar())
		}
	}
	// 0 is the tenth tab's key.
	m.handleKey(tea.KeyPressMsg{Code: '0', Text: "0"})
	if m.tab != tabSettings {
		t.Fatalf("0 should open Settings, tab %d", m.tab)
	}
}

const testPubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGthEC3exNQSmVGJF5mrLIKNsnnnFtMLZKfIOeAzHOfD"

func TestServicesGroupByState(t *testing.T) {
	m := testModel(t)
	ids := strings.Join(m.items(tabServices), " ")
	want := "unit:a-service-with-a-really-long-name-for-testing.service unit:ssh.service unit:docker.service timer:apt-daily.timer timer:never.timer cron:job:0 cron:job:1 cron:every:daily unit:cron.service"
	if ids != want {
		t.Fatalf("items %s", ids)
	}
}

func TestVitalServicesCantBeStopped(t *testing.T) {
	m := testModel(t)
	labels := func(key string) string {
		var out []string
		for _, f := range m.detailFor(key, 40).fixes {
			out = append(out, f.Label)
		}
		return strings.Join(out, ",")
	}
	if got := labels("unit:ssh.service"); got != "Restart,Reload,Logs" {
		t.Fatalf("ssh: %s", got)
	}
	if got := labels("unit:cron.service"); got != "Start,Logs,Don't start at boot" {
		t.Fatalf("cron: %s", got)
	}
	if f := m.detailFor("unit:docker.service", 40).fixes[0]; !f.Danger {
		t.Fatal("restarting docker should ask first")
	}
}

func TestProcsSortAndProtect(t *testing.T) {
	m := testModel(t)
	if ids := m.procItems(); ids[0] != "pid:4242" || len(ids) != 4 {
		t.Fatalf("by CPU: %v", ids)
	}
	m.setProcSort(true)
	if ids := m.procItems(); ids[0] != "pid:5000" {
		t.Fatalf("by memory: %v", ids)
	}
	d := m.detailFor("pid:4242", 40)
	if len(d.fixes) < 3 || d.fixes[0].Args["start"] != "9999" || !d.fixes[0].Danger {
		t.Fatalf("fixes %+v", d.fixes)
	}
	if last := d.fixes[len(d.fixes)-1]; last.Special != "open:docker:ctr:site-database-with-a-long-name" {
		t.Fatalf("container link %+v", last)
	}
	if d := m.detailFor("pid:1", 40); len(d.fixes) != 0 {
		t.Fatal("offered to stop PID 1")
	}
	if d := m.detailFor("pid:5000", 40); d.fixes[2].Label != "Normal priority" {
		t.Fatalf("niced process: %+v", d.fixes)
	}
}

func TestOpenLinksSwitchTabs(t *testing.T) {
	m := testModel(t)
	m.runFix(health.Fix{Special: "open:services:unit:ssh.service"})
	if m.tab != tabServices || m.modal == nil || m.modal.key != "unit:ssh.service" {
		t.Fatalf("tab %d modal %+v", m.tab, m.modal)
	}
}

func TestArrowsMoveBetweenColumns(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 130, 40
	m.switchTab(tabSecurity)
	m.render()
	sel := func() string { return m.items(m.tab)[m.lists[m.tab].sel] }
	if sel() != "ssh:passwords" {
		t.Fatalf("start %s", sel())
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	m.render()
	if !strings.HasPrefix(sel(), "key:") {
		t.Fatalf("→ went to %s, want the SSH keys card beside Access", sel())
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	m.render()
	if !strings.HasPrefix(sel(), "ssh:") {
		t.Fatalf("← went to %s", sel())
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.modal != nil {
		t.Fatal("→ must not open anything")
	}
	// One column on a phone: nothing beside, nothing happens.
	m.width = 45
	m.render()
	before := sel()
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if sel() != before || m.modal != nil {
		t.Fatal("→ on one column should do nothing")
	}
}

func TestSingleContainerProjectIsOneItem(t *testing.T) {
	m := testModel(t)
	m.facts.Containers = append(m.facts.Containers, priv.Container{Name: "blog-app", State: "running", Image: "ghost:5", Project: "blog", Service: "app",
		WorkingDir: "/home/debian/apps/blog", ConfigFiles: "/home/debian/apps/blog/compose.yaml", Started: time.Now().Add(-time.Hour)})
	m.dock.preflight["blog"] = preflightState{p: priv.Preflight{Free: 20 << 30, Images: 1 << 30, MemAvailable: 2 << 30}}
	ids := strings.Join(m.dockerItems(), " ")
	if !strings.Contains(ids, "proj:blog") || strings.Contains(ids, "ctr:blog-app") {
		t.Fatalf("items %s", ids)
	}
	var labels []string
	for _, f := range m.detailFor("proj:blog", 40).fixes {
		labels = append(labels, f.Label)
	}
	if got := strings.Join(labels, ","); got != "Logs,Shell,Restart,Pull & recreate,Rebuild & recreate,Open folder,Backups,Stop" {
		t.Fatalf("actions %s", got)
	}
	// The container's row opens the project too.
	m.width, m.height = 45, 60
	m.switchTab(tabDocker)
	frame := m.render()
	row := -1
	for i, line := range strings.Split(frame, "\n") {
		if strings.Contains(ansi.Strip(line), "blog-app") {
			row = i
		}
	}
	m.handleMouse(tea.MouseClickMsg{X: 5, Y: row, Button: tea.MouseLeft})
	if m.modal == nil || m.modal.key != "proj:blog" {
		t.Fatalf("tap opened %+v", m.modal)
	}
	for _, sz := range sizes {
		m.modal = nil
		m.width, m.height = sz[0], sz[1]
		checkFrame(t, "single", m.render(), sz[0], sz[1])
		m.openItem(tabDocker, "proj:blog")
		checkFrame(t, "single-detail", m.render(), sz[0], sz[1])
	}
}

func TestButtonArrowsFollowLayout(t *testing.T) {
	m := testModel(t)
	m.switchTab(tabProcs)
	m.openItem(tabProcs, "pid:5000")
	// Wide: one row, so ← → choose and ↓ doesn't.
	m.width, m.height = 130, 40
	m.render()
	if m.modal.rows != 1 {
		t.Fatalf("expected one row of buttons at 130 columns, got %d", m.modal.rows)
	}
	m.modal.focus = 0
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.modal.focus != 0 {
		t.Fatal("↓ moved along a row of buttons")
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.modal.focus != 1 {
		t.Fatal("→ should choose the next button in a row")
	}
	// Phone: the buttons become a menu of a few rows.
	m.width, m.height = 40, 40
	m.render()
	d := m.modal
	if d.rows < 2 {
		t.Fatalf("expected the %d buttons to make a menu of a few rows, got %d", len(d.spots), d.rows)
	}
	// → runs on along the buttons, across rows.
	d.focus = 0
	for i := 1; i < len(d.spots); i++ {
		m.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
		if d.focus != i {
			t.Fatalf("→ should reach button %d, focus %d", i, d.focus)
		}
	}
	// ↓ goes to the row below, ↑ back up.
	d.focus = 0
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if d.spots[d.focus].y != 1 {
		t.Fatalf("↓ should move to the second row, focus %d at row %d", d.focus, d.spots[d.focus].y)
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if d.spots[d.focus].y != 0 {
		t.Fatal("↑ should go back to the first row")
	}
	if m.hints()[1].key != "←→↑↓" {
		t.Fatalf("the hint should name all four arrows, got %q", m.hints()[1].key)
	}
	// Every button stays inside the popup.
	for i, sp := range d.spots {
		if sp.x+sp.w > 40-2-4 {
			t.Fatalf("button %d runs past the popup: %+v", i, sp)
		}
	}
}

func TestPopupClosesOnTapOutside(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 130, 40
	m.switchTab(tabUpdates)
	m.openItem(tabUpdates, "pkg:libssl3t64")
	frame := strings.Split(m.render(), "\n")
	// The page shows behind the popup.
	if !strings.Contains(ansi.Strip(strings.Join(frame, "\n")), "Automatic updates") {
		t.Fatal("the page should stay visible behind the popup")
	}
	// Tapping the popup's text does nothing.
	row := -1
	for i, line := range frame {
		if strings.Contains(ansi.Strip(line), "Installed") {
			row = i
		}
	}
	m.handleMouse(tea.MouseClickMsg{X: 65, Y: row, Button: tea.MouseLeft})
	if m.modal == nil {
		t.Fatal("a tap inside the popup closed it")
	}
	m.handleMouse(tea.MouseClickMsg{X: 2, Y: 20, Button: tea.MouseLeft})
	if m.modal != nil {
		t.Fatal("a tap outside should close the popup")
	}
}

func TestDangerNeverStartsFocused(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 100, 40
	m.switchTab(tabProcs)
	m.openItem(tabProcs, "pid:4242")
	m.render()
	if b := m.buttons()[m.modal.focus]; b.danger {
		t.Fatalf("opened with %q focused", b.label)
	}
	// Choose Stop on purpose: the confirmation still starts on Cancel,
	// and digits can't skip past it.
	m.modal.focus = 0
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.render()
	if m.modal.stage != stageConfirm || m.buttons()[m.modal.focus].label != "Cancel" {
		t.Fatalf("stage %d focus %d", m.modal.stage, m.modal.focus)
	}
	m.handleKey(tea.KeyPressMsg{Code: '1', Text: "1"})
	if m.modal.stage != stageConfirm {
		t.Fatal("a digit went ahead with the action")
	}
	// Enter twice in a row cancels, and back in the view nothing drastic
	// is focused again.
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.render()
	if m.modal.stage != stageView || m.buttons()[m.modal.focus].danger {
		t.Fatalf("after cancel: stage %d, focused %q", m.modal.stage, m.buttons()[m.modal.focus].label)
	}
}

func TestTunnelLinksAppsAndWarns(t *testing.T) {
	m := testModel(t)
	// The tunnel and the sites live with the network, not with Docker.
	net := strings.Join(m.items(tabNetwork), " ")
	for _, want := range []string{"tunnel:cloudflared-tunnel-with-a-long-name", "route:site.example.com", "site:https://status.elsewhere-with-a-long-name.example.org/health"} {
		if !strings.Contains(net, want) {
			t.Errorf("network items lack %s: %s", want, net)
		}
	}
	if resultTab("tunnel:x") != tabNetwork {
		t.Error("a tunnel warning should lead to the Network tab")
	}
	ids := strings.Join(m.dockerItems(), " ")
	if strings.Contains(ids, "tunnel:") || strings.Contains(ids, "route:") {
		t.Errorf("tunnel still listed under Docker: %s", ids)
	}
	// The running app isn't listed as not started.
	if strings.Contains(ids, "app:/home/debian/apps/site ") || strings.HasSuffix(ids, "app:/home/debian/apps/site") {
		t.Errorf("running app listed as idle: %s", ids)
	}
	if page := ansi.Strip(m.render()); strings.Contains(page, "Free up disk space") {
		t.Error("the Docker card still has a clean-up row; its popup has it")
	}
	if !strings.HasSuffix(ids, "app:/home/debian/apps/a-sleeping-app-with-a-long-name") {
		t.Errorf("idle apps should be last: %s", ids)
	}

	routes := m.routesTo("site", "")
	if len(routes) != 2 {
		t.Fatalf("site routes %+v", routes)
	}
	m.width, m.height = 45, 200
	m.switchTab(tabDocker)
	if page := ansi.Strip(m.render()); !strings.Contains(page, "↗ site.example.com") || !strings.Contains(page, "Not started") {
		t.Errorf("page lacks links or idle apps:\n%s", page)
	}

	d := m.detailFor("route:site.example.com", 40)
	if !strings.Contains(ansi.Strip(d.headline), "down") {
		t.Errorf("route headline %q", ansi.Strip(d.headline))
	}
	if len(d.fixes) == 0 || d.fixes[0].Special != "open:docker:proj:site" {
		t.Errorf("route fixes %+v", d.fixes)
	}
	// The checklist warns about the site that's down.
	var found bool
	for _, r := range m.results {
		if strings.HasPrefix(r.ID, "tunnel:") {
			found = r.Status == health.Warn && strings.Contains(r.Summary, "site.example.com")
		}
	}
	if !found {
		t.Error("no tunnel warning in the checklist")
	}
	// A stopped tunnel is a problem.
	m.facts.Tunnels[0].Running = false
	m.evaluate()
	for _, r := range m.results {
		if strings.HasPrefix(r.ID, "tunnel:") && r.Status != health.Crit {
			t.Errorf("stopped tunnel is %v", r.Status)
		}
	}
}

func TestIdleAppStarts(t *testing.T) {
	m := testModel(t)
	d := m.detailFor("app:/home/debian/apps/a-sleeping-app-with-a-long-name", 40)
	if len(d.fixes) < 2 || d.fixes[0].Op != "compose.up" || d.fixes[0].Args["dir"] != "/home/debian/apps/a-sleeping-app-with-a-long-name" || d.fixes[1].Special != "folder" {
		t.Fatalf("fixes %+v", d.fixes)
	}
}

func TestThemes(t *testing.T) {
	for _, name := range config.Themes {
		if _, ok := palettes[name]; !ok {
			t.Errorf("theme %s has no palette", name)
		}
	}
	if th := themeFor(config.Config{Theme: "nope"}); th.name != "opsie" {
		t.Errorf("an unknown theme should get the default, got %s", th.name)
	}
	lin := func(v uint32) float64 {
		c := float64(v>>8) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	lum := func(c colorT) float64 {
		r, g, b, _ := c.RGBA()
		return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
	}
	oklab := func(c colorT) [3]float64 {
		r32, g32, b32, _ := c.RGBA()
		r, g, b := lin(r32), lin(g32), lin(b32)
		l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
		m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
		s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
		return [3]float64{
			0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
			1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
			0.0259040371*l + 0.7827717662*m - 0.8086757660*s,
		}
	}
	contrast := func(a, b float64) float64 { return (max(a, b) + 0.05) / (min(a, b) + 0.05) }
	accents := map[float64]string{}
	for name, p := range palettes {
		if other, dup := accents[p.accent]; dup {
			t.Errorf("%s and %s share an accent", name, other)
		}
		accents[p.accent] = name
		for g, gname := range map[ground]string{groundDark: "dark", groundLight: "light", groundUnknown: "unknown"} {
			th := themeNamed(name, g)
			roles := map[string]colorT{
				"accent": th.accent, "nav": th.nav, "cpu": th.cpu, "mem": th.mem, "disk": th.disk, "rx": th.rx, "tx": th.tx,
				"ok": th.ok, "info": th.info, "warn": th.warn, "danger": th.danger, "grey": th.muted,
			}
			for a, ca := range roles {
				if _, none := ca.(lipgloss.NoColor); none {
					continue
				}
				// Colors have to read on the terminal's background: on
				// black and on white both when it isn't known.
				l := lum(ca)
				onDark, onLight := contrast(l, 0.01), contrast(l, 1)
				switch {
				case g == groundDark && onDark < 4.5:
					t.Errorf("%s on %s: %s is %.1f:1 on a dark background", name, gname, a, onDark)
				case g == groundLight && onLight < 3.6:
					t.Errorf("%s on %s: %s is %.1f:1 on a light background", name, gname, a, onLight)
				case g == groundUnknown && (onDark < 3 || onLight < 3):
					t.Errorf("%s on %s: %s is %.1f:1 on dark and %.1f:1 on light", name, gname, a, onDark, onLight)
				}
				// Within a theme every part has its own color: the
				// selection's isn't a chart's, a chart's isn't a status's.
				// Distances are in OKLab, where 0.05 is about as close as
				// two colors of one brightness can be and still be told
				// apart at a glance. mono has no colors to tell apart.
				if name == "mono" || a == "grey" {
					continue
				}
				for b, cb := range roles {
					if a >= b || b == "grey" {
						continue
					}
					x, y := oklab(ca), oklab(cb)
					if d := math.Sqrt((x[0]-y[0])*(x[0]-y[0]) + (x[1]-y[1])*(x[1]-y[1]) + (x[2]-y[2])*(x[2]-y[2])); d < 0.05 {
						t.Errorf("%s on %s: %s and %s look alike, %.3f apart", name, gname, a, b, d)
					}
				}
			}
		}
	}
}

// The arrows go where the page goes: down a column and on to the next
// one, however many columns the width gives it.
func TestArrowsFollowColumns(t *testing.T) {
	for _, width := range []int{60, 130, 200} {
		m := testModel(t)
		m.width, m.height = width, 40
		m.switchTab(tabSecurity)
		m.render()
		ids := m.items(m.tab)
		sel := func() string { return m.items(m.tab)[m.lists[m.tab].sel] }
		at := func(id string) hit {
			for _, s := range m.spots {
				if s.id == id {
					return s
				}
			}
			t.Fatalf("width %d: %s isn't drawn", width, id)
			return hit{}
		}
		columns := map[int]bool{}
		for _, s := range m.spots {
			columns[s.x] = true
		}
		if want := columnsFor(width); len(columns) != want {
			t.Fatalf("width %d: %d columns, want %d", width, len(columns), want)
		}
		seen := map[string]bool{sel(): true}
		for range ids {
			before := at(sel())
			m.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
			m.render()
			after := at(sel())
			if after.id == before.id {
				break
			}
			if seen[after.id] {
				t.Fatalf("width %d: ↓ came back to %s", width, after.id)
			}
			seen[after.id] = true
			switch {
			case after.x == before.x && after.y <= before.y:
				t.Fatalf("width %d: ↓ went up, from %s to %s", width, before.id, after.id)
			case after.x < before.x:
				t.Fatalf("width %d: ↓ went left, from %s to %s", width, before.id, after.id)
			}
		}
		if len(seen) != len(ids) {
			t.Fatalf("width %d: ↓ reached %d of %d items", width, len(seen), len(ids))
		}
		// And all the way back.
		for range ids {
			m.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
			m.render()
		}
		if sel() != ids[0] {
			t.Fatalf("width %d: ↑ ended on %s, want %s", width, sel(), ids[0])
		}
	}
}

func TestMotdFitsAndNamesIssues(t *testing.T) {
	m := testModel(t)
	env := m.env
	now := time.Now()
	st := daemon.Status{Checked: now.Add(-20 * time.Second), Every: 30, Issues: []daemon.Issue{
		{Status: health.Crit, Title: "Disk /", Summary: "96% · 1.2G free"},
		{Status: health.Warn, Title: "Restart", Summary: "needed · new kernel"},
		{Status: health.Warn, Title: "Tunnel", Summary: "site.example.com is down"},
		{Status: health.Warn, Title: "Updates", Summary: "2 waiting"},
	}}
	for _, w := range []int{30, 45, 80, 200} {
		text := ansi.Strip(motdText(m.th, env, m.snap, st, true, true, now, w))
		lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		for _, l := range lines {
			if lipgloss.Width(l) > w {
				t.Errorf("at %d: %q is too wide", w, l)
			}
		}
		if !(strings.Contains(lines[0], "attention") || strings.Contains(lines[0], "4 issues")) || !strings.Contains(text, "Disk /") || !strings.Contains(text, "and 1 more") {
			t.Errorf("at %d:\n%s", w, text)
		}
	}
	// Without the service, it says so instead of claiming all is well.
	text := ansi.Strip(motdText(m.th, env, m.snap, daemon.Status{}, false, false, now, 80))
	if !strings.Contains(text, "Background checks are off") {
		t.Errorf("no service:\n%s", text)
	}
	// A stale status is called out.
	st.Checked = now.Add(-2 * time.Hour)
	if text := ansi.Strip(motdText(m.th, env, m.snap, st, true, true, now, 80)); !strings.Contains(text, "may have stopped") {
		t.Errorf("stale:\n%s", text)
	}
}

func TestSettingsSaveToTheFile(t *testing.T) {
	m := testModel(t)
	m.env.Paths.Config = t.TempDir()
	path := m.env.Paths.ConfigFile()
	m.switchTab(tabSettings)

	// A choice saves straight away and applies.
	m.openItem(tabSettings, "set:theme")
	var flexoki health.Fix
	for _, f := range m.detailFor(m.modal.key, 40).fixes {
		if f.Label == "flexoki" {
			flexoki = f
		}
	}
	m.chooseFix(flexoki)
	if m.th.name != "flexoki" || m.env.Config.Theme != "flexoki" {
		t.Fatalf("theme not applied: %s", m.th.name)
	}

	// A typed value is checked first.
	m.openItem(tabSettings, "set:notify.ntfy_url")
	m.chooseFix(m.detailFor(m.modal.key, 40).fixes[0])
	if m.modal.stage != stageInput {
		t.Fatal("no input")
	}
	m.modal.input.SetValue("ntfy.sh/topic")
	m.submitInput()
	if m.modal == nil || m.modal.err == "" {
		t.Fatal("a bad URL was accepted")
	}
	m.modal.input.SetValue("https://ntfy.sh/a-long-secret-topic")
	m.submitInput()
	if m.env.Config.Notify.NtfyURL != "https://ntfy.sh/a-long-secret-topic" {
		t.Fatalf("not saved: %+v", m.env.Config.Notify)
	}
	// Numbers keep their type, and out-of-range ones are refused.
	m.openItem(tabSettings, "set:check_seconds")
	m.chooseFix(m.detailFor(m.modal.key, 40).fixes[0])
	m.modal.input.SetValue("2")
	m.submitInput()
	if m.modal == nil || m.modal.err == "" {
		t.Fatal("2 seconds was accepted")
	}
	m.modal.input.SetValue("60")
	m.submitInput()
	if m.env.Config.CheckSeconds != 60 {
		t.Fatalf("check_seconds %d", m.env.Config.CheckSeconds)
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{`theme = "flexoki"`, "check_seconds = 60\n", `ntfy_url = "https://ntfy.sh/a-long-secret-topic"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("file lacks %q:\n%s", want, data)
		}
	}
	// Clearing removes it from the file.
	m.openItem(tabSettings, "set:notify.ntfy_url")
	fixes := m.detailFor(m.modal.key, 40).fixes
	if fixes[len(fixes)-1].Special != "config:unset" {
		t.Fatalf("no clear: %+v", fixes)
	}
	m.chooseFix(fixes[len(fixes)-1])
	if data, _ := os.ReadFile(path); strings.Contains(string(data), "ntfy_url") {
		t.Errorf("not cleared:\n%s", data)
	}
}

func TestSecretsAreMasked(t *testing.T) {
	m := testModel(t)
	m.env.Config.Notify.TelegramToken = "123456:ABCDEFGHIJKLMNOP"
	m.width, m.height = 60, 80
	m.switchTab(tabSettings)
	page := ansi.Strip(m.render())
	if strings.Contains(page, "ABCDEFGH") || !strings.Contains(page, "••••MNOP") {
		t.Errorf("token shown:\n%s", page)
	}
	m.openItem(tabSettings, "set:notify.telegram_token")
	f := m.detailFor(m.modal.key, 40).fixes[0]
	if f.Input == nil || !f.Input.Secret || f.Input.Value != "" {
		t.Errorf("input %+v", f.Input)
	}
}

func TestDiskExplorer(t *testing.T) {
	m := testModel(t)
	m.du = map[string]duEntry{"/var": {at: time.Now(), u: priv.DirUsage{Path: "/var", Size: 12 << 30, Files: 8192, Children: []priv.DirSize{
		{Path: "/var/lib", Size: 11 << 30}, {Path: "/var/log", Size: 75 << 20}, {Path: "/var/mail", Size: 4096},
	}}}}
	d := m.detailFor("du:/var", 40)
	var labels []string
	for _, f := range d.fixes {
		labels = append(labels, strings.TrimSpace(f.Label)+"="+f.Special)
	}
	got := strings.Join(labels, " ")
	if !strings.Contains(got, "lib/=explore:/var/lib") || !strings.Contains(got, "log/=explore:/var/log") || strings.Contains(got, "mail") || !strings.Contains(got, "‹ /=explore:/") {
		t.Errorf("explorer buttons: %s", got)
	}
	// Each folder is drawn with its share of this one: a size, a
	// percentage and a bar that long.
	body := ansi.Strip(strings.Join(d.body, "\n"))
	if !strings.Contains(body, "1 smaller") {
		t.Errorf("small folders not summed:\n%s", body)
	}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "lib/") {
			continue
		}
		bar := strings.Count(line, m.th.look.fill)
		if !strings.Contains(line, "11G") || !strings.Contains(line, "92%") || bar < 10 || strings.Count(line, m.th.look.rest) != 1 {
			t.Errorf("lib/ should be most of the bar: %q", line)
		}
	}
	// Docker's folder points at Docker's clean-up instead of deleting.
	m.du["/var/lib/docker"] = duEntry{at: time.Now(), u: priv.DirUsage{Path: "/var/lib/docker", Size: 5 << 30}}
	if d := m.detailFor("du:/var/lib/docker", 40); d.fixes[0].Special != "open:prune" {
		t.Errorf("docker folder fixes %+v", d.fixes)
	}
	// Without admin access it says so rather than hanging.
	m.du = nil
	m.runFix(health.Fix{Special: "explore:/home"})
	if m.modal == nil || !strings.Contains(ansi.Strip(strings.Join(m.detailFor(m.modal.key, 40).body, " ")), "admin access") {
		t.Error("locked explorer")
	}
}

func TestCleanUpOffersOnlySafeThings(t *testing.T) {
	m := testModel(t)
	m.clean = cleanState{at: time.Now(), c: priv.Cleanable{Journal: 2 << 30, AptCache: 300 << 20, Autoremove: []string{"linux-image-6.1.0-old", "libfoo1"}, AutoremoveSize: 400 << 20}}
	d := m.detailFor("clean", 40)
	byLabel := map[string]health.Fix{}
	for _, f := range d.fixes {
		byLabel[f.Label] = f
	}
	if f := byLabel["Trim the system log"]; f.Op != "journal.vacuum" || !f.Danger {
		t.Errorf("journal %+v", f)
	}
	if f := byLabel["Clear downloaded packages"]; f.Op != "apt.clean" {
		t.Errorf("apt clean %+v", f)
	}
	if f := byLabel["Remove 2 unneeded packages"]; f.Op != "apt.autoremove" || !f.Danger || f.Args["expect"] != "linux-image-6.1.0-old libfoo1" || !strings.Contains(f.Confirm, "libfoo1") {
		t.Errorf("autoremove %+v", f)
	}
	// Small amounts aren't worth offering.
	m.clean.c = priv.Cleanable{Journal: 50 << 20, AptCache: 1 << 20}
	for _, f := range m.detailFor("clean", 40).fixes {
		if f.Op != "" {
			t.Errorf("offered %s for almost nothing", f.Label)
		}
	}
}

func TestHistoryCharts(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 45, 60
	m.openItem(tabOverview, "disk:/")
	body := ansi.Strip(strings.Join(m.detailFor(m.modal.key, 41).body, "\n"))
	for _, want := range []string{"Last 24 hours", "Last 7 days", "low 70% · avg", "high 80%", "7 days ago"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q:\n%s", want, body)
		}
	}
	if testing.Verbose() {
		t.Log("\n" + ansi.Strip(m.render()))
		m.openItem(tabOverview, "history:net")
		t.Log("\n" + ansi.Strip(m.render()))
	}
	m.hist = histState{at: time.Now()}
	if body := ansi.Strip(strings.Join(m.detailFor("result:memory", 41).body, " ")); !strings.Contains(body, "No history yet") {
		t.Errorf("empty history:\n%s", body)
	}
}

func TestContainerNetworkSpeed(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 200, 60
	at := time.Now()
	m.dock.trackNet([]priv.Stats{{Name: "web", NetRx: 1000, NetTx: 500, HaveNet: true}}, at)
	if m.netSpeed("web") != "" {
		t.Fatal("one reading can't give a speed")
	}
	m.dock.trackNet([]priv.Stats{{Name: "web", NetRx: 1000 + 5*2048, NetTx: 500 + 5*100, HaveNet: true}}, at.Add(5*time.Second))
	if got := m.netSpeed("web"); got != "↓2.0K/s ↑100B/s" {
		t.Fatalf("speed %q", got)
	}
	st := m.dock.stats["web"]
	st.HaveNet, st.NetRx, st.NetTx = true, 1<<20, 2<<20
	m.dock.stats["web"] = st
	body := ansi.Strip(strings.Join(m.detailFor("ctr:web", 60).body, "\n"))
	if !strings.Contains(body, "↓ 2.0K/s  ↑ 100B/s") || !strings.Contains(body, "↓ 1.0M  ↑ 2.0M") {
		t.Errorf("detail:\n%s", body)
	}
	// A restarted container starts counting again from zero.
	m.dock.trackNet([]priv.Stats{{Name: "web", NetRx: 10, NetTx: 10, HaveNet: true}}, at.Add(10*time.Second))
	if m.netSpeed("web") != "" {
		t.Error("counts went down, but a speed was shown")
	}
}

func TestBackups(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 45, 70
	created := m.now.Add(-26 * time.Hour)
	dir := m.backupDir()
	plainFile, lockedFile := dir+"/site-20261109-112005.tar.gz", dir+"/site-20261108-112005.tar.gz.age"
	_, public, _ := backup.NewKey()
	m.bk = backupsState{at: time.Now(), b: priv.Backups{Dir: dir, Free: 20 << 30, List: []priv.BackupInfo{
		{Manifest: backup.Manifest{Project: "site", Created: created, Folder: "/home/debian/apps/site", Volumes: []string{"site_db"}, Stopped: true}, Path: plainFile, Size: 12 << 20},
		{Manifest: backup.Manifest{Project: "site", Created: created.Add(-24 * time.Hour), Folder: "/home/debian/apps/site", Recipient: public}, Path: lockedFile, Size: 11 << 20},
	}, Aside: []priv.SetAside{{Project: "site", At: m.now.Add(-time.Hour), Paths: []string{"/home/debian/apps/site.before-restore-20261110-122005"}, Size: 3 << 20}}}}
	for i := range m.facts.Containers {
		if m.facts.Containers[i].Project == "site" {
			m.facts.Containers[i].WorkingDir = "/home/debian/apps/site"
			m.facts.Containers[i].Mounts = []priv.Mount{{Type: "volume", Name: "site_db"}, {Type: "bind", Source: "/srv/uploads"}}
		}
	}

	d := m.detailFor("backup:site", 41)
	body := strings.Join(strings.Fields(ansi.Strip(strings.Join(d.body, " "))), " ")
	for _, want := range []string{"site_db", "/srv/uploads", "Encryption", "earlier data aside", "keeps the last 5"} {
		if !strings.Contains(body, want) {
			t.Errorf("backup detail: missing %q:\n%s", want, body)
		}
	}
	var labels []string
	for _, f := range d.fixes {
		labels = append(labels, f.Label)
	}
	if !strings.HasPrefix(strings.Join(labels, ","), "Back up now,Back up without stopping,") || !strings.Contains(strings.Join(labels, ","), "encrypted") {
		t.Fatalf("fixes %v", labels)
	}
	if !d.fixes[0].Danger || d.fixes[0].Args["stop"] != "1" || d.fixes[1].Args["stop"] != "" {
		t.Error("stopping the app has to be confirmed")
	}
	for _, f := range d.fixes {
		if f.Op == "backup.discard" && !f.Danger {
			t.Error("deleting the set-aside data has to be confirmed")
		}
	}

	// An encrypted backup without its key here asks for it, then confirms.
	m.modal = &modal{key: "bfile:" + lockedFile}
	d = m.detailFor(m.modal.key, 41)
	restore := d.fixes[0]
	if restore.Op != "backup.restore" || restore.Input == nil || !restore.Input.Secret || !restore.Danger {
		t.Fatalf("restore %+v", restore)
	}
	m.chooseFix(restore)
	secret, _, _ := backup.NewKey()
	m.modal.input.SetValue(secret[:20] + " " + secret[20:])
	m.submitInput()
	if m.modal.stage != stageConfirm || m.modal.focus != 1 || m.modal.fix.Args["identity"] == "" {
		t.Fatalf("after the key: stage %v focus %d", m.modal.stage, m.modal.focus)
	}
	if redact(m.modal.fix.Args)["identity"] != "(hidden)" {
		t.Error("the secret key would be logged")
	}

	// With the key file here, no typing.
	m.env.Config.Backup.EncryptTo = public
	if cmd := m.runFix(health.Fix{Special: "backupkey:new"}); cmd == nil {
		t.Fatal("no key made")
	}
	if m.modal == nil || m.modal.key != "backupkey" {
		t.Fatal("the new key isn't shown")
	}
	keyBody := ansi.Strip(strings.Join(m.detailFor("backupkey", 30).body, ""))
	if ids, err := backup.ParseIdentities(keyBody); err != nil || len(ids) != 1 {
		t.Errorf("key not shown:\n%s", keyBody)
	}
	newPublic := m.env.Config.Backup.EncryptTo
	if newPublic == public || !strings.HasPrefix(newPublic, "age1") {
		t.Fatalf("encrypt_to is %q", newPublic)
	}
	m.bk.b.List[1].Recipient = newPublic
	d = m.detailFor("bfile:"+lockedFile, 41)
	if d.fixes[0].Input != nil || d.fixes[0].Args["keyfile"] != m.env.Paths.BackupKey() {
		t.Errorf("restore with key file %+v", d.fixes[0])
	}
	if info, err := os.Stat(m.env.Paths.BackupKey()); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("key file %v %v", info, err)
	}
	if m.encryptionWord() != "on · key on this server" {
		t.Errorf("encryption %q", m.encryptionWord())
	}
	if testing.Verbose() {
		m.modal = &modal{key: "backup:site"}
		t.Log("\n" + ansi.Strip(m.render()))
		m.modal = &modal{key: "set:encryption"}
		t.Log("\n" + ansi.Strip(m.render()))
	}
}

func TestCronSaysWhetherItRuns(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 100, 60
	m.switchTab(tabServices)
	page := ansi.Strip(m.render())
	for _, want := range []string{"e2scrub_all_cron", "a-backup-script-with-a-long-name.sh", "cron.daily", "isn't running"} {
		if !strings.Contains(page, want) {
			t.Errorf("stopped cron: missing %q:\n%s", want, page)
		}
	}
	for i := range m.svc.units {
		if m.svc.units[i].Name == "cron.service" {
			m.svc.units[i].Active, m.svc.units[i].Sub = "active", "running"
		}
	}
	page = ansi.Strip(m.render())
	for _, want := range []string{"every Sunday at 03:30", "every 15 minutes"} {
		if !strings.Contains(page, want) {
			t.Errorf("running cron: missing %q:\n%s", want, page)
		}
	}
	m.openItem(tabServices, "cron:job:0")
	body := ansi.Strip(strings.Join(m.detailFor(m.modal.key, 60).body, "\n"))
	for _, want := range []string{"Sun 15 Nov 03:30", "/etc/cron.d/e2scrub_all:1", "root"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail: missing %q:\n%s", want, body)
		}
	}
	m.cron.tab.Installed = false
	if body := ansi.Strip(strings.Join(m.detailFor("cron:every:daily", 60).body, " ")); !strings.Contains(body, "isn't installed") || !strings.Contains(body, "man-db") {
		t.Errorf("no cron:\n%s", body)
	}
}

func TestNewImagesAreNewsNotProblems(t *testing.T) {
	m := testModel(t)
	var r health.Result
	for _, x := range m.results {
		if x.ID == "images" {
			r = x
		}
	}
	if r.Status != health.Info || !strings.Contains(strings.Join(r.Detail, " "), "nginx:1.27") {
		t.Fatalf("result %+v", r)
	}
	m.width, m.height = 60, 120
	m.switchTab(tabDocker)
	if page := ansi.Strip(m.render()); !strings.Contains(page, "new image") {
		t.Errorf("no mark on the project:\n%s", page)
	}
	if body := ansi.Strip(strings.Join(m.detailFor("proj:site", 50).body, " ")); !strings.Contains(body, "newer nginx:1.27") {
		t.Errorf("project detail: %s", body)
	}
}

func TestRestartAtATypedTimeConfirmsIt(t *testing.T) {
	m := testModel(t)
	m.openItem(tabOverview, "reboot")
	var later health.Fix
	for _, f := range m.detailFor(m.modal.key, 40).fixes {
		if f.Input != nil {
			later = f
		}
	}
	m.chooseFix(later)
	m.modal.input.SetValue("25:00")
	m.submitInput()
	if m.modal.stage != stageInput || m.modal.err == "" {
		t.Fatal("25:00 was accepted")
	}
	m.modal.input.SetValue("03:30")
	m.submitInput()
	if m.modal.stage != stageConfirm || !strings.Contains(m.modal.fix.Confirm, "03:30") || m.modal.fix.Args["at"] != "03:30" || m.modal.focus != 1 {
		t.Fatalf("stage %v confirm %q focus %d", m.modal.stage, m.modal.fix.Confirm, m.modal.focus)
	}
}

func TestSelectionCoversWrappedItem(t *testing.T) {
	th := newTheme()
	on, _, _ := strings.Cut(th.selected.Render("x"), "x")
	b := th.card("T", "", []string{"a", "b first", "b second", "c"}, 30, 0, 1, 3, true)
	for i, want := range []bool{false, true, true, false} {
		if got := strings.Contains(b.lines[i+1], on); got != want {
			t.Errorf("line %d highlighted = %v, want %v", i, got, want)
		}
		if w := lipgloss.Width(b.lines[i+1]); w != 30 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
}

// Docker's clean-up says what each button frees, and the one that frees
// the images no container uses asks first.
func TestPruneOffersUnusedImages(t *testing.T) {
	m := testModel(t)
	if d := m.detailFor("prune", 60); len(d.fixes) != 2 {
		t.Fatalf("with no unused images there's nothing more to offer: %+v", d.fixes)
	}
	newTabs(m)
	d := m.detailFor("prune", 60)
	if len(d.fixes) != 3 {
		t.Fatalf("fixes %+v", d.fixes)
	}
	safe, unused, deep := d.fixes[0], d.fixes[1], d.fixes[2]
	if safe.Danger || safe.Args["mode"] != "safe" {
		t.Errorf("clean up %+v", safe)
	}
	if !unused.Danger || unused.Args["mode"] != "unused" || !strings.Contains(unused.Confirm, "an-app-with-a-long-name:1.0.0-beta.7") {
		t.Errorf("unused %+v", unused)
	}
	if !deep.Danger || deep.Args["mode"] != "all" {
		t.Errorf("deep %+v", deep)
	}
	// The leftover layer and the build cache are what the safe one frees;
	// the layer has no name to list.
	body := strings.Join(strings.Fields(ansi.Strip(strings.Join(d.body, " "))), " ")
	if !strings.Contains(body, "about 2.4G now") || strings.Contains(body, "sha256:bb") {
		t.Errorf("body:\n%s", body)
	}
}

func TestNewTabsOpenTheirItems(t *testing.T) {
	m := testModel(t)
	newTabs(m)
	m.width, m.height = 130, 40
	for tab, want := range map[tabID][]string{
		tabStorage: {"vol:/", "du:/var", "clean", "clean:apt", "clean:unneeded", "prune:unused", "prune"},
		tabNetwork: {"history:net", "speedtest"},
		tabAgents:  {"agent:claude", "agent:codex"},
	} {
		m.switchTab(tab)
		m.render()
		ids := strings.Join(m.items(tab), " ")
		for _, id := range want {
			if !strings.Contains(" "+ids+" ", " "+id+" ") {
				t.Errorf("tab %s lacks %s: %s", tabs[tab].name, id, ids)
				continue
			}
			m.modal = nil
			m.openItem(tab, id)
			if m.modal == nil || m.detailFor(m.modal.key, 60).title == "Gone" {
				t.Errorf("%s on %s opens nothing", id, tabs[tab].name)
			}
		}
	}
	// A limit that's nearly used up is the color of a problem.
	if c := m.th.limitColor(93); !sameColor(c, m.th.danger) {
		t.Error("93% of a limit should be red")
	}
	// The Claude limits are only asked for when the setting is on.
	if a, _ := m.agent("claude"); !strings.Contains(a.LimitsNote, "can ask Anthropic") {
		t.Errorf("note %q", a.LimitsNote)
	}
	m.env.Config.Agents.ClaudeLimits = true
	m.agents.claude = claudeLimits{at: time.Now(), plan: "max", limits: []agents.Window{{Name: "Week", Used: 40}}}
	if a, _ := m.agent("claude"); len(a.Limits) != 1 || a.Plan != "max" || a.LimitsNote != "" {
		t.Errorf("agent %+v", a)
	}
}

// A card that is one thing is selected by its border, so what it shows
// stays readable; a row in a list gets the band, which stops short of a
// bar at its end.
func TestSelectionDoesNotCoverCharts(t *testing.T) {
	m := testModel(t)
	newTabs(m)
	m.width, m.height = 130, 40
	band, _, _ := strings.Cut(m.th.selected.Render("x"), "x")
	edge, _, _ := strings.Cut(m.th.accentText.Render("x"), "x")
	for tab, id := range map[tabID]string{tabStorage: "vol:/", tabNetwork: "history:net", tabAgents: "agent:codex"} {
		m.switchTab(tab)
		m.lists[tab].pick(id, m.items(tab))
		frame := m.render()
		if strings.Contains(frame, band) {
			t.Errorf("%s: selecting %s painted a band over it", tabs[tab].name, id)
		}
		if !strings.Contains(frame, edge+m.th.look.tl) {
			t.Errorf("%s: selecting %s should light its card's border", tabs[tab].name, id)
		}
	}
	// A folder's row: the band covers its name and numbers, not its bar.
	m.switchTab(tabStorage)
	m.lists[tabStorage].pick("du:/var", m.items(tabStorage))
	for _, line := range strings.Split(m.render(), "\n") {
		if i := strings.Index(line, band); i >= 0 {
			rest := line[i:]
			end := strings.Index(rest, "\x1b[m")
			if strings.Contains(rest[:end], m.th.look.fill) || !strings.Contains(rest[end:], m.th.look.fill) {
				t.Errorf("the bar should come after the band, in its own color: %q", line)
			}
			return
		}
	}
	t.Error("the selected folder has no band")
}

func TestServicesOnAgentsTab(t *testing.T) {
	m := testModel(t)
	newTabs(m)
	m.width, m.height = 130, 50
	m.switchTab(tabAgents)
	// With no keys, the tab offers to add each service.
	ids := strings.Join(m.items(tabAgents), " ")
	if !strings.Contains(ids, "set:agents.openrouter_key") || !strings.Contains(ids, "set:agents.deepseek_key") {
		t.Fatalf("items %s", ids)
	}
	m.openItem(tabAgents, "set:agents.openrouter_key")
	if f := m.detailFor(m.modal.key, 60).fixes; len(f) == 0 || f[0].Input == nil || !f[0].Input.Secret {
		t.Fatalf("adding a key should ask for it, hidden: %+v", f)
	}
	m.modal = nil
	services(m)
	ids = strings.Join(m.items(tabAgents), " ")
	if strings.Contains(ids, "set:agents") || !strings.Contains(ids, "service:openrouter") || !strings.Contains(ids, "service:deepseek") {
		t.Fatalf("items with keys: %s", ids)
	}
	page := ansi.Strip(m.render())
	for _, want := range []string{"$74.75 left of $100.50", "$4.25 left of $50.00", "starts over monthly", "$0.0042", "¥110.00", "Nothing left"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// The key itself is never drawn.
	if strings.Contains(page, "sk-or-v1-test") || strings.Contains(page, "sk-test") {
		t.Error("a key is on the page")
	}
	d := m.detailFor("service:openrouter", 60)
	if body := ansi.Strip(strings.Join(d.body, "\n")); !strings.Contains(body, "$25.75") || !strings.Contains(body, "12 of 1000 requests") || strings.Contains(body, "sk-or-v1-test") {
		t.Errorf("detail:\n%s", body)
	}
}

func TestChartPeakLeavesHeadroom(t *testing.T) {
	for name := range palettes {
		th := themeNamed(name, groundDark)
		bars := th.look.bars
		rows := th.chart([]float64{1, 5, 10}, 3, 3, 10, func(float64) colorT { return th.cpu })
		top := []rune(ansi.Strip(rows[0]))
		if top[len(top)-1] == bars[len(bars)-1] {
			t.Errorf("%s: the peak fills its top cell %q and would touch the line above", name, rows[0])
		}
		if top[len(top)-1] == ' ' {
			t.Errorf("%s: the peak doesn't reach the top row", name)
		}
	}
}
