package priv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Where the background service lives. It runs as root from its own copy of
// opsie, which only root can change, so it doesn't run a file your account
// could swap out.
const (
	ServiceUnit = "opsie.service"
	ServiceFile = "/etc/systemd/system/opsie.service"
	ServiceBin  = "/usr/local/libexec/opsie/opsie"
	ServiceDir  = "/var/lib/opsie"
)

// ServiceInstalled reports whether the background service is set up.
func ServiceInstalled() bool {
	_, err := os.Stat(ServiceFile)
	return err == nil
}

func serviceUnit(config, user string) string {
	return fmt.Sprintf(`# Installed by opsie. Remove it from opsie, or with:
#   sudo systemctl disable --now opsie && sudo rm %s
[Unit]
Description=opsie background checks
After=network-online.target docker.service
Wants=network-online.target

[Service]
ExecStart=%s daemon --config %s --user %s --state %s
Restart=on-failure
RestartSec=15
Nice=5
StateDirectory=opsie
StateDirectoryMode=0755

[Install]
WantedBy=multi-user.target
`, ServiceFile, ServiceBin, config, user, ServiceDir)
}

// opServiceInstall copies the running opsie, which is the one that started
// this helper, into place and (re)starts the service with it. Updating
// opsie runs it again to bring the service up to date.
func opServiceInstall(ctx context.Context, args map[string]string) (any, error) {
	a, err := findAccount(caller(args))
	if err != nil {
		return nil, err
	}
	config := filepath.Clean(args["config"])
	if !filepath.IsAbs(config) || !strings.HasPrefix(config, a.home+"/") || filepath.Base(config) != "config.toml" || strings.ContainsAny(config, " \t\n\"'\\%$") {
		return nil, fmt.Errorf("%q isn't a config file in %s's home", args["config"], a.name)
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(ServiceBin), 0o755); err != nil {
		return nil, err
	}
	if err := copyFile(exe, ServiceBin, 0o755); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(ServiceFile, []byte(serviceUnit(config, a.name)), 0o644); err != nil {
		return nil, err
	}
	if err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		return nil, err
	}
	if err := run(ctx, "systemctl", "enable", ServiceUnit); err != nil {
		return nil, err
	}
	return nil, run(ctx, "systemctl", "restart", ServiceUnit)
}

// opServiceRefresh updates an installed service to this opsie; it does
// nothing if the service isn't installed.
func opServiceRefresh(ctx context.Context, args map[string]string) (any, error) {
	if !ServiceInstalled() {
		return "not installed", nil
	}
	return opServiceInstall(ctx, args)
}

// opServiceRemove turns the background service off. With purge, used when
// opsie itself is being uninstalled, it also deletes the history and
// status the service kept.
func opServiceRemove(ctx context.Context, args map[string]string) (any, error) {
	_ = run(ctx, "systemctl", "disable", "--now", ServiceUnit)
	for _, p := range []string{ServiceFile, ServiceBin} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	_ = os.Remove(filepath.Dir(ServiceBin))
	if args["purge"] == "1" {
		if err := os.RemoveAll(ServiceDir); err != nil {
			return nil, err
		}
	}
	return nil, run(ctx, "systemctl", "daemon-reload")
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}
