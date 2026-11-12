package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/offsite"
	"github.com/aloglu/opsie/internal/priv"
)

const backupsFile = "backups.json"

// window is how late a missed nightly backup may still start: a server
// that was off at 03:30 catches up when it's back soon after, but doesn't
// stop apps in the middle of the day.
const window = 2 * time.Hour

// nightlyState is what the nightly backups last did, by app.
type nightlyState struct {
	// Day is the last day they ran, like "2026-11-10".
	Day  string             `json:"day"`
	Runs []health.BackupRun `json:"runs"`
}

// ReadBackups reads what the nightly backups last did.
func ReadBackups(dir string) []health.BackupRun {
	var s nightlyState
	if data, err := os.ReadFile(filepath.Join(dir, backupsFile)); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	return s.Runs
}

// due reports whether the nightly backups should start now.
func due(now time.Time, at, lastDay string) bool {
	t, err := time.ParseInLocation("15:04", at, now.Location())
	if err != nil {
		return false
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	return lastDay != now.Format("2006-01-02") && !now.Before(start) && now.Sub(start) < window
}

// BackupsDir is where backups go for account, by the settings.
func BackupsDir(cfg config.Config, account string, dev bool) string {
	home := "/root"
	if u, err := user.Lookup(account); err == nil {
		home = u.HomeDir
	}
	if d := strings.TrimSpace(cfg.Backup.Dir); d != "" {
		return config.Expand(d, home)
	}
	name := "opsie-backups"
	if dev {
		name = "opsie-dev-backups"
	}
	return filepath.Join(home, name)
}

// nightly runs the nightly backups when they're due, until ctx ends.
type nightly struct {
	opt  Options
	call func(ctx context.Context, op string, args map[string]string, out any) error
	mu   sync.Mutex
	logf func(string, ...any)
}

func (n *nightly) loop(ctx context.Context) {
	path := filepath.Join(n.opt.StateDir, backupsFile)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		cfg, err := config.Load(n.opt.ConfigPath)
		if err != nil || len(cfg.Backup.Nightly) == 0 {
			continue
		}
		var s nightlyState
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &s)
		}
		now := time.Now()
		if !due(now, cfg.Backup.At, s.Day) {
			continue
		}
		s.Day = now.Format("2006-01-02")
		saveJSON(path, s, 0o644) // once a day, even if one fails midway
		for _, app := range cfg.Backup.Nightly {
			run := n.backup(ctx, cfg, app)
			n.logf("nightly backup of %s: ok=%v %s", app, run.OK, run.Err)
			kept := []health.BackupRun{run}
			for _, r := range s.Runs {
				if r.Project != app {
					kept = append(kept, r)
				}
			}
			s.Runs = kept
			saveJSON(path, s, 0o644)
		}
	}
}

func (n *nightly) backup(ctx context.Context, cfg config.Config, app string) health.BackupRun {
	run := health.BackupRun{Project: app, At: time.Now()}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Hour)
	defer cancel()
	dir := BackupsDir(cfg, n.opt.User, n.opt.Dev)
	stop := ""
	if cfg.Backup.Stop {
		stop = "1"
	}
	var info priv.BackupInfo
	err := n.call(ctx, "backup.create", map[string]string{"project": app, "dir": dir, "keep": strconv.Itoa(cfg.Backup.Keep),
		"recipient": cfg.Backup.EncryptTo, "stop": stop, "version": n.opt.Version, "caller": n.opt.User}, &info)
	if err != nil {
		run.Err = err.Error()
		return run
	}
	run.Path, run.Size = info.Path, info.Size
	// Check it right away, with the key if it's on the server.
	var v priv.VerifyResult
	err = n.call(ctx, "backup.verify", map[string]string{"file": info.Path, "dir": dir, "caller": n.opt.User,
		"keyfile": filepath.Join(filepath.Dir(n.opt.ConfigPath), "backup-key.txt")}, &v)
	if err != nil {
		run.Err = "made, but the check failed: " + err.Error()
		return run
	}
	run.OK, run.Opened, run.Files = true, v.Opened, v.Files
	if offsite.Configured(cfg.Backup) {
		files := []string{info.Path}
		if side := offsite.Sidecar(info.Path); side != "" {
			files = append(files, side)
		}
		if err := offsite.Copy(ctx, cfg.Backup, files, n.opt.User); err != nil {
			run.CopyErr = err.Error()
		} else {
			run.Copied = true
		}
	}
	return run
}
