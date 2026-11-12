package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"

	"time"

	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/facts"
	"github.com/aloglu/opsie/internal/notify"
	"github.com/aloglu/opsie/internal/priv"
	"github.com/aloglu/opsie/internal/watch"
)

const loginsFile = "logins.json"

// ReadLogins reads which addresses the service has seen log in.
func ReadLogins(dir string) (watch.Logins, bool) {
	var l watch.Logins
	data, err := os.ReadFile(filepath.Join(dir, loginsFile))
	if err != nil {
		return l, false
	}
	return l, json.Unmarshal(data, &l) == nil
}

// KeyName names the authorized key a login used, by its comment.
func KeyName(f facts.Facts, l priv.Login) string {
	for _, set := range f.Keys {
		for _, k := range set.Keys {
			if k.Fingerprint == l.Fingerprint && l.Fingerprint != "" {
				if k.Comment != "" {
					return k.Comment
				}
				return k.Type + " key"
			}
		}
	}
	if l.Method == "password" || l.Method == "keyboard-interactive/pam" {
		return "a password"
	}
	return ""
}

// loginAlerts works out which new logins to tell about.
func loginAlerts(dir string, f facts.Facts, n config.Notify, host string, now time.Time) []notify.Message {
	l, _ := ReadLogins(dir)
	fresh, first := l.Step(f.Failures.Accepted, now)
	saveJSON(filepath.Join(dir, loginsFile), l, 0o644)
	var out []notify.Message
	for i, a := range fresh {
		if n.Logins == "off" || (n.Logins == "new" && !first[i]) || (!first[i] && n.Quiet(now)) {
			continue
		}
		with := KeyName(f, a)
		if with == "" {
			with = a.Method
		}
		m := notify.Message{Title: watch.Title(host), Level: notify.LevelInfo,
			Lines: []string{"SSH login: " + a.User + " from " + a.From + " with " + with + " at " + a.At.Local().Format("15:04") + "."}}
		if first[i] {
			m.Level = notify.LevelWarn
			m.Lines[0] = "New SSH login: " + a.User + " from " + a.From + ", an address opsie hasn't seen before, with " + with + " at " + a.At.Local().Format("15:04") + "."
			m.Lines = append(m.Lines, "If it wasn't you, end the session in opsie's Security tab and remove that key.")
		}
		out = append(out, m)
	}
	return out
}
