package priv

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aloglu/opsie/internal/cron"
)

// CronTable is everything cron is set up to run.
type CronTable struct {
	// Installed is whether a cron program is on the server at all; without
	// one, none of the jobs below run.
	Installed bool         `json:"installed"`
	Jobs      []cron.Job   `json:"jobs"`
	Periodic  []CronFolder `json:"periodic"`
	// Unreadable is set when users' own crontabs couldn't be read, which
	// happens without admin access.
	Unreadable bool `json:"unreadable"`
}

// CronFolder is one of /etc/cron.daily and the like: scripts run in turn.
type CronFolder struct {
	Every   string   `json:"every"` // hourly, daily, weekly, monthly
	Path    string   `json:"path"`
	Scripts []string `json:"scripts"`
}

// ReadCron reads the crontabs under root ("/" on the real server).
func ReadCron(root string) CronTable {
	var t CronTable
	at := func(p string) string { return filepath.Join(root, p) }
	for _, bin := range []string{"/usr/sbin/cron", "/usr/sbin/crond", "/usr/bin/crond", "/usr/sbin/anacron", "/usr/bin/fcron"} {
		if _, err := os.Stat(at(bin)); err == nil {
			t.Installed = true
		}
	}
	read := func(path, owner string, system bool) {
		data, err := os.ReadFile(at(path))
		if err == nil {
			t.Jobs = append(t.Jobs, cron.ParseFile(string(data), path, owner, system)...)
		}
	}
	read("/etc/crontab", "", true)
	if names, err := os.ReadDir(at("/etc/cron.d")); err == nil {
		for _, n := range names {
			if runPartsName(n.Name()) && !n.IsDir() {
				read("/etc/cron.d/"+n.Name(), "", true)
			}
		}
	}
	for _, spool := range []string{"/var/spool/cron/crontabs", "/var/spool/cron"} {
		names, err := os.ReadDir(at(spool))
		if err != nil {
			if os.IsPermission(err) {
				t.Unreadable = true
			}
			continue
		}
		for _, n := range names {
			if n.IsDir() || strings.HasPrefix(n.Name(), ".") {
				continue
			}
			path := spool + "/" + n.Name()
			if _, err := os.ReadFile(at(path)); os.IsPermission(err) {
				t.Unreadable = true
			}
			read(path, n.Name(), false)
		}
		break
	}
	for _, every := range []string{"hourly", "daily", "weekly", "monthly"} {
		path := "/etc/cron." + every
		names, err := os.ReadDir(at(path))
		if err != nil {
			continue
		}
		f := CronFolder{Every: every, Path: path}
		for _, n := range names {
			if !n.IsDir() && runPartsName(n.Name()) {
				f.Scripts = append(f.Scripts, n.Name())
			}
		}
		if len(f.Scripts) > 0 {
			sort.Strings(f.Scripts)
			t.Periodic = append(t.Periodic, f)
		}
	}
	return t
}

// runPartsName is whether cron would use a file of this name: run-parts
// skips ones with dots, like backups and package leftovers.
func runPartsName(s string) bool {
	if s == "" || s == "placeholder" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func opCron(context.Context, map[string]string) (any, error) {
	return ReadCron("/"), nil
}
