package priv

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// --- types ---

// AuthorizedKey is one line of an authorized_keys file.
type AuthorizedKey struct {
	Type        string `json:"type"`
	Comment     string `json:"comment,omitempty"`
	Fingerprint string `json:"fingerprint"`
	Options     bool   `json:"options,omitempty"`
}

// KeySet is one account's authorized keys.
type KeySet struct {
	User string          `json:"user"`
	File string          `json:"file"`
	Keys []AuthorizedKey `json:"keys"`
}

// Failures sums up SSH login attempts that didn't get in.
type Failures struct {
	Total    int       `json:"total"`
	IPs      int       `json:"ips"`
	Since    time.Time `json:"since"`
	Top      []IPCount `json:"top,omitempty"`
	Users    []string  `json:"users,omitempty"` // most-tried usernames
	Accepted []Login   `json:"accepted,omitempty"`
}

// IPCount is how many failed attempts came from an address.
type IPCount struct {
	IP    string    `json:"ip"`
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

// Login is a successful SSH login.
type Login struct {
	At          time.Time `json:"at"`
	User        string    `json:"user"`
	From        string    `json:"from"`
	Method      string    `json:"method"`
	Fingerprint string    `json:"fingerprint,omitempty"`
}

// Fail2ban is fail2ban's state.
type Fail2ban struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Jails     []Jail `json:"jails,omitempty"`
}

// Jail is one fail2ban jail.
type Jail struct {
	Name   string   `json:"name"`
	Banned []string `json:"banned,omitempty"`
	Total  int      `json:"total"`
}

// Listener is a port something listens on.
type Listener struct {
	Proto   string   `json:"proto"`
	Port    int      `json:"port"`
	Addrs   []string `json:"addrs"`
	Process string   `json:"process,omitempty"`
	// Public means reachable from other machines, not just this one.
	Public bool `json:"public"`
	// Docker means Docker publishes it, which bypasses ufw.
	Docker bool `json:"docker,omitempty"`
}

// Rule is one ufw rule.
type Rule struct {
	Num    int    `json:"num"`
	To     string `json:"to"`
	Action string `json:"action"`
	From   string `json:"from"`
	V6     bool   `json:"v6,omitempty"`
}

// Text is the rule the way ufw prints it, which is how rules are named
// when deleting them.
func (r Rule) Text() string {
	return strings.Join(strings.Fields(r.To+" "+r.Action+" "+r.From), " ")
}

// SudoState is how sudo treats an account.
type SudoState struct {
	// Sources are the files granting passwordless sudo.
	Sources     []string `json:"sources,omitempty"`
	Override    bool     `json:"override,omitempty"` // opsie's require-password file is in place
	HasPassword bool     `json:"has_password"`
}

// --- parsing ---

var keyTypes = map[string]bool{
	"ssh-ed25519": true, "ssh-rsa": true, "ssh-dss": true,
	"ecdsa-sha2-nistp256": true, "ecdsa-sha2-nistp384": true, "ecdsa-sha2-nistp521": true,
	"sk-ssh-ed25519@openssh.com": true, "sk-ecdsa-sha2-nistp256@openssh.com": true,
}

// ParseKey reads one authorized_keys line. ok is false for blank lines,
// comments and anything that isn't a key.
func ParseKey(line string) (AuthorizedKey, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return AuthorizedKey{}, false
	}
	fields := strings.Fields(line)
	for i, f := range fields {
		if !keyTypes[f] || i+1 >= len(fields) {
			continue
		}
		blob, err := base64.StdEncoding.DecodeString(fields[i+1])
		if err != nil || len(blob) < 16 {
			return AuthorizedKey{}, false
		}
		sum := sha256.Sum256(blob)
		return AuthorizedKey{
			Type:        f,
			Comment:     strings.Join(fields[i+2:], " "),
			Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]),
			Options:     i > 0,
		}, true
	}
	return AuthorizedKey{}, false
}

// ParseKeys reads an authorized_keys file.
func ParseKeys(content string) []AuthorizedKey {
	var keys []AuthorizedKey
	for _, line := range strings.Split(content, "\n") {
		if k, ok := ParseKey(line); ok {
			keys = append(keys, k)
		}
	}
	return keys
}

