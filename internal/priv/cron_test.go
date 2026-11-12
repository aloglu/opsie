package priv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadCron(t *testing.T) {
	root := t.TempDir()
	put := func(p, text string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("/etc/cron.d/e2scrub_all", "30 3 * * 0 root /usr/libexec/e2scrub_all_cron\n")
	put("/etc/cron.d/old.dpkg-old", "* * * * * root nope\n")
	put("/etc/cron.daily/man-db", "")
	put("/etc/cron.daily/.placeholder", "")
	put("/var/spool/cron/crontabs/debian", "0 4 * * * ~/backup.sh\n")
	tab := ReadCron(root)
	if tab.Installed {
		t.Error("no cron program, but Installed")
	}
	if len(tab.Jobs) != 2 || tab.Jobs[0].User != "root" || tab.Jobs[1].User != "debian" {
		t.Errorf("jobs: %+v", tab.Jobs)
	}
	if len(tab.Periodic) != 1 || tab.Periodic[0].Every != "daily" || len(tab.Periodic[0].Scripts) != 1 {
		t.Errorf("periodic: %+v", tab.Periodic)
	}
	put("/usr/sbin/cron", "")
	if !ReadCron(root).Installed {
		t.Error("cron is there, but not Installed")
	}
}
