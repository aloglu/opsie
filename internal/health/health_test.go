package health

import (
	"github.com/aloglu/opsie/internal/sites"
	"strings"
	"testing"
	"time"

	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/facts"
	"github.com/aloglu/opsie/internal/metrics"
	"github.com/aloglu/opsie/internal/priv"
)

func find(results []Result, id string) (Result, bool) {
	for _, r := range results {
		if r.ID == id {
			return r, true
		}
	}
	return Result{}, false
}

func baseFacts() facts.Facts {
	return facts.Facts{
		Admin:    facts.Admin{Available: true},
		Time:     facts.TimeSync{Known: true, NTP: true, Synced: true, Timezone: "UTC"},
		SSHD:     map[string]string{"passwordauthentication": "no", "permitrootlogin": "prohibit-password"},
		Firewall: priv.Firewall{Tool: "ufw", Active: true, Rules: 3},
		Rules:    priv.RuleSet{Installed: true, Active: true},
		Apt:      priv.AptState{AutoInstalled: true, AutoOn: true},
		User:     "debian",
	}
}

func eval(f facts.Facts, s metrics.Snapshot) []Result {
	return Evaluate(Readings{Snap: s, HaveSnap: true, Facts: f, HaveFacts: true, Thresholds: config.Default().Thresholds})
}

func TestHealthyServerHasNoIssues(t *testing.T) {
	s := metrics.Snapshot{Cores: 2, Mem: metrics.Memory{Percent: 40}, Disks: []metrics.Disk{{Mount: "/", Percent: 40}}}
	crit, warn := Issues(eval(baseFacts(), s))
	if crit+warn != 0 {
		t.Fatalf("got %d problems and %d warnings on a healthy server", crit, warn)
	}
}

func TestResultsAreWorstFirst(t *testing.T) {
	f := baseFacts()
	f.FailedUnits = []string{"x.service"}
	f.RebootRequired = true
	results := eval(f, metrics.Snapshot{Cores: 1})
	for i := 1; i < len(results); i++ {
		if results[i].Status > results[i-1].Status {
			t.Fatalf("%s (%v) comes after %s (%v)", results[i].ID, results[i].Status, results[i-1].ID, results[i-1].Status)
		}
	}
	if results[0].ID != "unit:x.service" {
		t.Fatalf("first result is %s, want the failed unit", results[0].ID)
	}
}

func TestRebootFixIsGuarded(t *testing.T) {
	f := baseFacts()
	f.RebootRequired = true
	r, ok := find(eval(f, metrics.Snapshot{}), "reboot")
	if !ok || r.Status != Warn || len(r.Fixes) != 3 {
		t.Fatalf("reboot result: %+v", r)
	}
	if fix := r.Fixes[0]; !fix.Danger || fix.Countdown == 0 || fix.Confirm == "" {
		t.Fatalf("rebooting must confirm and count down: %+v", fix)
	}
	for _, fix := range r.Fixes {
		if !fix.Danger || fix.Confirm == "" {
			t.Errorf("%s must confirm: %+v", fix.Label, fix)
		}
	}
	// A restart that's set can be cancelled, and says when.
	f.Scheduled = priv.Scheduled{At: f.At.Add(3 * time.Hour), Mode: "reboot"}
	r, _ = find(eval(f, metrics.Snapshot{}), "reboot")
	if r.Status != Info || !strings.HasPrefix(r.Summary, "set for ") || r.Fixes[0].Op != "system.reboot-cancel" {
		t.Errorf("scheduled: %+v", r)
	}
}

func TestDiskThresholds(t *testing.T) {
	s := metrics.Snapshot{Disks: []metrics.Disk{
		{Mount: "/", Percent: 96},
		{Mount: "/data", Percent: 88},
		{Mount: "/tmp2", Percent: 10, InodePercent: 95},
	}}
	results := eval(baseFacts(), s)
	want := map[string]Status{"disk:/": Crit, "disk:/data": Warn, "disk:/tmp2": Warn}
	for id, status := range want {
		if r, _ := find(results, id); r.Status != status {
			t.Errorf("%s: %v, want %v", id, r.Status, status)
		}
	}
}

