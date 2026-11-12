package priv

import (
	"os"
	"strings"
	"testing"
)

func TestParseInspect(t *testing.T) {
	data, err := os.ReadFile("testdata/inspect.json")
	if err != nil {
		t.Fatal(err)
	}
	list, err := ParseInspect(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d containers", len(list))
	}
	init, web := list[0], list[1] // sorted by name
	if init.Name != "sync-init" || init.State != "exited" || init.Exit != 0 || init.Project != "sync" {
		t.Errorf("init: %+v", init)
	}
	if web.Name != "wiki" || web.State != "running" || web.Health != "healthy" || web.Started.IsZero() ||
		web.Policy != "unless-stopped" || web.ConfigFiles == "" || len(web.Ports) != 1 || web.Ports[0] != "127.0.0.1:8040 → 3000/tcp" {
		t.Errorf("wiki: %+v", web)
	}
}

func TestParseStats(t *testing.T) {
	out := `{"BlockIO":"12.4MB / 108MB","CPUPerc":"1.07%","MemPerc":"4.34%","MemUsage":"166MiB / 3.737GiB","Name":"wiki","NetIO":"154kB / 704kB","PIDs":"35"}
garbage`
	list := ParseStats(out)
	if len(list) != 1 {
		t.Fatalf("got %d", len(list))
	}
	s := list[0]
	if s.CPU != 1.07 || s.MemPct != 4.34 || s.MemUsed != 166<<20 || s.PIDs != 35 || s.MemLimit < 3<<30 {
		t.Fatalf("%+v", s)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]uint64{"0B": 0, "512B": 512, "1.5KiB": 1536, "2GiB": 2 << 30, "3.2GB": 3_200_000_000, "154kB": 154_000, "": 0, "abc": 0} {
		if got := ParseSize(in); got != want {
			t.Errorf("ParseSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseDF(t *testing.T) {
	out := `{"Active":"8","Reclaimable":"3.237GB (44%)","Size":"7.203GB","TotalCount":"17","Type":"Images"}
{"Active":"0","Reclaimable":"2.525GB","Size":"5.19GB","TotalCount":"29","Type":"Build Cache"}`
	list := ParseDF(out)
	if len(list) != 2 || list[0].Total != 17 || list[0].Active != 8 || list[1].Reclaimable != "2.525GB" {
		t.Fatalf("%+v", list)
	}
}

func TestParseUFW(t *testing.T) {
	out := `Status: active
Logging: on (low)
Default: deny (incoming), allow (outgoing), deny (routed)
New profiles: skip

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW IN    Anywhere
443                        ALLOW IN    Anywhere
22/tcp (v6)                ALLOW IN    Anywhere (v6)
`
	fw := ParseUFW(out)
	if !fw.Active || fw.Rules != 3 || fw.Default != "deny (incoming), allow (outgoing), deny (routed)" {
		t.Fatalf("%+v", fw)
	}
	if ParseUFW("Status: inactive\n").Active {
		t.Fatal("inactive ufw reported active")
	}
}

func TestParseSSHD(t *testing.T) {
	cfg := ParseSSHD("port 22\nport 2222\nPermitRootLogin no\npasswordauthentication yes\n")
	if cfg["port"] != "22 2222" || cfg["permitrootlogin"] != "no" || cfg["passwordauthentication"] != "yes" {
		t.Fatalf("%v", cfg)
	}
}

func TestValidation(t *testing.T) {
	for _, ok := range []string{"nginx.service", "getty@tty1.service", "systemd-fsck@dev-disk-by\\x2duuid.service", "a.timer"} {
		if !ValidUnit(ok) {
			t.Errorf("%q should be a valid unit", ok)
		}
	}
	for _, bad := range []string{"", "nginx", "--now.service", "a b.service", "x.service; rm -rf /", "../x.service"} {
		if ValidUnit(bad) {
			t.Errorf("%q shouldn't be a valid unit", bad)
		}
	}
	for _, bad := range []string{"", "-rm", "a b", "a;b", "../a"} {
		if ValidContainer(bad) || ValidProject(bad) {
			t.Errorf("%q shouldn't be a valid container", bad)
		}
	}
}

func TestParseNetDev(t *testing.T) {
	text := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:    1000      10    0    0    0     0          0         0     1000      10    0    0    0     0       0          0
  eth0: 1234567    900    0    0    0     0          0         0   765432     800    0    0    0     0       0          0
  eth1:      33      1    0    0    0     0          0         0       44       1    0    0    0     0       0          0
`
	rx, tx, ok := ParseNetDev(text)
	if !ok || rx != 1234600 || tx != 765476 {
		t.Errorf("got %d %d %v", rx, tx, ok)
	}
}

func TestSetLogLimit(t *testing.T) {
	out, changed, err := SetLogLimit("")
	if err != nil || !changed || !ParseLogDefault(out).Limited {
		t.Fatalf("empty: %q %v %v", out, changed, err)
	}
	out, changed, _ = SetLogLimit(`{"data-root": "/srv/docker", "log-opts": {"labels": "x"}}`)
	d := ParseLogDefault(out)
	if !changed || !d.Limited || !strings.Contains(out, `"data-root": "/srv/docker"`) || !strings.Contains(out, `"labels": "x"`) {
		t.Errorf("kept settings: %s", out)
	}
	if _, changed, _ := SetLogLimit(`{"log-driver": "journald"}`); changed {
		t.Error("changed journald")
	}
	if _, changed, _ := SetLogLimit(`{"log-opts": {"max-size": "50m"}}`); changed {
		t.Error("changed an existing limit")
	}
	if _, _, err := SetLogLimit(`{broken`); err == nil {
		t.Error("broken file accepted")
	}
	if ParseLogDefault("").Limited {
		t.Error("no file means no limit")
	}
}

func TestParseUnused(t *testing.T) {
	out := `[{"Containers":"1","ID":"sha256:aa","Repository":"web","Tag":"2","UniqueSize":"400MB"},` +
		`{"Containers":"0","ID":"sha256:bb","Repository":"web","Tag":"1","UniqueSize":"422.9MB"},` +
		`{"Containers":"0","ID":"sha256:cc","Repository":"<none>","Tag":"<none>","UniqueSize":"12kB"},` +
		`{"Containers":"N/A","ID":"sha256:dd","Repository":"odd","Tag":"1","UniqueSize":"1GB"}]`
	list := ParseUnused(out + "\n")
	if len(list) != 2 {
		t.Fatalf("got %+v: only images with no containers count, and ones Docker can't vouch for don't", list)
	}
	if list[0].Ref != "web:1" || list[0].Size != 422900000 || list[1].Ref != "" || list[1].Size != 12000 {
		t.Fatalf("got %+v", list)
	}
	if ParseUnused("not json") != nil {
		t.Fatal("junk should give nothing")
	}
}
