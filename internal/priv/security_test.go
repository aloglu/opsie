package priv

import (
	"context"
	"os"
	"os/user"
	"strconv"
	"strings"
	"testing"
)

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGthEC3exNQSmVGJF5mrLIKNsnnnFtMLZKfIOeAzHOfD"

func TestParseKey(t *testing.T) {
	k, ok := ParseKey(testKey + " me@laptop")
	if !ok || k.Type != "ssh-ed25519" || k.Comment != "me@laptop" || k.Options {
		t.Fatalf("got %+v %v", k, ok)
	}
	// Same as ssh-keygen -lf.
	if k.Fingerprint != "SHA256:24zRnbKRJ19wQ5mHB7jiO1axVccfHWsToUojIeLv2j0" {
		t.Fatalf("fingerprint %s", k.Fingerprint)
	}
	if k, ok := ParseKey(`from="10.0.0.1",no-pty ` + testKey); !ok || !k.Options {
		t.Fatalf("options: %+v %v", k, ok)
	}
	for _, bad := range []string{"", "# ssh-ed25519 AAAA", "ssh-ed25519 notbase64!!", "hello world"} {
		if _, ok := ParseKey(bad); ok {
			t.Errorf("%q parsed as a key", bad)
		}
	}
	if n := len(ParseKeys("# keys\n" + testKey + "\n\n" + testKey + " two\n")); n != 2 {
		t.Fatalf("ParseKeys found %d", n)
	}
}

func TestParseAuthLog(t *testing.T) {
	log := `1794235192.344232 harbor sshd-session[1]: Connection reset by authenticating user root 192.0.2.69 port 57806 [preauth]
1794235615.465184 harbor sshd-session[2]: Connection closed by authenticating user root 192.0.2.157 port 29906 [preauth]
1794235803.590461 harbor sshd-session[3]: Invalid user drayton from 192.0.2.112 port 46442
1794235803.628598 harbor sshd-session[3]: Connection closed by invalid user drayton 192.0.2.112 port 46442 [preauth]
1794235878.100810 harbor sshd-session[4]: Invalid user admin from 192.0.2.112 port 16974
1794235900.000000 harbor sshd-session[5]: Accepted publickey for debian from 198.51.100.7 port 50532 ssh2: ED25519 SHA256:24zRnbKRJ19wQ5mHB7jiO1axVccfHWsToUojIeLv2j0`
	f := ParseAuthLog(log)
	if f.Total != 4 {
		t.Fatalf("total %d", f.Total)
	}
	if f.Top[0].IP != "192.0.2.112" || f.Top[0].Count != 2 {
		t.Fatalf("top %+v", f.Top)
	}
	if f.Users[0] != "root" {
		t.Fatalf("users %v", f.Users)
	}
	if len(f.Accepted) != 1 || f.Accepted[0].User != "debian" || f.Accepted[0].Fingerprint != "SHA256:24zRnbKRJ19wQ5mHB7jiO1axVccfHWsToUojIeLv2j0" {
		t.Fatalf("accepted %+v", f.Accepted)
	}
}

func TestParseListeners(t *testing.T) {
	out := `udp UNCONN 0      0              127.0.0.54:53    0.0.0.0:* users:(("systemd-resolve",pid=340,fd=20))
udp UNCONN 0      0      203.0.113.50%ens3:68    0.0.0.0:* users:(("systemd-network",pid=660,fd=31))
udp UNCONN 0      0           203.0.113.50:27015 0.0.0.0:* users:(("docker-proxy",pid=1493908,fd=8))
tcp LISTEN 0      128               0.0.0.0:22    0.0.0.0:* users:(("sshd",pid=734,fd=6))
tcp LISTEN 0      4096            127.0.0.1:8040  0.0.0.0:* users:(("docker-proxy",pid=1910809,fd=8))
tcp LISTEN 0      128                  [::]:22       [::]:* users:(("sshd",pid=734,fd=7))`
	ls := ParseListeners(out)
	if len(ls) != 4 {
		t.Fatalf("got %+v", ls)
	}
	if ls[0].Port != 22 || !ls[0].Public || len(ls[0].Addrs) != 2 || ls[0].Process != "sshd" {
		t.Fatalf("ssh %+v", ls[0])
	}
	if ls[1].Port != 27015 || !ls[1].Docker || !ls[1].Public {
		t.Fatalf("docker %+v", ls[1])
	}
	if ls[3].Port != 8040 || ls[3].Public {
		t.Fatalf("local %+v", ls[3])
	}
}

func TestParseRules(t *testing.T) {
	out := `Status: active

     To                         Action      From
     --                         ------      ----
[ 1] 22/tcp                     ALLOW IN    Anywhere                  
[ 2] Anywhere                   DENY IN     1.2.3.4                   
[ 3] 6000:6010/udp              ALLOW IN    Anywhere                  
[ 4] 22/tcp (v6)                ALLOW IN    Anywhere (v6)             
`
	rules := ParseRules(out)
	if len(rules) != 4 {
		t.Fatalf("got %+v", rules)
	}
	if rules[0].Text() != "22/tcp ALLOW IN Anywhere" || rules[1].Action != "DENY IN" || !rules[3].V6 {
		t.Fatalf("got %+v", rules)
	}
	if !AllowsPort(rules[:1], 22, "tcp") || AllowsPort(rules[1:3], 22, "tcp") || !AllowsPort(rules, 6005, "udp") || AllowsPort(rules, 6005, "tcp") {
		t.Fatal("AllowsPort")
	}
}