var (
	failRes = []*regexp.Regexp{
		regexp.MustCompile(`Invalid user (\S*) from (\S+)`),
		regexp.MustCompile(`Failed \S+ for (?:invalid user )?(\S+) from (\S+)`),
		regexp.MustCompile(`Connection (?:closed|reset) by authenticating user (\S+) (\S+) port`),
		regexp.MustCompile(`Disconnected from authenticating user (\S+) (\S+) port`),
	}
	acceptRe = regexp.MustCompile(`Accepted (\S+) for (\S+) from (\S+) port \d+ [^:\s]+(?:: \S+ (SHA256:\S+))?`)
)

// ParseAuthLog reads `journalctl -o short-unix` lines from sshd.
func ParseAuthLog(out string) Failures {
	f := Failures{}
	byIP := map[string]*IPCount{}
	users := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		stamp, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		secs, err := strconv.ParseFloat(stamp, 64)
		if err != nil {
			continue
		}
		at := time.Unix(int64(secs), 0)
		if f.Since.IsZero() {
			f.Since = at
		}
		if m := acceptRe.FindStringSubmatch(rest); m != nil {
			f.Accepted = append(f.Accepted, Login{At: at, Method: m[1], User: m[2], From: m[3], Fingerprint: m[4]})
			continue
		}
		for _, re := range failRes {
			m := re.FindStringSubmatch(rest)
			if m == nil {
				continue
			}
			f.Total++
			users[m[1]]++
			c := byIP[m[2]]
			if c == nil {
				c = &IPCount{IP: m[2]}
				byIP[m[2]] = c
			}
			c.Count++
			c.Last = at
			break
		}
	}
	f.IPs = len(byIP)
	for _, c := range byIP {
		f.Top = append(f.Top, *c)
	}
	sort.Slice(f.Top, func(i, j int) bool {
		if f.Top[i].Count != f.Top[j].Count {
			return f.Top[i].Count > f.Top[j].Count
		}
		return f.Top[i].IP < f.Top[j].IP
	})
	if len(f.Top) > 8 {
		f.Top = f.Top[:8]
	}
	for u := range users {
		f.Users = append(f.Users, u)
	}
	sort.Slice(f.Users, func(i, j int) bool {
		if users[f.Users[i]] != users[f.Users[j]] {
			return users[f.Users[i]] > users[f.Users[j]]
		}
		return f.Users[i] < f.Users[j]
	})
	if len(f.Users) > 5 {
		f.Users = f.Users[:5]
	}
	if len(f.Accepted) > 10 {
		f.Accepted = f.Accepted[len(f.Accepted)-10:]
	}
	return f
}

var processRe = regexp.MustCompile(`\(\("([^"]+)"`)

// ParseListeners reads `ss -tulpnH` output, merging IPv4 and IPv6
// listeners on the same port.
func ParseListeners(out string) []Listener {
	type key struct {
		proto string
		port  int
		proc  string
	}
	merged := map[key]*Listener{}
	var order []key
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		proto, local := f[0], f[4]
		i := strings.LastIndex(local, ":")
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(local[i+1:])
		if err != nil {
			continue
		}
		addr := strings.Trim(local[:i], "[]")
		if j := strings.Index(addr, "%"); j >= 0 {
			addr = addr[:j]
		}
		proc := ""
		if m := processRe.FindStringSubmatch(line); m != nil {
			proc = m[1]
		}
		// DHCP clients listen on port 68 of the public address to renew
		// the lease; that's not a service.
		if proto == "udp" && port == 68 {
			continue
		}
		k := key{proto, port, proc}
		l := merged[k]
		if l == nil {
			l = &Listener{Proto: proto, Port: port, Process: proc, Docker: proc == "docker-proxy"}
			merged[k] = l
			order = append(order, k)
		}
		l.Addrs = append(l.Addrs, addr)
		if ip := net.ParseIP(addr); addr == "*" || ip == nil || !(ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
			l.Public = true
		}
	}
	out2 := make([]Listener, 0, len(order))
	for _, k := range order {
		out2 = append(out2, *merged[k])
	}
	sort.SliceStable(out2, func(i, j int) bool {
		if out2[i].Public != out2[j].Public {
			return out2[i].Public
		}
		return out2[i].Port < out2[j].Port
	})
	return out2
}

