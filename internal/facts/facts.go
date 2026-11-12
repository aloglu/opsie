// Package facts gathers the slower-changing state of the server that the
// health checks look at: pending reboots, failed services, SSH and firewall
// settings, containers, and so on.
package facts

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/priv"
	"github.com/aloglu/opsie/internal/registry"
	"github.com/aloglu/opsie/internal/sites"
)

// Facts is one gathering pass. Each *Err field explains why its value is
// missing; an empty error with an empty value means there's nothing there.
type Facts struct {
	At time.Time

	RebootRequired bool
	RebootPackages []string
	// Scheduled is a restart that's been set for later.
	Scheduled priv.Scheduled
	// LogDefault is the log limit Docker gives new containers.
	LogDefault     priv.DockerLogDefault
	HaveLogDefault bool

	FailedUnits    []string
	FailedUnitsErr string

	Time TimeSync

	// PasswordlessSudo is whether sudo runs without asking for a password.
	PasswordlessSudo bool

	Admin Admin

	SSHD    map[string]string
	SSHDErr string

	Firewall    priv.Firewall
	FirewallErr string

	Containers []priv.Container
	DockerErr  string
	NoDocker   bool

	Keys    []priv.KeySet
	KeysErr string
	// SessionKey is the fingerprint of the key this SSH session logged in
	// with, when it used one.
	SessionKey string

	Rules    priv.RuleSet
	RulesErr string

	Listen    []priv.Listener
	ListenErr string

	Failures    priv.Failures
	FailuresErr string

	Fail2ban    priv.Fail2ban
	Fail2banErr string

	Sudo    priv.SudoState
	SudoErr string

	Apt    priv.AptState
	AptErr string

	Stale    []priv.Stale
	StaleErr string

	// Sites are the public websites that were visited, when site checks
	// are on. They're checked by CheckSites, after gathering.
	Sites []sites.Result

	// Images are the images the containers run; ImageChecks what their
	// registries say about them, when image checks are on.
	Images      []priv.ImageInfo
	ImageChecks []registry.Check

	// Tunnels are the Cloudflare Tunnel connectors running here.
	Tunnels   []priv.Tunnel
	TunnelErr string

	// User is who's running opsie, and Session where they're connected
	// from, for refusing actions that would cut them off.
	User    string
	Session Session
}

// Session is how this opsie got here.
type Session struct {
	ID         string // logind session
	IP, Port   string // SSH client address
	ServerPort string
}

// Here reads the current session from the environment.
func Here() Session {
	s := Session{ID: os.Getenv("XDG_SESSION_ID")}
	if f := strings.Fields(os.Getenv("SSH_CONNECTION")); len(f) == 4 {
		s.IP, s.Port, s.ServerPort = f[0], f[1], f[3]
	}
	return s
}

// slow readings are costly or change rarely, so they're reused for a
// while instead of read on every pass.
var slow = struct {
	sync.Mutex
	at   map[string]time.Time
	data map[string][]byte
	errs map[string]string
}{at: map[string]time.Time{}, data: map[string][]byte{}, errs: map[string]string{}}

const slowEvery = 5 * time.Minute

// Invalidate drops the reused readings, after an action that may have
// changed them.
func Invalidate() {
	slow.Lock()
	defer slow.Unlock()
	slow.at = map[string]time.Time{}
}

func cachedCall(ctx context.Context, admin priv.Caller, op string, args map[string]string, out any) string {
	key := op
	slow.Lock()
	if at, ok := slow.at[key]; ok && time.Since(at) < slowEvery {
		data, errText := slow.data[key], slow.errs[key]
		slow.Unlock()
		if errText == "" {
			_ = json.Unmarshal(data, out)
		}
		return errText
	}
	slow.Unlock()
	errText := ""
	if err := admin.Call(ctx, op, args, out); err != nil {
		errText = err.Error()
	}
	slow.Lock()
	raw, _ := json.Marshal(out)
	slow.at[key], slow.data[key], slow.errs[key] = time.Now(), raw, errText
	slow.Unlock()
	return errText
}

