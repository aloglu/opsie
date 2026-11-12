package health

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/human"
)

// Watcher is the background service's state, as the dashboard sees it.
type Watcher struct {
	Dev       bool // a development build, which runs it by hand
	Installed bool
	Active    bool
	// From the service's status file:
	HaveStatus bool
	Version    string
	Checked    time.Time
	Every      int
	SendError  string
	Problems   []string
	LastSent   time.Time
	// ConfigPath is where the dashboard's settings are, which the service
	// reads too.
	ConfigPath string
	Notify     config.Notify
	// Mine is the dashboard's version.
	Mine string
	// Motd is whether the login summary is set up.
	Motd bool
	// Backups are what the nightly backups last did.
	Backups []BackupRun
}

// Running reports whether the service has checked recently.
func (w Watcher) Running(now time.Time) bool {
	every := time.Duration(max(w.Every, 5)) * time.Second
	return w.HaveStatus && now.Sub(w.Checked) < 3*every+time.Minute
}

// Targets names where alerts go.
func Targets(n config.Notify) []string {
	var out []string
	if n.NtfyURL != "" {
		out = append(out, "ntfy")
	}
	if n.TelegramToken != "" && n.TelegramChat != "" {
		out = append(out, "Telegram")
	}
	if n.WebhookURL != "" {
		out = append(out, "webhook")
	}
	return out
}

func watcher(w Watcher, now time.Time) Result {
	r := Result{ID: "watcher", Title: "Background checks"}
	what := "Background checks run the same checks as this dashboard every few seconds even when it's closed, keep a history, and send you an alert when something goes wrong and again when it's fixed."
	targets := Targets(w.Notify)
	alerts := "Alerts go to " + strings.Join(targets, " and ") + "."
	if len(targets) == 0 {
		alerts = "No alerts are set up yet. Add an ntfy topic, a Telegram bot or a webhook in Settings, and the checks pick it up on their own."
	}
	running := w.Running(now)
	switch {
	case w.Dev && !running:
		r.Status, r.Summary = Info, "off"
		r.Detail = []string{what, "This is a development build, so they aren't installed as a service. Run them in another terminal with: make daemon"}
		return r
	case !w.Installed && !w.Dev:
		r.Status, r.Summary = Info, "off"
		r.Detail = []string{what, "They run as a system service from their own copy of opsie that only root can change, and update along with opsie.", alerts}
		r.Fixes = []Fix{{Label: "Turn on", Op: "service.install", Args: map[string]string{"config": w.ConfigPath}, Done: "Background checks are on"}}
		return r
	case !running:
		r.Status, r.Summary = Warn, "not running"
		r.Detail = []string{"The background checks are installed but haven't checked in lately, so nothing is watching while the dashboard is closed. Their log says why."}
		if !w.Dev {
			r.Fixes = []Fix{
				{Label: "Restart", Op: "unit.restart", Args: map[string]string{"unit": "opsie.service"}, Done: "Restarted the background checks"},
				{Label: "Logs", Special: "logs", Args: map[string]string{"unit": "opsie.service"}},
			}
		}
		return r
	}
	r.Status, r.Summary = OK, "on"
	if len(targets) > 0 {
		r.Summary = "on · alerts by " + strings.Join(targets, ", ")
	}
	r.Detail = []string{fmt.Sprintf("Checked %s ago, every %s.", human.Duration(now.Sub(w.Checked)), human.Duration(time.Duration(w.Every)*time.Second)), alerts}
	if !w.LastSent.IsZero() {
		r.Detail = append(r.Detail, "Last alert sent "+human.Duration(now.Sub(w.LastSent))+" ago.")
	}
	if len(targets) == 0 {
		r.Status, r.Summary = Info, "on · no alerts set"
	}
	if len(w.Problems) > 0 {
		r.Status, r.Summary = Warn, "settings problem"
		r.Detail = append([]string{"There's a mistake in " + shortPath(w.ConfigPath) + ": " + strings.Join(w.Problems, "; ") + "."}, r.Detail...)
	}
	if w.SendError != "" {
		r.Status, r.Summary = Warn, "alerts failing"
		r.Detail = append([]string{"The last alert couldn't be sent: " + w.SendError + ". It's tried again on the next check."}, r.Detail...)
	}
	if len(targets) > 0 {
		r.Fixes = append(r.Fixes, Fix{Label: "Send a test alert", Special: "notify:test"})
	} else {
		r.Fixes = append(r.Fixes, Fix{Label: "Set up alerts", Special: "tab:settings"})
	}
	if !w.Dev {
		if w.Version != "" && w.Version != w.Mine {
			r.Detail = append(r.Detail, "They run opsie "+w.Version+"; this is "+w.Mine+".")
			r.Fixes = append(r.Fixes, Fix{Label: "Update them", Op: "service.install", Args: map[string]string{"config": w.ConfigPath}, Done: "Background checks updated"})
		}
		if w.Motd {
			r.Detail = append(r.Detail, "A short summary of what they found shows each time you log in.")
			r.Fixes = append(r.Fixes, Fix{Label: "Hide summary at login", Special: "motd:off"})
		} else {
			r.Detail = append(r.Detail, "opsie can also show a short summary of what they found each time you log in over SSH.")
			r.Fixes = append(r.Fixes, Fix{Label: "Show summary at login", Special: "motd:on"})
		}
		r.Fixes = append(r.Fixes,
			Fix{Label: "Logs", Special: "logs", Args: map[string]string{"unit": "opsie.service"}},
			Fix{Label: "Turn off", Op: "service.remove", Danger: true,
				Confirm: "Stop the background checks and remove the service? Nothing will watch the server while the dashboard is closed. The history is kept.",
				Done:    "Background checks are off"})
	}
	return r
}

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+"/") {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