func TestValidPort(t *testing.T) {
	for in, want := range map[string]string{"443": "443", "443/tcp": "443/tcp", "6000:6010/udp": "6000:6010/udp", "53 udp": "53/udp"} {
		port, proto := parsePortInput(in)
		got, err := validPort(port, proto)
		if err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"0", "70000", "6000:6010", "22; rm", "ssh", "80/icmp"} {
		port, proto := parsePortInput(in)
		if _, err := validPort(port, proto); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestParseUpgradable(t *testing.T) {
	out := `Listing...
libssl3t64/stable-security 3.5.4-1~deb13u2 amd64 [upgradable from: 3.5.4-1~deb13u1]
curl/stable,stable 8.14.1-2+deb13u3 amd64 [upgradable from: 8.14.1-2+deb13u2]
`
	p := ParseUpgradable(out)
	if len(p) != 2 || p[0].Name != "libssl3t64" || !p[0].Security || p[0].From != "3.5.4-1~deb13u1" || p[1].Security || p[1].Suite != "stable" {
		t.Fatalf("got %+v", p)
	}
}

func TestParseAutoUpgrades(t *testing.T) {
	if !ParseAutoUpgrades("APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"1\";") {
		t.Fatal("on")
	}
	if ParseAutoUpgrades("APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"0\";") {
		t.Fatal("off")
	}
}

func TestIsLibrary(t *testing.T) {
	for p, want := range map[string]bool{
		"/usr/lib/x86_64-linux-gnu/libz.so.1.3.1": true,
		"/SYSV00000000": false,
		"/dev/zero":     false,
		"/tmp/foo.so":   false,
	} {
		if IsLibrary(p) != want {
			t.Errorf("%s", p)
		}
	}
}

func TestJournalText(t *testing.T) {
	out := `{"MESSAGE":"Started cron.","__REALTIME_TIMESTAMP":"1794291957261137"}
{"MESSAGE":[104,105,255],"__REALTIME_TIMESTAMP":"1794291957261138"}
{"MESSAGE":"two\nlines","__REALTIME_TIMESTAMP":"1794291957261139"}
not json`
	got := JournalText([]byte(out))
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 4 || !strings.HasSuffix(lines[0], " Started cron.") || !strings.HasSuffix(lines[1], " hi�") || !strings.HasSuffix(lines[3], " lines") {
		t.Fatalf("%q", got)
	}
	if !strings.HasPrefix(lines[0], "2026-") {
		t.Fatalf("stamp %q", lines[0])
	}
}

func TestVital(t *testing.T) {
	if !Vital("ssh.service") || !Vital("user@1000.service") || Vital("cron.service") {
		t.Fatal("vital")
	}
}

func TestTargetRefusesOwnAncestors(t *testing.T) {
	data, _ := os.ReadFile("/proc/self/stat")
	start, ppid, ok := statStart(string(data))
	if !ok {
		t.Fatal("statStart")
	}
	_ = start
	pdata, _ := os.ReadFile("/proc/" + strconv.Itoa(ppid) + "/stat")
	pstart, _, _ := statStart(string(pdata))
	if _, err := target(map[string]string{"pid": strconv.Itoa(ppid), "start": strconv.FormatUint(pstart, 10)}); err == nil {
		t.Fatal("allowed signalling our own parent")
	}
	if _, err := target(map[string]string{"pid": strconv.Itoa(ppid), "start": "1"}); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("PID reuse not caught: %v", err)
	}
	if _, err := target(map[string]string{"pid": "1", "start": "0"}); err == nil {
		t.Fatal("allowed PID 1")
	}
}

func TestServiceUnit(t *testing.T) {
	u := serviceUnit("/home/debian/.config/opsie/config.toml", "debian")
	for _, want := range []string{"ExecStart=/usr/local/libexec/opsie/opsie daemon --config /home/debian/.config/opsie/config.toml --user debian --state /var/lib/opsie", "WantedBy=multi-user.target", "StateDirectory=opsie"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
}

func TestServiceInstallChecksConfigPath(t *testing.T) {
	// Whoever runs the tests: the account has to exist for the path to be judged.
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	for _, bad := range []string{"relative/config.toml", "/etc/shadow", me.HomeDir + "/../../etc/config.toml", me.HomeDir + "/.config/opsie/other.toml", me.HomeDir + "/a b/config.toml"} {
		if _, err := opServiceInstall(context.Background(), map[string]string{"config": bad, "caller": me.Username}); err == nil || !strings.Contains(err.Error(), "isn't a config file") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestLocalCaller(t *testing.T) {
	var pong string
	if err := (Local{}).Call(context.Background(), "ping", nil, &pong); err != nil || pong != "pong" {
		t.Fatal(pong, err)
	}
	if err := (Local{}).Call(context.Background(), "nope", nil, nil); err == nil {
		t.Fatal("unknown op")
	}
}

func TestScheduledAndClock(t *testing.T) {
	s := ParseScheduled("USEC=1731300000000000\nWARN_WALL=1\nMODE=reboot\n")
	if s.Mode != "reboot" || s.At.Unix() != 1731300000 {
		t.Errorf("%+v", s)
	}
	for in, want := range map[string]bool{"04:00": true, "4:30": true, "23:59": true, "24:00": false, "04:60": false, "now": false, "+5": false, "04:00; reboot": false} {
		if ValidClock(in) != want {
			t.Errorf("%q: %v", in, !want)
		}
	}
}
