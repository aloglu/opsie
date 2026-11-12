package metrics

import "testing"

func TestParseLogind(t *testing.T) {
	out := `Name=debian
TTY=
RemoteHost=203.0.113.9
Service=sshd
Type=tty
Timestamp=Tue 2026-11-10 12:50:28 UTC

Name=debian
TTY=
RemoteHost=
Service=
Type=unspecified
Timestamp=Wed 2026-11-04 11:35:01 UTC

Name=root
TTY=tty1
RemoteHost=
Service=login
Type=tty
Timestamp=`
	got := parseLogind(out)
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2 (background sessions are skipped): %+v", len(got), got)
	}
	if got[0].User != "debian" || got[0].From != "203.0.113.9" || got[0].Terminal != "sshd" || got[0].Started.Hour() != 12 {
		t.Errorf("first session: %+v", got[0])
	}
	if got[1].Terminal != "tty1" {
		t.Errorf("second session: %+v", got[1])
	}
}

func TestVirtualInterfaces(t *testing.T) {
	for name, virtual := range map[string]bool{"lo": true, "veth12ab": true, "docker0": true, "br-1f2e": true, "ens3": false, "eth0": false} {
		if virtualInterface(name) != virtual {
			t.Errorf("%s: virtual=%v", name, !virtual)
		}
	}
}
