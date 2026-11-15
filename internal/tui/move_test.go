package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/aloglu/opsie/internal/backup"
	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/priv"
)

// moveModel is a model on a server with nothing running and a folder of
// backups from another one.
func moveModel(t *testing.T) (*Model, string) {
	m := testModel(t)
	dir := t.TempDir()
	m.env.Config.Backup.Dir = dir
	m.env.Config.AppsDir = filepath.Join(t.TempDir(), "apps")
	m.facts.Containers = nil
	old := time.Date(2026, 11, 1, 3, 30, 0, 0, time.UTC)
	m.bk = backupsState{at: time.Now(), b: priv.Backups{Dir: dir, Free: 50 << 30, List: []priv.BackupInfo{
		{Manifest: backup.Manifest{Project: "wiki", Created: old.Add(13 * 24 * time.Hour), Host: "oldbox", Folder: "/home/someone-else/apps/wiki", Bytes: 2 << 30, Recipient: "age1example"}, Path: filepath.Join(dir, "wiki-20261114-033000.tar.gz.age"), Size: 1 << 30},
		{Manifest: backup.Manifest{Project: "wiki", Created: old, Host: "oldbox", Folder: "/home/someone-else/apps/wiki"}, Path: filepath.Join(dir, "wiki-20261101-033000.tar.gz.age"), Size: 1 << 30},
		{Manifest: backup.Manifest{Project: "notes", Created: old, Host: "oldbox", Folder: "/home/someone-else/apps/notes", Skipped: []string{"/mnt/media"}}, Path: filepath.Join(dir, "notes-20261101-033000.tar.gz"), Size: 5 << 20},
	}}}
	return m, dir
}

func TestRestoreAllScreen(t *testing.T) {
	m, _ := moveModel(t)
	if got := m.newestBackups(); len(got) != 2 || got[1].Path != m.bk.b.List[0].Path {
		t.Fatalf("newest: %+v", got)
	}
	d := m.detailFor("restoreall", 60)
	text := ansi.Strip(strings.Join(d.body, "\n"))
	for _, want := range []string{"wiki", "notes", "from oldbox", "goes to " + tildePath(filepath.Join(m.appsDir(), "wiki")), "paste the secret key"} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
	if len(d.fixes) == 0 || d.fixes[0].Special != "move:restore" || !d.fixes[0].Danger || d.fixes[0].Input == nil || !d.fixes[0].Input.Secret {
		t.Fatalf("fixes %+v", d.fixes)
	}
	// With nowhere set, there's nothing to fetch from; with S3 set, there is.
	for _, f := range d.fixes {
		if strings.HasPrefix(f.Special, "move:fetch:") {
			t.Errorf("a fetch with no source: %+v", f)
		}
	}
	m.env.Config.Backup.S3 = config.S3{Endpoint: "https://s3.example.com", Bucket: "b", KeyID: "id", Secret: "s"}
	found := false
	for _, f := range m.detailFor("restoreall", 60).fixes {
		found = found || f.Special == "move:fetch:s3"
	}
	if !found {
		t.Error("no fetch from S3")
	}
	// An app whose folder is here already is left alone.
	os.MkdirAll(filepath.Join(m.appsDir(), "wiki"), 0o755)
	os.MkdirAll(filepath.Join(m.appsDir(), "notes"), 0o755)
	d = m.detailFor("restoreall", 60)
	for _, f := range d.fixes {
		if f.Special == "move:restore" {
			t.Error("offers to restore apps that are here")
		}
	}
	// The Docker tab leads here when nothing runs.
	if ids := m.dockerItems(); len(ids) == 0 || ids[len(ids)-1] != "restoreall" {
		t.Errorf("docker items %v", ids)
	}
}

