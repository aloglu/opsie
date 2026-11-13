package metrics

import (
	"testing"
	"time"
)

func TestDiskRates(t *testing.T) {
	const stats = `   8       0 sda 100 0 2000 50 200 0 4000 90 0 1000 140 0 0 0 0 0 0
   8       1 sda1 100 0 2000 50 200 0 4000 90 0 1000 140 0 0 0 0 0 0
 259       0 nvme0n1 10 0 100 5 20 0 300 9 0 400 14
   7       0 loop0 5 0 80 1 0 0 0 0 0 4 1 0 0 0 0 0 0
 253       0 dm-0 100 0 2000 50 200 0 4000 90 0 1000 140 0 0 0 0 0 0
`
	whole := func(name string) bool { return name != "sda1" }
	first := parseDiskstats(stats, whole)
	if len(first) != 2 || first["sda"].read != 2000*512 || first["sda"].written != 4000*512 || first["nvme0n1"].busyMs != 400 {
		t.Fatalf("partitions, loops and mapped devices aren't disks: %+v", first)
	}
	if io := rates(nil, first, 0); io.Ready || io.ReadTotal != 2100*512 || len(io.Devices) != 2 {
		t.Errorf("a first reading has totals but no rates: %+v", io)
	}
	second := map[string]diskCount{
		"sda":     {read: first["sda"].read + 4<<20, written: first["sda"].written + 2<<20, busyMs: 1000 + 500},
		"nvme0n1": {read: first["nvme0n1"].read, written: first["nvme0n1"].written + 6<<20, busyMs: 400 + 1800},
	}
	io := rates(first, second, 2*time.Second)
	if !io.Ready || io.ReadRate != 2<<20 || io.WriteRate != 4<<20 || io.Busy != 90 {
		t.Errorf("rates: %+v", io)
	}
	// A disk that was swapped, so its counters start over, isn't a burst.
	second["sda"] = diskCount{read: 10}
	if io := rates(first, second, 2*time.Second); io.ReadRate != 0 || io.WriteRate != 3<<20 {
		t.Errorf("after a reset: %+v", io)
	}
}

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
