package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetKeepsTheRestOfTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	orig := `# my settings
refresh_seconds = 2   # fast

[thresholds]
disk_warn = 80

[notify]
level = "warn"  # or crit
`
	os.WriteFile(path, []byte(orig), 0o640)
	steps := []struct {
		key   string
		value any
	}{
		{"theme", "moss"},                    // new, top level
		{"notify.level", "crit"},             // replaced, comment kept
		{"notify.ntfy_url", "https://x/y"},   // new, in a table
		{"thresholds.disk_warn", nil},        // removed
		{"refresh_seconds", 5.0},             // replaced
		{"notify.quiet_hours", `say "hi" \`}, // escaping
	}
	for _, s := range steps {
		if err := Set(path, s.key, s.value); err != nil {
			t.Fatal(s.key, err)
		}
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	for _, want := range []string{"# my settings", "refresh_seconds = 5.0   # fast", `theme = "moss"`, `level = "crit"  # or crit`, `ntfy_url = "https://x/y"`, "[thresholds]"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "disk_warn") {
		t.Errorf("disk_warn not removed:\n%s", text)
	}
	// theme went to the top, before the first table.
	if strings.Index(text, "theme") > strings.Index(text, "[thresholds]") {
		t.Errorf("theme in a table:\n%s", text)
	}
	cfg, err := Load(path)
	if err != nil || cfg.Theme != "moss" || cfg.Notify.Level != "crit" || cfg.Notify.QuietHours != `say "hi" \` || cfg.RefreshSeconds != 5 {
		t.Errorf("reloaded %+v %v", cfg, err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	if v := FileValues(path); v["notify.ntfy_url"] != "https://x/y" || v["thresholds.disk_warn"] != nil {
		t.Errorf("file values %v", v)
	}
}

func TestSetMakesANewPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	if err := Set(path, "notify.telegram_token", "123:abc"); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if string(data) != "[notify]\ntelegram_token = \"123:abc\"\n" {
		t.Errorf("%q", data)
	}
	// Removing what isn't there is fine.
	if err := Set(path, "theme", nil); err != nil {
		t.Fatal(err)
	}
}

func TestSetRefusesToBreakTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("refresh_seconds = 2\n"), 0o600)
	if err := Set(path, "refresh_seconds", "fast"); err == nil {
		t.Error("a string for a number was written")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "refresh_seconds = 2\n" {
		t.Errorf("file changed: %q", data)
	}
}

func TestValues(t *testing.T) {
	v := Default().Values()
	if v["notify.level"] != "warn" || v["thresholds.disk_warn"] != 85.0 || v["apps_dir"] != "~/apps" {
		t.Errorf("%v", v)
	}
}

func TestSetNestedTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("[backup]\nkeep = 3 # mine\n"), 0o600)
	if err := Set(path, "backup.s3.bucket", "b"); err != nil {
		t.Fatal(err)
	}
	if err := Set(path, "backup.s3.secret", "s"); err != nil {
		t.Fatal(err)
	}
	if err := Set(path, "backup.nightly", []string{"app"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.Backup.S3.Bucket != "b" || cfg.Backup.S3.Secret != "s" || cfg.Backup.Keep != 3 || len(cfg.Backup.Nightly) != 1 {
		data, _ := os.ReadFile(path)
		t.Fatalf("%+v %v\n%s", cfg.Backup, err, data)
	}
}