func TestContainerProblems(t *testing.T) {
	f := baseFacts()
	f.Containers = []priv.Container{
		{Name: "ok", State: "running", Health: "healthy"},
		{Name: "sick", State: "running", Health: "unhealthy"},
		{Name: "loop", State: "restarting"},
		{Name: "crashed", State: "exited", Exit: 137},
		{Name: "init", State: "exited", Exit: 0},
		{Name: "flaky", State: "running", Restarts: 5},
	}
	results := eval(f, metrics.Snapshot{})
	for id, status := range map[string]Status{"ctr:sick": Crit, "ctr:loop": Crit, "ctr:crashed": Warn, "ctr:flaky": Warn} {
		if r, ok := find(results, id); !ok || r.Status != status {
			t.Errorf("%s: %+v, want %v", id, r, status)
		}
	}
	if _, ok := find(results, "ctr:init"); ok {
		t.Error("a container that exited cleanly isn't a problem")
	}
	if r, _ := find(results, "docker"); r.Summary != "3 running · 3 stopped" {
		t.Errorf("summary %q", r.Summary)
	}
}

func TestSSHWarnings(t *testing.T) {
	f := baseFacts()
	f.SSHD = map[string]string{"passwordauthentication": "yes", "permitrootlogin": "yes"}
	results := eval(f, metrics.Snapshot{})
	for _, id := range []string{"ssh:passwords", "ssh:root"} {
		if r, ok := find(results, id); !ok || r.Status != Warn {
			t.Errorf("%s missing or not a warning", id)
		}
	}
	if _, ok := find(results, "ssh"); ok {
		t.Error("shouldn't also say SSH is fine")
	}
}

func TestLockedAdminOffersUnlock(t *testing.T) {
	f := baseFacts()
	f.Admin = facts.Admin{NeedsPassword: true, Err: "sudo needs your password"}
	r, ok := find(eval(f, metrics.Snapshot{}), "admin")
	if !ok || len(r.Fixes) == 0 || r.Fixes[0].Special != "unlock" {
		t.Fatalf("admin result: %+v", r)
	}
}

func TestClockOffOffersFix(t *testing.T) {
	f := baseFacts()
	f.Time.NTP = false
	r, _ := find(eval(f, metrics.Snapshot{}), "clock")
	if r.Status != Warn || len(r.Fixes) == 0 || r.Fixes[0].Op != "time.ntp-on" {
		t.Fatalf("clock result: %+v", r)
	}
}

func TestSecurityUpdatesWarn(t *testing.T) {
	f := baseFacts()
	f.Apt.Packages = []priv.Package{{Name: "libssl3t64", Security: true}, {Name: "curl"}}
	r, ok := find(eval(f, metrics.Snapshot{Cores: 1}), "updates")
	if !ok || r.Status != Warn || r.Fixes[0].Op != "apt.upgrade" || !r.Fixes[0].Danger {
		t.Fatalf("got %+v", r)
	}
	f.Apt.Packages = f.Apt.Packages[1:]
	if r, _ := find(eval(f, metrics.Snapshot{Cores: 1}), "updates"); r.Status != Info {
		t.Fatalf("non-security updates: %+v", r)
	}
	f.Apt.AutoOn = false
	if r, ok := find(eval(f, metrics.Snapshot{Cores: 1}), "updates:auto"); !ok || r.Fixes[0].Op != "apt.auto-on" {
		t.Fatalf("auto: %+v", r)
	}
}

func TestPasswordFixNeedsAKey(t *testing.T) {
	f := baseFacts()
	f.SSHD["passwordauthentication"] = "yes"
	r, _ := find(eval(f, metrics.Snapshot{Cores: 1}), "ssh:passwords")
	if len(r.Fixes) != 0 {
		t.Fatalf("offered to turn passwords off with no key: %+v", r.Fixes)
	}
	f.Keys = []priv.KeySet{{User: "debian", Keys: []priv.AuthorizedKey{{Type: "ssh-ed25519"}}}}
	r, _ = find(eval(f, metrics.Snapshot{Cores: 1}), "ssh:passwords")
	if len(r.Fixes) != 1 || r.Fixes[0].Args["enabled"] != "no" || !r.Fixes[0].Danger {
		t.Fatalf("got %+v", r.Fixes)
	}
}

