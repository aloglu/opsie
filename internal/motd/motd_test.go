package motd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilePicksWhatTheShellReads(t *testing.T) {
	home := t.TempDir()
	if got := File(home, "/bin/bash"); got != filepath.Join(home, ".profile") {
		t.Errorf("bash with nothing: %s", got)
	}
	os.WriteFile(filepath.Join(home, ".bash_profile"), nil, 0o644)
	if got := File(home, "/bin/bash"); got != filepath.Join(home, ".bash_profile") {
		t.Errorf("bash with .bash_profile: %s", got)
	}
	if got := File(home, "/usr/bin/zsh"); got != filepath.Join(home, ".zprofile") {
		t.Errorf("zsh: %s", got)
	}
}

func TestInstallAndRemoveKeepTheRest(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".profile")
	orig := "# my profile\nexport FOO=1\n"
	os.WriteFile(path, []byte(orig), 0o600)

	if _, err := Install(home, "/bin/bash", "/home/me/.local/bin/opsie"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	if !strings.HasPrefix(text, orig) || !strings.Contains(text, "'/home/me/.local/bin/opsie' motd") || !Installed(home, "/bin/bash") {
		t.Fatalf("after install:\n%s", text)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("permissions changed to %v", info.Mode().Perm())
	}
	// Installing again doesn't add a second copy.
	Install(home, "/bin/bash", "/home/me/.local/bin/opsie")
	data, _ = os.ReadFile(path)
	if n := strings.Count(string(data), begin); n != 1 {
		t.Errorf("%d hooks after installing twice", n)
	}
	_, had, err := Remove(home, "/bin/bash")
	if err != nil || !had {
		t.Fatal(had, err)
	}
	data, _ = os.ReadFile(path)
	if string(data) != orig {
		t.Errorf("after remove:\n%q\nwant\n%q", data, orig)
	}
}

func TestFishGetsItsOwnFile(t *testing.T) {
	home := t.TempDir()
	path, err := Install(home, "/usr/bin/fish", "/x/opsie")
	if err != nil || !strings.HasSuffix(path, "conf.d/opsie.fish") {
		t.Fatal(path, err)
	}
	if _, _, err := Remove(home, "/usr/bin/fish"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("fish file left behind")
	}
}

func TestQuoting(t *testing.T) {
	if got := shellQuote("/a b/it's"); got != `'/a b/it'\''s'` {
		t.Error(got)
	}
}