var ruleRe = regexp.MustCompile(`^\[\s*(\d+)\]\s+(.+?)\s{2,}(ALLOW|DENY|REJECT|LIMIT)( IN| OUT| FWD)?\s+(.+?)\s*$`)

// ParseRules reads `ufw status numbered` output.
func ParseRules(out string) []Rule {
	var rules []Rule
	for _, line := range strings.Split(out, "\n") {
		m := ruleRe.FindStringSubmatch(strings.TrimRight(line, " "))
		if m == nil {
			continue
		}
		num, _ := strconv.Atoi(m[1])
		r := Rule{Num: num, To: strings.TrimSpace(m[2]), Action: strings.TrimSpace(m[3] + m[4]), From: strings.TrimSpace(m[5])}
		r.V6 = strings.Contains(r.To, "(v6)") || strings.Contains(r.From, "(v6)")
		rules = append(rules, r)
	}
	return rules
}

// AllowsPort reports whether rules let traffic in on port/proto.
func AllowsPort(rules []Rule, port int, proto string) bool {
	for _, r := range rules {
		if !strings.HasPrefix(r.Action, "ALLOW") && !strings.HasPrefix(r.Action, "LIMIT") {
			continue
		}
		to := strings.TrimSpace(strings.TrimSuffix(r.To, "(v6)"))
		if to == "Anywhere" {
			return true
		}
		p, pr, _ := strings.Cut(to, "/")
		if pr != "" && pr != proto {
			continue
		}
		if lo, hi, isRange := strings.Cut(p, ":"); isRange {
			l, _ := strconv.Atoi(lo)
			h, _ := strconv.Atoi(hi)
			if port >= l && port <= h {
				return true
			}
		} else if n, err := strconv.Atoi(p); err == nil && n == port {
			return true
		}
	}
	return false
}

// --- reading ops ---

type account struct {
	name     string
	uid, gid int
	home     string
}

// accounts lists root and every account that can log in.
func accounts() ([]account, error) {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []account
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) < 7 {
			continue
		}
		uid, _ := strconv.Atoi(fields[2])
		gid, _ := strconv.Atoi(fields[3])
		shell := fields[6]
		if uid != 0 && uid < 1000 || strings.HasSuffix(shell, "nologin") || strings.HasSuffix(shell, "false") {
			continue
		}
		out = append(out, account{name: fields[0], uid: uid, gid: gid, home: fields[5]})
	}
	return out, nil
}

func findAccount(name string) (account, error) {
	list, err := accounts()
	if err != nil {
		return account{}, err
	}
	for _, a := range list {
		if a.name == name {
			return a, nil
		}
	}
	return account{}, fmt.Errorf("there's no login account called %q", name)
}

func keysFile(a account) string { return filepath.Join(a.home, ".ssh", "authorized_keys") }

func opKeys(context.Context, map[string]string) (any, error) {
	list, err := accounts()
	if err != nil {
		return nil, err
	}
	sets := []KeySet{}
	for _, a := range list {
		data, err := os.ReadFile(keysFile(a))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
		keys := ParseKeys(string(data))
		if keys == nil {
			keys = []AuthorizedKey{}
		}
		sets = append(sets, KeySet{User: a.name, File: keysFile(a), Keys: keys})
	}
	return sets, nil
}

func sshJournal(ctx context.Context, since string) (string, error) {
	return output(ctx, "journalctl", "--unit", "ssh.service", "--unit", "sshd.service", "--since", since, "--output", "short-unix", "--no-pager", "--quiet")
}

func opFailures(ctx context.Context, _ map[string]string) (any, error) {
	out, err := sshJournal(ctx, "-24h")
	if err != nil {
		return nil, err
	}
	f := ParseAuthLog(out)
	if f.Since.IsZero() || time.Since(f.Since) > 24*time.Hour {
		f.Since = time.Now().Add(-24 * time.Hour)
	}
	return f, nil
}