func TestFailedLoginsWarnOnlyWithPasswords(t *testing.T) {
	f := baseFacts()
	f.Failures = priv.Failures{Total: 500, IPs: 40}
	if r, _ := find(eval(f, metrics.Snapshot{Cores: 1}), "logins"); r.Status != Info {
		t.Fatalf("keys only: %+v", r)
	}
	f.SSHD["passwordauthentication"] = "yes"
	if r, _ := find(eval(f, metrics.Snapshot{Cores: 1}), "logins"); r.Status != Warn {
		t.Fatalf("passwords on: %+v", r)
	}
}

func TestDockerPortsBypassFirewall(t *testing.T) {
	f := baseFacts()
	f.Rules.Active = true
	f.Containers = []priv.Container{
		{Name: "game-server", State: "running", Project: "game", Service: "game", ConfigFiles: "/x/compose.yaml", Ports: []string{"203.0.113.50:27015 → 27015/udp"}},
		{Name: "db", State: "running", Project: "app", Service: "db", ConfigFiles: "/y/compose.yaml", Ports: []string{"*:5432 → 5432/tcp", "*:5432 → 5432/tcp"}},
		{Name: "web", State: "running", Ports: []string{"127.0.0.1:8040 → 3000/tcp", "10.0.0.5:9000 → 9000/tcp"}},
		{Name: "old", State: "exited", Ports: []string{"*:80 → 80/tcp"}},
	}
	rs := eval(f, metrics.Snapshot{Cores: 1})
	game, ok := find(rs, "ports:game-server")
	if !ok || game.Status != Warn || game.Summary != "27015/udp" || !strings.Contains(strings.Join(game.Detail, " "), "ufw") {
		t.Fatalf("game %+v", game)
	}
	if db, _ := find(rs, "ports:db"); db.Status != Crit || db.Summary != "5432/tcp" {
		t.Errorf("database %+v", db)
	}
	for _, id := range []string{"ports:web", "ports:old"} {
		if r, ok := find(rs, id); ok {
			t.Errorf("%s shouldn't warn: %+v", id, r)
		}
	}
	var bind, meant Fix
	for _, fx := range game.Fixes {
		switch {
		case fx.Op == "compose.bindlocal":
			bind = fx
		case fx.Special == "config:add":
			meant = fx
		}
	}
	if !bind.Danger || bind.Args["port"] != "27015" || bind.Args["proto"] != "udp" || meant.Args["value"] != "game-server:27015/udp" {
		t.Fatalf("fixes %+v / %+v", bind, meant)
	}
	// Once marked, it's a note, not a warning.
	rs = Evaluate(Readings{Facts: f, HaveFacts: true, Snap: metrics.Snapshot{Cores: 1}, HaveSnap: true, PublicPorts: []string{"game-server:27015/udp"}})
	if _, ok := find(rs, "ports:game-server"); ok {
		t.Error("still warned after it was marked public")
	}
	if r, ok := find(rs, "ports:public"); !ok || r.Status != Info {
		t.Errorf("public note %+v", r)
	}
}

func TestSudoFixNeedsAPassword(t *testing.T) {
	f := baseFacts()
	f.PasswordlessSudo = true
	if r, _ := find(eval(f, metrics.Snapshot{Cores: 1}), "sudo"); len(r.Fixes) != 0 {
		t.Fatalf("offered with no password: %+v", r.Fixes)
	}
	f.Sudo.HasPassword = true
	if r, _ := find(eval(f, metrics.Snapshot{Cores: 1}), "sudo"); len(r.Fixes) != 1 || r.Fixes[0].Args["enabled"] != "no" {
		t.Fatalf("got %+v", r.Fixes)
	}
}