var sessionKey struct {
	sync.Mutex
	done bool
	fp   string
}

// TimeSync is the clock's state from timedatectl.
type TimeSync struct {
	Known, NTP, Synced bool
	Timezone           string
}

// Admin is whether the privileged helper is running.
type Admin struct {
	Available     bool
	NeedsPassword bool
	Err           string
}

// errNoAdmin stands in for readings that need the helper when it isn't
// running.
const errNoAdmin = "needs admin access"

// Gather collects everything. admin may be nil, in which case the readings
// that need root are reported as unavailable.
func Gather(ctx context.Context, admin priv.Caller, adminErr error) Facts {
	return GatherFor(ctx, admin, adminErr, "")
}

// GatherFor gathers on behalf of an account, for the background service,
// which runs as root but reports on the person who set it up. An empty
// name means whoever runs this.
func GatherFor(ctx context.Context, admin priv.Caller, adminErr error, account string) Facts {
	f := Facts{At: time.Now(), Session: Here()}
	if u, err := user.Current(); err == nil {
		f.User = u.Username
	}
	if account != "" {
		f.User, f.Session = account, Session{}
	}
	switch {
	case admin != nil && admin.Alive():
		f.Admin.Available = true
	case errors.Is(adminErr, priv.ErrNeedsPassword):
		f.Admin.NeedsPassword = true
		f.Admin.Err = adminErr.Error()
	case adminErr != nil:
		f.Admin.Err = adminErr.Error()
	default:
		f.Admin.Err = "the admin helper isn't running"
	}

	var wg sync.WaitGroup
	do := func(fn func()) {
		wg.Add(1)
		go func() { defer wg.Done(); fn() }()
	}

	do(func() { f.RebootRequired, f.RebootPackages = rebootRequired() })
	do(func() { f.FailedUnits, f.FailedUnitsErr = failedUnits(ctx) })
	do(func() { f.Time = timeSync(ctx) })
	if account == "" {
		do(func() { f.PasswordlessSudo = passwordlessSudo(ctx) })
	}

	if f.Admin.Available {
		call := func(op string, out any) string {
			if err := admin.Call(ctx, op, map[string]string{"user": f.User}, out); err != nil {
				return err.Error()
			}
			return ""
		}
		do(func() { f.SSHDErr = call("sshd.config", &f.SSHD) })
		do(func() { f.FirewallErr = call("firewall.status", &f.Firewall) })
		do(func() {
			f.DockerErr = call("docker.list", &f.Containers)
			if f.DockerErr == priv.ErrNoDocker.Error() {
				f.DockerErr, f.NoDocker = "", true
			}
		})
		do(func() { f.KeysErr = call("ssh.keys", &f.Keys) })
		do(func() { f.RulesErr = call("firewall.rules", &f.Rules) })
		do(func() { f.ListenErr = call("net.listen", &f.Listen) })
		do(func() { f.SudoErr = call("sudo.status", &f.Sudo) })
		do(func() { f.Fail2banErr = call("fail2ban.status", &f.Fail2ban) })
		do(func() { f.AptErr = call("apt.status", &f.Apt) })
		do(func() { f.FailuresErr = cachedCall(ctx, admin, "auth.failures", nil, &f.Failures) })
		do(func() { f.StaleErr = cachedCall(ctx, admin, "restart.needed", nil, &f.Stale) })
		do(func() { f.TunnelErr = call("tunnel.status", &f.Tunnels) })
		do(func() { cachedCall(ctx, admin, "docker.images", nil, &f.Images) })
		do(func() { call("reboot.scheduled", &f.Scheduled) })
		do(func() { f.HaveLogDefault = call("docker.logdefault", &f.LogDefault) == "" })
		do(func() { f.SessionKey = findSessionKey(ctx, admin, f.User, f.Session) })
	} else {
		for _, e := range []*string{&f.SSHDErr, &f.FirewallErr, &f.DockerErr, &f.KeysErr, &f.RulesErr, &f.ListenErr,
			&f.SudoErr, &f.Fail2banErr, &f.AptErr, &f.FailuresErr, &f.StaleErr, &f.TunnelErr} {
			*e = errNoAdmin
		}
	}
	wg.Wait()
	if account != "" && f.SudoErr == "" {
		// Root can't ask sudo on someone's behalf; read the rules instead.
		f.PasswordlessSudo = len(f.Sudo.Sources) > 0 && !f.Sudo.Override
	}
	return f
}