func TestSettingsTravel(t *testing.T) {
	m, dir := moveModel(t)
	secret, public, err := backup.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	cfgFile := m.env.Paths.ConfigFile()
	os.MkdirAll(filepath.Dir(cfgFile), 0o700)
	os.WriteFile(cfgFile, []byte("theme = \"nord\"\n\n[notify]\nntfy_url = \"https://ntfy.sh/a-long-secret-topic\"\n"), 0o600)
	cfg := config.Backup{EncryptTo: public}
	line, err := packSettings(context.Background(), cfg, cfgFile, dir)
	if err != nil || !strings.Contains(line, "saved") {
		t.Fatalf("pack: %q %v", line, err)
	}
	file := m.settingsCopy()
	if data, _ := os.ReadFile(file); file == "" || strings.Contains(string(data), "ntfy") {
		t.Fatalf("the copy %q isn't there or isn't encrypted", file)
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Errorf("copy is %v", st.Mode().Perm())
	}

	// The new server has settings of its own, which are kept.
	os.WriteFile(cfgFile, []byte("theme = \"mono\"\n"), 0o600)
	m.modal = &modal{key: "restoreall"}
	wrong, _, _ := backup.NewKey()
	msg := m.restoreSettings(wrong)().(moveDoneMsg)
	if msg.reload || msg.report[0].status != health.Crit {
		t.Fatalf("a wrong key brought settings back: %+v", msg)
	}
	if data, _ := os.ReadFile(cfgFile); string(data) != "theme = \"mono\"\n" {
		t.Fatalf("a wrong key changed the settings: %s", data)
	}
	msg = m.restoreSettings(secret)().(moveDoneMsg)
	if !msg.reload {
		t.Fatalf("restore: %+v", msg)
	}
	m.moveDone(msg)
	if m.env.Config.Theme != "nord" || m.env.Config.Notify.NtfyURL != "https://ntfy.sh/a-long-secret-topic" {
		t.Errorf("settings not in use: %+v", m.env.Config.Notify)
	}
	if data, _ := os.ReadFile(cfgFile + ".before-restore"); string(data) != "theme = \"mono\"\n" {
		t.Errorf("what was here wasn't kept: %q", data)
	}

	// A pasted key can be kept, once.
	m.moving().key = secret
	m.keepKey()
	if got, pub, ok := m.keyFile(); !ok || !strings.Contains(got, secret) || len(pub) != 1 || pub[0] != public || m.moving().key != "" {
		t.Errorf("key not kept: %v %v", pub, ok)
	}
	if st, _ := os.Stat(m.env.Paths.BackupKey()); st.Mode().Perm() != 0o600 {
		t.Errorf("key file is %v", st.Mode().Perm())
	}
}

func TestPackAllScreen(t *testing.T) {
	m := testModel(t)
	d := m.detailFor("set:packall", 60)
	text := strings.Join(strings.Fields(ansi.Strip(strings.Join(d.body, "\n"))), " ")
	if !strings.Contains(text, "only go along encrypted") || len(d.fixes) != 1 || d.fixes[0].Special != "move:pack" || !d.fixes[0].Danger {
		t.Fatalf("screen:\n%s\n%+v", text, d.fixes)
	}
}

func TestOffServerCost(t *testing.T) {
	cfg := config.Backup{EveryDays: 1}
	for _, c := range []struct {
		keep, every int
		scheduled   bool
		want        []string
	}{
		{0, 1, true, []string{"about 2.0G", "14G a week", "every night", "keeps growing"}},
		{0, 7, true, []string{"2.0G a week", "once a week"}},
		{3, 1, true, []string{"keeps the last 3", "6.0G in all"}},
		{0, 1, false, []string{"about 2.0G.", "keeps growing"}},
	} {
		cfg.KeepRemote, cfg.EveryDays = c.keep, c.every
		got := offServerCost(cfg, 2<<30, c.scheduled)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%+v: %q lacks %q", c, got, w)
			}
		}
		if !c.scheduled && strings.Contains(got, "a week") {
			t.Errorf("a weekly figure for an app with no schedule: %q", got)
		}
	}
	if offServerCost(cfg, 0, true) != "" {
		t.Error("a cost with no size")
	}
}

// A measurement that comes back after the sizes were forgotten, as they
// are when something frees space, has to have somewhere to go.
func TestFolderSizeArrivesAfterForgetting(t *testing.T) {
	m := testModel(t)
	m.forgetDisk()
	m.duUpdated(duMsg{path: "/var"})
	if _, ok := m.du["/var"]; !ok {
		t.Fatal("the measurement was dropped")
	}
}