func TestWatcherStates(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	get := func(w Watcher) Result {
		f := baseFacts()
		f.At = now
		r, _ := find(Evaluate(Readings{Facts: f, HaveFacts: true, Thresholds: config.Default().Thresholds, Watcher: w, HaveWatcher: true}), "watcher")
		return r
	}
	labels := func(r Result) string {
		var out []string
		for _, f := range r.Fixes {
			out = append(out, f.Label)
		}
		return strings.Join(out, ",")
	}
	w := Watcher{Mine: "v1.0.0", ConfigPath: "/home/debian/.config/opsie/config.toml"}
	if r := get(w); r.Status != Info || labels(r) != "Turn on" || r.Fixes[0].Args["config"] != w.ConfigPath {
		t.Fatalf("off: %+v", r)
	}
	w.Installed, w.HaveStatus, w.Every, w.Checked = true, true, 30, now.Add(-10*time.Minute)
	if r := get(w); r.Status != Warn || labels(r) != "Restart,Logs" {
		t.Fatalf("stale: %+v", r)
	}
	w.Checked, w.Version = now.Add(-10*time.Second), "v0.9.0"
	if r := get(w); r.Status != Info || labels(r) != "Set up alerts,Update them,Show summary at login,Logs,Turn off" {
		t.Fatalf("no targets, old version: %s %+v", labels(r), r)
	}
	w.Motd = true
	if r := get(w); !strings.Contains(labels(r), "Hide summary at login") {
		t.Fatalf("motd on: %s", labels(r))
	}
	w.Motd = false
	w.Version, w.Notify.NtfyURL = "v1.0.0", "https://ntfy.sh/x"
	if r := get(w); r.Status != OK || labels(r) != "Send a test alert,Show summary at login,Logs,Turn off" || r.Summary != "on · alerts by ntfy" {
		t.Fatalf("on: %s %+v", labels(r), r)
	}
	w.SendError = "401 Unauthorized"
	if r := get(w); r.Status != Warn || r.Summary != "alerts failing" {
		t.Fatalf("failing: %+v", r)
	}
	dev := Watcher{Dev: true}
	if r := get(dev); r.Status != Info || len(r.Fixes) != 0 {
		t.Fatalf("dev: %+v", r)
	}
}

func TestSitesCheck(t *testing.T) {
	f := facts.Facts{Sites: []sites.Result{
		{URL: "https://a.example.com", Host: "a.example.com", Route: true, Status: 200, Took: 100 * time.Millisecond},
		{URL: "https://b.example.com", Host: "b.example.com", Status: 200, Took: 100 * time.Millisecond},
	}}
	cfg := config.Sites{Check: true, SlowSeconds: 5}
	if rs := sitesCheck(f, cfg); len(rs) != 1 || rs[0].ID != "sites" || rs[0].Status != OK {
		t.Fatalf("all fine: %+v", rs)
	}
	f.Sites[0].Status = 502
	f.Sites[1].Took = 8 * time.Second
	rs := sitesCheck(f, cfg)
	if len(rs) != 2 || rs[0].Status != Crit || rs[0].ID != "site:https://a.example.com" || rs[1].Status != Warn || !strings.Contains(rs[1].Summary, "slow") {
		t.Fatalf("problems: %+v", rs)
	}
	if rs[0].Fixes[0].Special != "open:docker:route:a.example.com" {
		t.Errorf("fix %+v", rs[0].Fixes)
	}
	f.Sites[1].Took, f.Sites[1].CertExpires = time.Second, time.Now().Add(3*24*time.Hour)
	if rs := sitesCheck(f, cfg); !strings.Contains(rs[1].Summary, "certificate") {
		t.Errorf("cert: %+v", rs[1])
	}
	if rs := sitesCheck(facts.Facts{}, cfg); rs != nil {
		t.Error("no sites should say nothing")
	}
}