// findSessionKey asks sshd's log which key this session used. It never
// changes, so it's looked up once.
func findSessionKey(ctx context.Context, admin priv.Caller, user string, s Session) string {
	sessionKey.Lock()
	defer sessionKey.Unlock()
	if sessionKey.done || s.IP == "" {
		return sessionKey.fp
	}
	var fp string
	if err := admin.Call(ctx, "ssh.sessionkey", map[string]string{"user": user, "ip": s.IP, "port": s.Port}, &fp); err == nil {
		sessionKey.fp, sessionKey.done = fp, true
	}
	return sessionKey.fp
}

func rebootRequired() (bool, []string) {
	if _, err := os.Stat("/var/run/reboot-required"); err != nil {
		return false, nil
	}
	var pkgs []string
	if data, err := os.ReadFile("/var/run/reboot-required.pkgs"); err == nil {
		seen := map[string]bool{}
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" && !seen[line] {
				seen[line] = true
				pkgs = append(pkgs, line)
			}
		}
	}
	return true, pkgs
}

func failedUnits(ctx context.Context) ([]string, string) {
	out, err := exec.CommandContext(ctx, "systemctl", "list-units", "--failed", "--plain", "--no-legend", "--all").Output()
	if err != nil {
		return nil, "couldn't ask systemd: " + err.Error()
	}
	var units []string
	for _, line := range strings.Split(string(out), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			units = append(units, strings.TrimPrefix(fields[0], "●"))
		}
	}
	return units, ""
}

func timeSync(ctx context.Context) TimeSync {
	out, err := exec.CommandContext(ctx, "timedatectl", "show").Output()
	if err != nil {
		return TimeSync{Timezone: localZone()}
	}
	t := TimeSync{Known: true}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		key, value, _ := strings.Cut(scanner.Text(), "=")
		switch key {
		case "Timezone":
			t.Timezone = value
		case "NTP":
			t.NTP = value == "yes"
		case "NTPSynchronized":
			t.Synced = value == "yes"
		}
	}
	if t.Timezone == "" {
		t.Timezone = localZone()
	}
	return t
}

func localZone() string {
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, zone, ok := strings.Cut(target, "zoneinfo/"); ok {
			return zone
		}
	}
	return time.Now().Format("MST")
}

// passwordlessSudo asks sudo to run `true` without a password, ignoring any
// cached credentials (-k), so a recent `sudo` doesn't count.
func passwordlessSudo(ctx context.Context) bool {
	if _, err := exec.LookPath("sudo"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sudo", "-k", "-n", "true")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	return cmd.Run() == nil
}

// CheckOnline does the checks that reach outside the server, when the
// settings say to: visiting the public sites and asking registries about
// newer images. They run after gathering because they need its results.
func (f *Facts) CheckOnline(ctx context.Context, cfg config.Config) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		f.Sites = nil
		if cfg.Sites.Check {
			f.Sites = sites.Check(ctx, sites.Targets(f.Tunnels, cfg.Sites.Extra))
		}
	}()
	go func() {
		defer wg.Done()
		f.ImageChecks = nil
		if cfg.Images.Check {
			f.ImageChecks = registry.Checks(ctx, f.Images)
		}
	}()
	wg.Wait()
}