// opSessionKey finds which key a login used, from sshd's log line for its
// address and port.
func opSessionKey(ctx context.Context, args map[string]string) (any, error) {
	user, ip, port := args["user"], args["ip"], args["port"]
	if net.ParseIP(ip) == nil {
		return nil, fmt.Errorf("%q isn't an address", ip)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return nil, fmt.Errorf("%q isn't a port", port)
	}
	out, err := sshJournal(ctx, "-30d")
	if err != nil {
		return nil, err
	}
	needle := fmt.Sprintf("for %s from %s port %s ", user, ip, port)
	fp := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Accepted publickey "+needle) {
			if m := acceptRe.FindStringSubmatch(line); m != nil {
				fp = m[4]
			}
		}
	}
	return fp, nil
}

func opFail2ban(ctx context.Context, _ map[string]string) (any, error) {
	f := Fail2ban{}
	if _, err := lookPath("fail2ban-client"); err != nil {
		return f, nil
	}
	f.Installed = true
	out, err := output(ctx, "fail2ban-client", "status")
	if err != nil {
		return f, nil // installed but not running
	}
	f.Running = true
	for _, line := range strings.Split(out, "\n") {
		_, list, ok := strings.Cut(line, "Jail list:")
		if !ok {
			continue
		}
		for _, name := range strings.Split(list, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			j := Jail{Name: name}
			if status, err := output(ctx, "fail2ban-client", "status", name); err == nil {
				for _, l := range strings.Split(status, "\n") {
					if _, v, ok := strings.Cut(l, "Total banned:"); ok {
						j.Total, _ = strconv.Atoi(strings.TrimSpace(v))
					}
					if _, v, ok := strings.Cut(l, "Banned IP list:"); ok {
						j.Banned = strings.Fields(v)
					}
				}
			}
			f.Jails = append(f.Jails, j)
		}
	}
	return f, nil
}

func opListen(ctx context.Context, _ map[string]string) (any, error) {
	out, err := output(ctx, "ss", "-tulpnH")
	if err != nil {
		return nil, err
	}
	return ParseListeners(out), nil
}

// sudoersFiles are the files sudo reads, in order.
func sudoersFiles() []string {
	files := []string{"/etc/sudoers"}
	entries, _ := os.ReadDir("/etc/sudoers.d")
	for _, e := range entries {
		// sudo skips names with a dot or ending in ~.
		if n := e.Name(); !e.IsDir() && !strings.Contains(n, ".") && !strings.HasSuffix(n, "~") {
			files = append(files, filepath.Join("/etc/sudoers.d", n))
		}
	}
	return files
}

func overrideFile(user string) string { return "/etc/sudoers.d/99-zz-opsie-" + user }
func nopasswdFile(user string) string { return "/etc/sudoers.d/90-opsie-nopasswd-" + user }

func opSudo(ctx context.Context, args map[string]string) (any, error) {
	user := args["user"]
	a, err := findAccount(user)
	if err != nil {
		return nil, err
	}
	s := SudoState{}
	for _, file := range sudoersFiles() {
		if file == overrideFile(a.name) {
			s.Override = true
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") || !strings.Contains(line, "NOPASSWD") {
				continue
			}
			if who := strings.Fields(line)[0]; who == a.name || strings.HasPrefix(who, "%") {
				s.Sources = append(s.Sources, file)
				break
			}
		}
	}
	if out, err := output(ctx, "passwd", "--status", a.name); err == nil {
		if f := strings.Fields(out); len(f) > 1 {
			s.HasPassword = f[1] == "P"
		}
	}
	return s, nil
}

// --- changing ops ---

// sshdDropIn is opsie's sshd settings file. sshd uses the first value it
// reads for most settings, and "0-" sorts before the usual "00-" to
// "99-" files, so these win.
const sshdDropIn = "/etc/ssh/sshd_config.d/0-opsie.conf"

