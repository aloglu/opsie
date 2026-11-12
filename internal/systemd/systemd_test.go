package systemd

import "testing"

func TestParseShow(t *testing.T) {
	out := `MainPID=734
MemoryCurrent=9170944
CPUUsageNSec=513389129000
TasksCurrent=1
NRestarts=2
Id=ssh.service
Description=OpenBSD Secure Shell server
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
ActiveEnterTimestamp=@1793792065
CanReload=yes
RefuseManualStop=no

MainPID=0
MemoryCurrent=[not set]
Id=cron.service
LoadState=not-found
ActiveState=inactive

Id=apt-daily.service
LoadState=loaded
ActiveState=failed
SubState=failed
MemoryCurrent=[not set]
TriggeredBy=apt-daily.timer`
	u := ParseShow(out)
	if len(u) != 2 {
		t.Fatalf("got %+v", u)
	}
	ssh := u[0]
	if ssh.Name != "ssh.service" || !ssh.Running() || ssh.MainPID != 734 || !ssh.HaveMemory || ssh.Memory != 9170944 || ssh.Restarts != 2 || !ssh.CanReload || ssh.Since.Unix() != 1793792065 || ssh.Short() != "ssh" {
		t.Fatalf("ssh %+v", ssh)
	}
	if !u[1].Failed() || u[1].HaveMemory || u[1].TriggeredBy != "apt-daily.timer" {
		t.Fatalf("apt %+v", u[1])
	}
}

func TestParseTimers(t *testing.T) {
	out := []byte(`[{"next":1794347814401135,"last":1794291957261137,"unit":"apt-daily.timer","activates":"apt-daily.service"},{"next":null,"last":null,"unit":"never.timer","activates":"never.service"},{"next":1794300000000000,"last":0,"unit":"soon.timer","activates":"soon.service"}]`)
	tm, err := ParseTimers(out)
	if err != nil || len(tm) != 3 {
		t.Fatal(err, tm)
	}
	if tm[0].Name != "soon.timer" || tm[2].Name != "never.timer" || !tm[0].Last.IsZero() || tm[1].Next.UnixMicro() != 1794347814401135 {
		t.Fatalf("%+v", tm)
	}
}
