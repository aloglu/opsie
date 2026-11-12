package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloglu/opsie/internal/app"
	"github.com/aloglu/opsie/internal/motd"
)

func fakeInstall(t *testing.T) (*uninstaller, *bytes.Buffer, *int) {
	t.Helper()
	home := t.TempDir()
	paths := app.Paths{Config: filepath.Join(home, ".config/opsie"), State: filepath.Join(home, ".local/state/opsie"), Backups: filepath.Join(home, "opsie-backups")}
	exe := filepath.Join(home, ".local/bin/opsie")
	for path, text := range map[string]string{
		exe:                                    "binary",
		paths.ConfigFile():                     "theme = \"moss\"\n",
		paths.BackupKey():                      "AGE-SECRET-KEY-1EXAMPLE\n",
		paths.AuditLog():                       "{}\n",
		filepath.Join(paths.Backups, "a.tar"):  "backup",
		filepath.Join(home, ".profile"):        "export EDITOR=vi\n",
		filepath.Join(home, "unrelated/notes"): "mine",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := motd.Install(home, "/bin/bash", exe); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	removed := 0
	u := &uninstaller{say: func(s string) { out.WriteString(s) }, home: home, shell: "/bin/bash", exe: exe, paths: paths,
		serviceInstalled: func() bool { return true },
		removeService:    func(context.Context) error { removed++; return nil },
	}
	return u, &out, &removed
}

func TestUninstallRemovesWhatOpsieAdded(t *testing.T) {
	u, out, removed := fakeInstall(t)
	steps, kept := u.plan()
	if err := u.run(context.Background(), steps); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, gone := range []string{u.exe, u.paths.ConfigFile(), u.paths.State} {
		if exists(gone) {
			t.Errorf("%s is still there", gone)
		}
	}
	// Backups and the key that opens them are the user's, and so is
	// everything opsie didn't write.
	for _, stays := range []string{u.paths.BackupKey(), filepath.Join(u.paths.Backups, "a.tar"), filepath.Join(u.home, "unrelated/notes")} {
		if !exists(stays) {
			t.Errorf("%s was removed", stays)
		}
	}
	if *removed != 1 {
		t.Errorf("service removed %d times", *removed)
	}
	if motd.Installed(u.home, u.shell) {
		t.Error("the login summary is still set up")
	}
	if profile, _ := os.ReadFile(motd.File(u.home, u.shell)); !strings.Contains(string(profile), "EDITOR=vi") {
		t.Errorf("the rest of the profile was lost: %q", profile)
	}
	if all := strings.Join(kept, "\n"); !strings.Contains(all, "backup-key.txt") || !strings.Contains(all, "opsie-backups") {
		t.Errorf("the plan doesn't say what stays: %s", all)
	}
}

func TestUninstallKeepsSettingsWhenAsked(t *testing.T) {
	u, _, _ := fakeInstall(t)
	u.keepSettings = true
	steps, _ := u.plan()
	if err := u.run(context.Background(), steps); err != nil {
		t.Fatal(err)
	}
	if !exists(u.paths.ConfigFile()) || !exists(u.paths.AuditLog()) || exists(u.exe) {
		t.Error("settings should stay and the binary should go")
	}
}

func TestUninstallKeepsItselfWhenAStepFails(t *testing.T) {
	u, out, _ := fakeInstall(t)
	u.removeService = func(context.Context) error { return errors.New("sudo said no") }
	steps, _ := u.plan()
	if err := u.run(context.Background(), steps); err == nil {
		t.Fatal("a failed step should fail the uninstall")
	}
	// The command has to survive so it can be run again.
	if !exists(u.exe) {
		t.Error("the binary was removed although the service wasn't")
	}
	if !strings.Contains(out.String(), "sudo said no") {
		t.Errorf("the reason isn't shown:\n%s", out)
	}
}
