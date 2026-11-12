package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMissingFileGivesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || !reflect.DeepEqual(cfg, Default()) {
		t.Fatalf("%+v %v", cfg, err)
	}
}

func TestPartialFileKeepsOtherDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("refresh_seconds = 5\n[thresholds]\ndisk_warn = 70\nload_crit = -1\n"), 0o600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	d := Default()
	if cfg.RefreshSeconds != 5 || cfg.Thresholds.DiskWarn != 70 || cfg.Thresholds.DiskCrit != d.Thresholds.DiskCrit || cfg.Thresholds.LoadCrit != d.Thresholds.LoadCrit {
		t.Fatalf("%+v", cfg)
	}
}

func TestBrokenFileStillStarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("refresh_seconds = = 5"), 0o600)
	cfg, err := Load(path)
	if err == nil || !reflect.DeepEqual(cfg, Default()) {
		t.Fatalf("want defaults and an error, got %+v %v", cfg, err)
	}
}

func TestQuietHours(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 10, h, m, 0, 0, time.UTC) }
	n := Notify{QuietHours: "23:00-08:00"}
	for _, c := range []struct {
		t    time.Time
		want bool
	}{{at(23, 30), true}, {at(3, 0), true}, {at(8, 0), false}, {at(12, 0), false}, {at(22, 59), false}} {
		if got := n.Quiet(c.t); got != c.want {
			t.Errorf("%v: %v", c.t, got)
		}
	}
	if (Notify{QuietHours: "12:00-14:00"}).Quiet(at(15, 0)) || !(Notify{QuietHours: "12:00-14:00"}).Quiet(at(13, 0)) {
		t.Error("same-day range")
	}
	if (Notify{QuietHours: "nonsense"}).Quiet(at(3, 0)) {
		t.Error("bad value should mean no quiet hours")
	}
	c := Default()
	c.Notify.QuietHours = "late"
	c.Notify.TelegramToken = "x"
	if len(c.Problems()) != 2 {
		t.Errorf("problems %v", c.Problems())
	}
}

func TestThemeAndApps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("theme = \"neon\"\nappearance = \"sepia\"\napps_dir = \"~/srv\"\n"), 0o600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// An old appearance line is ignored.
	if cfg.Theme != "opsie" {
		t.Errorf("bad theme kept: %q", cfg.Theme)
	}
	if p := cfg.Problems(); len(p) != 1 || !strings.Contains(p[0], "neon") {
		t.Errorf("problems %v", p)
	}
	if got := cfg.Apps("/home/me"); got != "/home/me/srv" {
		t.Error(got)
	}
	for in, want := range map[string]string{"~": "/home/me", "apps": "/home/me/apps", "/srv/apps/": "/srv/apps"} {
		if got := (Config{AppsDir: in}).Apps("/home/me"); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
	if Default().Apps("/home/me") != "/home/me/apps" {
		t.Error("default apps dir")
	}
}
