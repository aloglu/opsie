package offsite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloglu/opsie/internal/config"
)

// fakeSSH puts stand-ins for ssh and scp first on PATH that record how
// they were called.
func fakeSSH(t *testing.T, fail string) string {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	for _, name := range []string{"ssh", "scp"} {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> " + log + "\n"
		if name == fail {
			script += "echo 'Host key verification failed.' >&2\nexit 255\n"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return log
}

func TestCopySSH(t *testing.T) {
	log := fakeSSH(t, "")
	b := config.Backup{SSH: "backup@nas.example:/srv/backups/harbor", SSHPort: 2222}
	if err := Copy(context.Background(), b, []string{"/home/me/opsie-backups/a.tar.gz.age", "/home/me/opsie-backups/a.json"}, ""); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(log)
	calls := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(calls) != 2 {
		t.Fatalf("calls %q", calls)
	}
	for _, c := range calls {
		for _, want := range []string{"BatchMode=yes", "StrictHostKeyChecking=yes", "Port=2222"} {
			if !strings.Contains(c, want) {
				t.Errorf("%q lacks %s", c, want)
			}
		}
	}
	if !strings.HasSuffix(calls[0], "-- backup@nas.example mkdir -p -- '/srv/backups/harbor'") {
		t.Errorf("mkdir: %s", calls[0])
	}
	if !strings.HasSuffix(calls[1], "-q -p -- /home/me/opsie-backups/a.tar.gz.age /home/me/opsie-backups/a.json backup@nas.example:/srv/backups/harbor/") {
		t.Errorf("scp: %s", calls[1])
	}
}

func TestCopySSHExplainsHostKeys(t *testing.T) {
	fakeSSH(t, "ssh")
	err := Copy(context.Background(), config.Backup{SSH: "backup@nas.example:/srv/b"}, []string{"/x"}, "")
	if err == nil || !strings.Contains(err.Error(), "host key isn't known yet") {
		t.Fatalf("got %v", err)
	}
}