// setSSHD writes settings to opsie's drop-in, checks the result with
// `sshd -t`, reloads sshd, and confirms sshd now uses them. Any failure
// puts the old file back.
func setSSHD(ctx context.Context, settings map[string]string) error {
	old, readErr := os.ReadFile(sshdDropIn)
	current := map[string]string{}
	var order []string
	for _, line := range strings.Split(string(old), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && !strings.HasPrefix(f[0], "#") {
			k := strings.ToLower(f[0])
			if _, dup := current[k]; !dup {
				order = append(order, k)
			}
			current[k] = f[1]
		}
	}
	for k, v := range settings {
		if _, ok := current[k]; !ok {
			order = append(order, k)
		}
		current[k] = v
	}
	names := map[string]string{
		"passwordauthentication": "PasswordAuthentication", "kbdinteractiveauthentication": "KbdInteractiveAuthentication",
		"permitrootlogin": "PermitRootLogin",
	}
	var b strings.Builder
	b.WriteString("# Managed by opsie. Files here are read in order and the first value\n# wins, so this file comes first. Change these from opsie, or delete the\n# file to go back to your other settings.\n")
	for _, k := range order {
		name := names[k]
		if name == "" {
			name = k
		}
		fmt.Fprintf(&b, "%s %s\n", name, current[k])
	}
	restore := func() {
		if readErr == nil {
			_ = writeFileAtomic(sshdDropIn, old, 0o644)
		} else {
			_ = os.Remove(sshdDropIn)
		}
	}
	if err := writeFileAtomic(sshdDropIn, []byte(b.String()), 0o644); err != nil {
		return err
	}
	if out, err := output(ctx, "sshd", "-t"); err != nil {
		restore()
		return fmt.Errorf("sshd rejected the change, so it was undone: %s", strings.TrimSpace(out+" "+err.Error()))
	}
	if err := run(ctx, "systemctl", "reload", sshService()); err != nil {
		restore()
		_ = run(ctx, "systemctl", "reload", sshService())
		return fmt.Errorf("couldn't reload sshd, so the change was undone: %w", err)
	}
	out, err := output(ctx, "sshd", "-T")
	if err != nil {
		return err
	}
	effective := ParseSSHD(out)
	for k, v := range settings {
		got := effective[k]
		if k == "permitrootlogin" && v == "prohibit-password" && got == "without-password" {
			continue // the same setting under its old name
		}
		if got != v {
			return fmt.Errorf("saved, but sshd still uses %s %s: a setting in /etc/ssh/sshd_config comes before its Include line", names[k], got)
		}
	}
	return nil
}

func sshService() string {
	if _, err := os.Stat("/lib/systemd/system/sshd.service"); err == nil {
		if _, err := os.Stat("/lib/systemd/system/ssh.service"); err != nil {
			return "sshd.service"
		}
	}
	return "ssh.service"
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func opSSHPasswords(ctx context.Context, args map[string]string) (any, error) {
	switch args["enabled"] {
	case "no":
		// Never lock the person doing this out.
		a, err := findAccount(caller(args))
		if err != nil {
			return nil, err
		}
		data, _ := os.ReadFile(keysFile(a))
		if len(ParseKeys(string(data))) == 0 {
			return nil, fmt.Errorf("%s has no SSH key, so turning off passwords would lock you out; add a key first", a.name)
		}
		return nil, setSSHD(ctx, map[string]string{"passwordauthentication": "no", "kbdinteractiveauthentication": "no"})
	case "yes":
		return nil, setSSHD(ctx, map[string]string{"passwordauthentication": "yes"})
	}
	return nil, errors.New("enabled must be yes or no")
}

func opSSHRoot(ctx context.Context, args map[string]string) (any, error) {
	v := args["value"]
	switch v {
	case "no", "prohibit-password", "yes":
	default:
		return nil, fmt.Errorf("%q isn't a root login setting", v)
	}
	if v == "no" && caller(args) == "root" {
		return nil, errors.New("you're logged in as root, so turning off root login would lock you out")
	}
	return nil, setSSHD(ctx, map[string]string{"permitrootlogin": v})
}

func opKeyAdd(_ context.Context, args map[string]string) (any, error) {
	a, err := findAccount(args["user"])
	if err != nil {
		return nil, err
	}
	line := strings.TrimSpace(args["key"])
	if strings.ContainsAny(line, "\n\r") {
		return nil, errors.New("paste a single public key line")
	}
	key, ok := ParseKey(line)
	if !ok {
		return nil, errors.New("that doesn't look like an SSH public key; it should start with ssh-ed25519, ssh-rsa or ecdsa-sha2-…")
	}
	if key.Options {
		return nil, errors.New("keys with options aren't supported here; edit authorized_keys for that")
	}
	dir := filepath.Join(a.home, ".ssh")
	if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink; not touching it", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Lchown(dir, a.uid, a.gid)
	path := keysFile(a)
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink; not touching it", path)
	}
	data, _ := os.ReadFile(path)
	for _, k := range ParseKeys(string(data)) {
		if k.Fingerprint == key.Fingerprint {
			return nil, fmt.Errorf("%s already has that key", a.name)
		}
	}
	content := string(data)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += line + "\n"
	if err := writeFileAtomic(path, []byte(content), 0o600); err != nil {
		return nil, err
	}
	return key.Fingerprint, os.Lchown(path, a.uid, a.gid)
}

