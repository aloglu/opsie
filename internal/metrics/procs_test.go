package metrics

import (
	"os"
	"testing"
)

func TestParseStat(t *testing.T) {
	line := "734 (sshd (a) b) S 1 734 734 0 -1 4194560 2296 0 0 0 30 20 0 0 20 5 1 0 456 12000000 2300 18446744073709551615"
	p, ticks, ok := ParseStat([]byte(line))
	if !ok || p.PID != 734 || p.Name != "sshd (a) b" || p.PPID != 1 || ticks != 50 || p.Nice != 5 || p.Threads != 1 || p.Start != 456 || p.RSS != 2300 || p.Kernel {
		t.Fatalf("%+v %d %v", p, ticks, ok)
	}
	if p, _, _ := ParseStat([]byte("15 (kworker/0:1) I 2 0 0 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 9 0 0 0")); !p.Kernel {
		t.Fatal("kernel thread")
	}
}

func TestParseCgroup(t *testing.T) {
	cases := map[string][2]string{
		"0::/system.slice/ssh.service":                           {"ssh.service", ""},
		"0::/system.slice/system-getty.slice/getty@tty1.service": {"getty@tty1.service", ""},
		"0::/system.slice/docker-" + sixtyFour + ".scope":        {"", sixtyFour},
		"0::/user.slice/user-1000.slice/session-60.scope":        {"", ""},
		"0::/init.scope": {"", ""},
	}
	for in, want := range cases {
		u, c := ParseCgroup(in)
		if u != want[0] || c != want[1] {
			t.Errorf("%s: %q %q", in, u, c)
		}
	}
}

const sixtyFour = "4f1c0e2b9a8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d9c8b7a6f5e4d3c2b"

func TestSampleSelf(t *testing.T) {
	s := NewProcSampler()
	s.Sample()
	for _, p := range s.Sample() {
		if p.PID == os.Getpid() {
			if p.Cmd == "" || p.User == "" || p.RSS == 0 || p.Started.IsZero() {
				t.Fatalf("%+v", p)
			}
			return
		}
	}
	t.Fatal("didn't find this process")
}
