package health

import (
	"strings"
	"time"

	"github.com/aloglu/opsie/internal/human"
)

// BackupRun is how a nightly backup of one app went.
type BackupRun struct {
	Project string    `json:"project"`
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Err     string    `json:"err,omitempty"`
	Path    string    `json:"path,omitempty"`
	Size    uint64    `json:"size,omitempty"`
	// Opened is whether the check after it read it all the way through;
	// without the key, it only compares the checksum.
	Opened bool `json:"opened,omitempty"`
	Files  int  `json:"files,omitempty"`
	// Copied is whether it went off the server too, when that's set up.
	Copied  bool   `json:"copied,omitempty"`
	CopyErr string `json:"copy_err,omitempty"`
}

// Nightly is the nightly backup setup and what it last did.
type Nightly struct {
	Apps []string
	At   string
	Runs []BackupRun
	// Running is whether the background service is there to do it.
	Running bool
}

func (n Nightly) last(project string) (BackupRun, bool) {
	for _, r := range n.Runs {
		if r.Project == project {
			return r, true
		}
	}
	return BackupRun{}, false
}

func nightlyBackups(n Nightly, now time.Time) []Result {
	if len(n.Apps) == 0 {
		return nil
	}
	r := Result{ID: "backups", Title: "Nightly backups", Status: OK}
	fix := Fix{Label: "Background checks", Special: "open:settings:result:watcher"}
	if !n.Running {
		r.Status, r.Summary = Warn, "not running"
		r.Detail = []string{"Nightly backups of " + strings.Join(n.Apps, ", ") + " are run by the background checks, which aren't running, so none are being made."}
		r.Fixes = []Fix{fix}
		return []Result{r}
	}
	var failed, stale, never, good, uncopied []string
	for _, app := range n.Apps {
		run, ok := n.last(app)
		switch {
		case !ok:
			never = append(never, app)
		case !run.OK:
			failed = append(failed, app+" ("+run.Err+")")
		case now.Sub(run.At) > 30*time.Hour:
			stale = append(stale, app+" ("+human.Duration(now.Sub(run.At))+" ago)")
		default:
			good = append(good, app)
			if run.CopyErr != "" {
				uncopied = append(uncopied, app+" ("+run.CopyErr+")")
			}
		}
	}
	switch {
	case len(failed) > 0:
		r.Status, r.Summary = Crit, human.Plural(len(failed), "failed", "failed")
		r.Detail = append(r.Detail, "The last nightly backup failed for "+strings.Join(failed, ", ")+". Open the app's backups to try again.")
	case len(stale) > 0:
		r.Status, r.Summary = Warn, "missed"
		r.Detail = append(r.Detail, "No backup in over a day for "+strings.Join(stale, ", ")+". The server may have been off or busy at "+n.At+"; a backup is only started within two hours of that time.")
	case len(uncopied) > 0:
		r.Status, r.Summary = Warn, "not copied off"
		r.Detail = append(r.Detail, "The backups were made, but copying them off the server failed for "+strings.Join(uncopied, ", ")+". If this server is lost, so are they.")
	case len(good) == 0:
		r.Status, r.Summary = Info, "first at "+n.At
		r.Detail = append(r.Detail, "The first nightly backup of "+strings.Join(never, ", ")+" runs at "+n.At+".")
	default:
		r.Summary = human.Plural(len(good), "app", "apps") + " backed up"
		if len(never) > 0 {
			r.Detail = append(r.Detail, strings.Join(never, ", ")+" will be backed up for the first time at "+n.At+".")
		}
	}
	var lines []string
	for _, app := range n.Apps {
		if run, ok := n.last(app); ok && run.OK {
			check := "checksum checked"
			if run.Opened {
				check = "read back in full"
			}
			if run.Copied {
				check += ", copied off"
			}
			lines = append(lines, app+": "+human.Duration(now.Sub(run.At))+" ago, "+human.Bytes(run.Size)+", "+check)
		}
	}
	if len(lines) > 0 {
		r.Detail = append(r.Detail, strings.Join(lines, "; ")+".")
	}
	r.Detail = append(r.Detail, "They run every night at "+n.At+".")
	for _, app := range n.Apps {
		r.Fixes = append(r.Fixes, Fix{Label: "Backups of " + app, Special: "backup:" + app})
	}
	return []Result{r}
}