func opKeyRemove(ctx context.Context, args map[string]string) (any, error) {
	a, err := findAccount(args["user"])
	if err != nil {
		return nil, err
	}
	fp := args["fingerprint"]
	if fp != "" && fp == args["session_key"] {
		return nil, errors.New("this is the key you're logged in with; remove it from another session")
	}
	path := keysFile(a)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var kept []string
	removed, remaining := 0, 0
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if k, ok := ParseKey(line); ok {
			if k.Fingerprint == fp {
				removed++
				continue
			}
			remaining++
		}
		kept = append(kept, line)
	}
	if removed == 0 {
		return nil, errors.New("that key isn't there anymore")
	}
	if remaining == 0 && a.name == caller(args) {
		if out, err := output(ctx, "sshd", "-T"); err == nil && ParseSSHD(out)["passwordauthentication"] != "yes" {
			return nil, errors.New("that's your last key and password login is off, so you'd be locked out")
		}
	}
	content := strings.Join(kept, "\n")
	if content != "" {
		content += "\n"
	}
	if err := writeFileAtomic(path, []byte(content), 0o600); err != nil {
		return nil, err
	}
	return nil, os.Lchown(path, a.uid, a.gid)
}

func opTerminate(ctx context.Context, args map[string]string) (any, error) {
	id := args["session"]
	if _, err := strconv.Atoi(id); err != nil {
		return nil, fmt.Errorf("%q isn't a session id", id)
	}
	if id == args["caller_session"] {
		return nil, errors.New("that's this session; just quit instead")
	}
	return nil, run(ctx, "loginctl", "terminate-session", id)
}

var portRe = regexp.MustCompile(`^(\d{1,5})(?::(\d{1,5}))?$`)

func validPort(spec, proto string) (string, error) {
	m := portRe.FindStringSubmatch(spec)
	if m == nil {
		return "", fmt.Errorf("%q isn't a port; use a number like 443, or a range like 6000:6010", spec)
	}
	for _, p := range m[1:] {
		if p == "" {
			continue
		}
		if n, _ := strconv.Atoi(p); n < 1 || n > 65535 {
			return "", fmt.Errorf("ports go from 1 to 65535")
		}
	}
	switch proto {
	case "tcp", "udp":
		return spec + "/" + proto, nil
	case "", "any":
		if m[2] != "" {
			return "", errors.New("a port range needs tcp or udp")
		}
		return spec, nil
	}
	return "", fmt.Errorf("%q isn't tcp or udp", proto)
}

// parsePortInput reads what someone typed: "443", "443/tcp", "6000:6010/udp".
func parsePortInput(s string) (string, string) {
	s = strings.ToLower(strings.TrimSpace(s))
	port, proto, _ := strings.Cut(s, "/")
	if f := strings.Fields(port); len(f) == 2 {
		port, proto = f[0], f[1]
	}
	return strings.TrimSpace(port), strings.TrimSpace(proto)
}