func TestContainerLogs(t *testing.T) {
	f := baseFacts()
	f.HaveLogDefault = true
	f.Containers = []priv.Container{
		{Name: "chatty", State: "running", LogDriver: "json-file", LogPath: "/var/lib/docker/containers/a/a-json.log", LogSize: 2 << 30},
		{Name: "quiet", State: "running", LogDriver: "json-file", LogMaxSize: "10m", LogPath: "/x", LogSize: 1 << 20},
	}
	r, ok := find(eval(f, metrics.Snapshot{Cores: 1}), "docker-logs")
	if !ok || r.Status != Warn || !strings.Contains(strings.Join(r.Detail, " "), "chatty") || strings.Contains(strings.Join(r.Detail, " "), "quiet,") {
		t.Fatalf("got %+v", r)
	}
	var limit, empty bool
	for _, fx := range r.Fixes {
		limit = limit || fx.Op == "docker.log-limit"
		empty = empty || (fx.Op == "docker.log-truncate" && fx.Danger && fx.Args["container"] == "chatty")
	}
	if !limit || !empty {
		t.Errorf("fixes %+v", r.Fixes)
	}
	// Small, unlimited logs are a note; all limited and small is nothing.
	f.Containers[0].LogSize = 5 << 20
	if r, _ := find(eval(f, metrics.Snapshot{Cores: 1}), "docker-logs"); r.Status != Info {
		t.Errorf("small: %+v", r)
	}
	// Saved but Docker hasn't restarted: offer the restart, asking first.
	f.LogDefault.Limited, f.LogDefault.Pending = true, true
	r, _ = find(eval(f, metrics.Snapshot{Cores: 1}), "docker-logs")
	if !hasFix(r, "unit.restart", true) || hasFix(r, "docker.log-limit", false) {
		t.Errorf("pending: %+v", r.Fixes)
	}
	// Docker uses it: recreate the compose apps still without a limit.
	f.LogDefault.Pending = false
	f.Containers[0].Project, f.Containers[0].ConfigFiles = "blog", "/home/x/apps/blog/compose.yaml"
	r, _ = find(eval(f, metrics.Snapshot{Cores: 1}), "docker-logs")
	if !hasFix(r, "compose.update", true) || hasFix(r, "unit.restart", true) {
		t.Errorf("recreate: %+v", r.Fixes)
	}
	for _, fx := range r.Fixes {
		if fx.Op == "compose.update" && (fx.Args["mode"] != "recreate" || fx.Args["project"] != "blog") {
			t.Errorf("recreate args: %+v", fx)
		}
	}
	f.Containers[0].LogMaxSize = "10m"
	if _, ok := find(eval(f, metrics.Snapshot{Cores: 1}), "docker-logs"); ok {
		t.Error("warned with every log limited")
	}
}

func TestNightlyBackups(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	n := Nightly{Apps: []string{"a", "b"}, At: "03:30", Running: true}
	get := func(n Nightly) Result {
		rs := nightlyBackups(n, now)
		if len(rs) != 1 {
			t.Fatalf("got %d results", len(rs))
		}
		return rs[0]
	}
	if r := get(n); r.Status != Info {
		t.Errorf("before the first: %+v", r)
	}
	n.Runs = []BackupRun{{Project: "a", At: now.Add(-8 * time.Hour), OK: true, Opened: true}, {Project: "b", At: now.Add(-8 * time.Hour), OK: true}}
	if r := get(n); r.Status != OK {
		t.Errorf("all good: %+v", r)
	}
	n.Runs[1] = BackupRun{Project: "b", At: now.Add(-8 * time.Hour), Err: "disk full"}
	if r := get(n); r.Status != Crit || !strings.Contains(strings.Join(r.Detail, " "), "disk full") {
		t.Errorf("failed: %+v", r)
	}
	n.Runs[1] = BackupRun{Project: "b", At: now.Add(-50 * time.Hour), OK: true}
	if r := get(n); r.Status != Warn {
		t.Errorf("missed: %+v", r)
	}
	n.Running = false
	if r := get(n); r.Status != Warn || r.Summary != "not running" {
		t.Errorf("service off: %+v", r)
	}
	if rs := nightlyBackups(Nightly{}, now); len(rs) != 0 {
		t.Error("a result with nothing scheduled")
	}
}

func hasFix(r Result, op string, danger bool) bool {
	for _, fx := range r.Fixes {
		if fx.Op == op && fx.Danger == danger {
			return true
		}
	}
	return false
}