func opUFWAllow(ctx context.Context, args map[string]string) (any, error) {
	port, proto := parsePortInput(args["port"])
	if p := args["proto"]; p != "" {
		proto = p
	}
	spec, err := validPort(port, proto)
	if err != nil {
		return nil, err
	}
	return nil, run(ctx, "ufw", "allow", spec)
}

// sshPorts are the ports sshd listens on.
func sshPorts(ctx context.Context) []string {
	out, err := output(ctx, "sshd", "-T")
	if err != nil {
		return []string{"22"}
	}
	ports := strings.Fields(ParseSSHD(out)["port"])
	if len(ports) == 0 {
		return []string{"22"}
	}
	return ports
}

func opUFWDelete(ctx context.Context, args map[string]string) (any, error) {
	want := args["rule"]
	out, err := output(ctx, "ufw", "status", "numbered")
	if err != nil {
		return nil, err
	}
	rules := ParseRules(out)
	var target *Rule
	for i := range rules {
		if rules[i].Text() == want {
			target = &rules[i]
			break
		}
	}
	if target == nil {
		return nil, errors.New("that rule isn't there anymore")
	}
	if strings.Contains(out, "Status: active") {
		for _, p := range sshPorts(ctx) {
			n, _ := strconv.Atoi(p)
			if !AllowsPort([]Rule{*target}, n, "tcp") {
				continue
			}
			var others []Rule
			for _, r := range rules {
				if r.Num != target.Num && r.V6 == target.V6 {
					others = append(others, r)
				}
			}
			if !AllowsPort(others, n, "tcp") {
				return nil, fmt.Errorf("this rule is what lets SSH (port %s) in; deleting it would lock you out", p)
			}
		}
	}
	return nil, run(ctx, "ufw", "--force", "delete", strconv.Itoa(target.Num))
}

func opUFWDeny(ctx context.Context, args map[string]string) (any, error) {
	ip := strings.TrimSpace(args["ip"])
	if net.ParseIP(ip) == nil {
		if _, _, err := net.ParseCIDR(ip); err != nil {
			return nil, fmt.Errorf("%q isn't an address", ip)
		}
	}
	if ip == args["caller_ip"] {
		return nil, errors.New("that's the address you're connected from")
	}
	// prepend puts the block ahead of any allow rule; it fails when there
	// are no rules yet, and then order doesn't matter.
	if err := run(ctx, "ufw", "prepend", "deny", "from", ip); err != nil {
		return nil, run(ctx, "ufw", "deny", "from", ip)
	}
	return nil, nil
}

func opUFWEnable(ctx context.Context, _ map[string]string) (any, error) {
	if _, err := lookPath("ufw"); err != nil {
		return nil, err
	}
	// Let SSH in before anything else, so turning the firewall on can't
	// cut off this session.
	for _, p := range sshPorts(ctx) {
		if err := run(ctx, "ufw", "allow", p+"/tcp"); err != nil {
			return nil, err
		}
	}
	return nil, run(ctx, "ufw", "--force", "enable")
}

func opUFWDisable(ctx context.Context, _ map[string]string) (any, error) {
	return nil, run(ctx, "ufw", "disable")
}

// visudoInstall checks content with visudo before putting it in place.
func visudoInstall(ctx context.Context, path, content string) error {
	tmp, err := os.CreateTemp("/etc/sudoers.d", ".opsie-check-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if out, err := output(ctx, "visudo", "--check", "--file", tmp.Name()); err != nil {
		return fmt.Errorf("visudo rejected the change: %s", strings.TrimSpace(out+" "+err.Error()))
	}
	return writeFileAtomic(path, []byte(content), 0o440)
}

func opSudoPasswordless(ctx context.Context, args map[string]string) (any, error) {
	a, err := findAccount(args["user"])
	if err != nil {
		return nil, err
	}
	if a.uid == 0 {
		return nil, errors.New("root doesn't use sudo")
	}
	if strings.ContainsAny(a.name, ".~") {
		// sudo skips files in sudoers.d whose names have these.
		return nil, fmt.Errorf("opsie can't manage sudo for a name with a dot in it (%s); edit /etc/sudoers.d by hand", a.name)
	}
	stateAny, err := opSudo(ctx, map[string]string{"user": a.name})
	if err != nil {
		return nil, err
	}
	state := stateAny.(SudoState)
	switch args["enabled"] {
	case "no":
		if !state.HasPassword {
			return nil, fmt.Errorf("%s has no password, so sudo couldn't ask for one and you'd lose root; set one first with: passwd", a.name)
		}
		// sudo uses the last rule that matches, and this file sorts last.
		content := "# Managed by opsie: require a password for sudo. Delete this file to undo.\n" + a.name + " ALL=(ALL:ALL) ALL\n"
		return nil, visudoInstall(ctx, overrideFile(a.name), content)
	case "yes":
		_ = os.Remove(overrideFile(a.name))
		if len(state.Sources) == 0 {
			content := "# Managed by opsie: sudo without a password. Delete this file to undo.\n" + a.name + " ALL=(ALL:ALL) NOPASSWD: ALL\n"
			return nil, visudoInstall(ctx, nopasswdFile(a.name), content)
		}
		return nil, nil
	}
	return nil, errors.New("enabled must be yes or no")
}

func aptGet(ctx context.Context, args ...string) (string, error) {
	path, err := lookPath("apt-get")
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, path, append([]string{"-y", "-q",
		"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}, args...)...)
	// needrestart, if installed, only lists what needs restarting; opsie
	// shows that on the Updates tab.
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_MODE=l", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := string(out)
		if strings.Contains(text, "Could not get lock") || strings.Contains(text, "is held by process") {
			return text, errors.New("apt is busy, probably with automatic updates; try again in a few minutes")
		}
		return text, fmt.Errorf("apt-get %s failed: %s", args[0], lastLines(text, 2))
	}
	return string(out), nil
}

func opFail2banInstall(ctx context.Context, _ map[string]string) (any, error) {
	if _, err := aptGet(ctx, "install", "fail2ban"); err != nil {
		return nil, err
	}
	// Debian logs to the journal rather than auth.log, so make sure the
	// SSH jail reads from there.
	jail := "/etc/fail2ban/jail.d/opsie-sshd.local"
	if _, err := os.Stat(jail); errors.Is(err, os.ErrNotExist) {
		_ = writeFileAtomic(jail, []byte("# Added by opsie.\n[sshd]\nenabled = true\nbackend = systemd\n"), 0o644)
	}
	if err := run(ctx, "systemctl", "enable", "--now", "fail2ban.service"); err != nil {
		return nil, err
	}
	return nil, run(ctx, "systemctl", "restart", "fail2ban.service")
}

func opUFWInstall(ctx context.Context, _ map[string]string) (any, error) {
	_, err := aptGet(ctx, "install", "ufw")
	return nil, err
}

// RuleSet is ufw's state with its rules.
type RuleSet struct {
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`
	Default   string `json:"default,omitempty"`
	Rules     []Rule `json:"rules"`
}

func opRules(ctx context.Context, _ map[string]string) (any, error) {
	rs := RuleSet{Rules: []Rule{}}
	if _, err := lookPath("ufw"); err != nil {
		return rs, nil
	}
	rs.Installed = true
	verbose, err := output(ctx, "ufw", "status", "verbose")
	if err != nil {
		return nil, err
	}
	fw := ParseUFW(verbose)
	rs.Active, rs.Default = fw.Active, fw.Default
	if rs.Active {
		out, err := output(ctx, "ufw", "status", "numbered")
		if err != nil {
			return nil, err
		}
		if r := ParseRules(out); r != nil {
			rs.Rules = r
		}
	}
	return rs, nil
}

// caller is the account that started the helper. sudo says so in
// SUDO_USER; the request's word is only used when it doesn't.
func caller(args map[string]string) string {
	if u := os.Getenv("SUDO_USER"); u != "" {
		return u
	}
	if u := args["caller"]; u != "" {
		return u
	}
	return "root"
}
