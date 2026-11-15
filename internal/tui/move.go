package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/BurntSushi/toml"

	"github.com/aloglu/opsie/internal/audit"
	"github.com/aloglu/opsie/internal/backup"
	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/facts"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/offsite"
	"github.com/aloglu/opsie/internal/priv"
)

// Moving to another server, or getting one back, is two screens: Back up
// everything on the server that's being left, and Restore from backups
// on the one that's new.

// moveLine is one line of what a run did.
type moveLine struct {
	status health.Status
	text   string
}

// moveState is the last run of either screen, and how far the one that's
// running has got.
type moveState struct {
	// screen is the screen the report belongs to: "packall" or
	// "restoreall".
	screen string
	report []moveLine
	// key is a secret key pasted for the last restore, which can be kept
	// on this server.
	key string

	mu   sync.Mutex
	step string
}

func (s *moveState) say(step string) {
	s.mu.Lock()
	s.step = step
	s.mu.Unlock()
}

// doing is what the run is on right now, or "".
func (s *moveState) doing() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.step
}

type moveDoneMsg struct {
	screen string
	report []moveLine
	key    string
	flash  string
	// reload is whether the settings changed on disk.
	reload bool
}

// settingsRe is a copy of opsie's own settings among the backups.
var settingsRe = regexp.MustCompile(`^opsie-settings-(\d{8}-\d{6})\.toml\.age$`)

// keptSettings is how many copies of the settings stay in the folder.
const keptSettings = 3

// moveTimeout is how long backing up or restoring everything may take.
const moveTimeout = 12 * time.Hour

// newestBackups is the latest backup of each app in the folder.
func (m *Model) newestBackups() []priv.BackupInfo {
	seen := map[string]bool{}
	var out []priv.BackupInfo
	for _, bk := range m.bk.b.List { // newest first
		if !seen[bk.Project] {
			seen[bk.Project] = true
			out = append(out, bk)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out
}

// appHere reports whether an app is on this server already, running or
// as a folder.
func (m *Model) appHere(bk priv.BackupInfo) bool {
	for _, p := range m.projects() {
		if p.name == bk.Project {
			return true
		}
	}
	for _, dir := range []string{bk.Folder, filepath.Join(m.appsDir(), filepath.Base(bk.Folder))} {
		if bk.Folder == "" {
			continue
		}
		if _, err := os.Stat(dir); err == nil {
			return true
		}
	}
	return false
}

// landsIn is where a restore will put an app's folder: priv's rule, for
// showing before it happens.
func (m *Model) landsIn(bk priv.BackupInfo) string {
	if bk.Folder == "" {
		return ""
	}
	if _, err := os.Stat(filepath.Dir(bk.Folder)); err == nil {
		return bk.Folder
	}
	return filepath.Join(m.appsDir(), filepath.Base(bk.Folder))
}

// settingsCopy is the newest copy of opsie's settings in the folder.
func (m *Model) settingsCopy() string {
	names, _ := os.ReadDir(m.backupDir())
	newest := ""
	for _, n := range names {
		if settingsRe.MatchString(n.Name()) && n.Name() > newest {
			newest = n.Name()
		}
	}
	if newest == "" {
		return ""
	}
	return filepath.Join(m.backupDir(), newest)
}

// haveKeyFor reports whether the key on this server opens a backup.
func (m *Model) haveKeyFor(recipient string) bool {
	_, pub, have := m.keyFile()
	return have && (recipient == "" || slices.Contains(pub, recipient))
}

func (m *Model) reportLines(screen string, b *detailBuilder) {
	if m.moving().screen != screen || len(m.moving().report) == 0 {
		return
	}
	t := m.th
	b.para = true
	b.row(t.bold.Render("Last time"))
	for _, l := range m.moving().report {
		for i, line := range wrap(l.text, max(b.inner-2, 8)) {
			lead := "  "
			if i == 0 {
				lead = t.statusIcon(l.status) + " "
			}
			b.body = append(b.body, lead+line)
		}
	}
}

// packAllDetail is the screen for the server that's being left.
func (m *Model) packAllDetail(b *detailBuilder) detail {
	t := m.th
	plain := lipgloss.NewStyle()
	cfg := m.env.Config.Backup
	var apps []string
	for _, p := range m.projects() {
		if p.name != "" {
			apps = append(apps, p.name)
		}
	}
	b.text("Backs up every app, one after the other, so all of them can be brought back on another server: before moving to a new one, or just to have it all somewhere else.", plain)
	if len(apps) == 0 {
		b.text("There are no apps running here to back up.", t.subtle)
		return detail{title: "Back up everything", body: b.body}
	}
	b.kv("Apps", strconv.Itoa(len(apps))+t.dim.Render(" · "+strings.Join(apps, ", ")))
	b.kv("Saved in", tildePath(m.backupDir()))
	if n, total := m.backupsTotal(""); n > 0 {
		b.kv("Backups here", human.Bytes(total)+t.dim.Render(" · "+human.Plural(n, "backup", "backups")+" · "+human.Bytes(m.bk.b.Free)+" free"))
	}
	if offsite.Configured(cfg) {
		b.kv("Copies to", strings.Join(offsite.Where(cfg), ", "))
	} else {
		b.kv("Copies to", "only this server")
		b.text("With nowhere to copy them, the backups stay on this server, and you carry them over yourself. Set a place under Off-server copies and they're sent there as they're made.", t.warnText)
	}
	if cfg.EncryptTo != "" {
		b.kv("Encrypted", "yes · "+shortKey(cfg.EncryptTo))
		b.text("opsie's own settings go along too, encrypted like the rest: alerts, thresholds, and where the copies go.", t.subtle)
	} else {
		b.kv("Encrypted", "no")
		b.text("opsie's own settings hold tokens and keys, so they only go along encrypted. Turn on Encryption first to take them with you; otherwise you set opsie up again on the other server.", t.warnText)
	}
	m.reportLines("packall", b)
	stop := "Each app stops while it's copied and starts again after, so databases are saved whole."
	if !cfg.Stop {
		stop = "The apps keep running while they're copied, as your settings say; a database that's writing may need repairing after a restore."
	}
	fixes := []health.Fix{{Label: "Back up " + human.Plural(len(apps), "app", "apps"), Special: "move:pack", Danger: true,
		Confirm: "Back up " + human.Plural(len(apps), "app", "apps") + " now, one after the other? " + stop}}
	return detail{title: "Back up everything", headline: headline(t, health.Info, human.Plural(len(apps), "app", "apps"), ""), body: b.body, fixes: fixes}
}

// restoreAllDetail is the screen for the server that's new.
func (m *Model) restoreAllDetail(b *detailBuilder) detail {
	t := m.th
	plain := lipgloss.NewStyle()
	cfg := m.env.Config.Backup
	b.text("Brings every app back from its newest backup: on a new server, or after losing what was here. An app that's already here is left alone.", plain)
	if m.bk.err != "" {
		b.text(m.bk.err, t.warnText)
		return detail{title: "Restore from backups", body: b.body}
	}
	if m.bk.loading || m.bk.at.IsZero() {
		b.text("Looking for backups…", t.dim)
		return detail{title: "Restore from backups", body: b.body}
	}
	var fixes []health.Fix
	var todo []priv.BackupInfo
	var need uint64
	needKey := false
	b.kv("Backups in", tildePath(m.backupDir()))
	found := m.newestBackups()
	if len(found) == 0 {
		b.text("There are no backups in that folder yet. Fetch them from where they were copied, or put them there yourself.", t.subtle)
	}
	for _, bk := range found {
		what := m.backupWhen(bk.Created) + " · " + human.Bytes(bk.Size)
		if bk.Host != "" && bk.Host != m.snap.Host.Hostname {
			what += " · from " + bk.Host
		}
		switch {
		case m.appHere(bk):
			b.row(t.statusIcon(health.OK) + " " + bk.Project + t.dim.Render("  already here"))
			continue
		case bk.Folder == "":
			// No description beside it, so what's in it isn't known until
			// it's opened.
			what += " · no description"
		}
		todo = append(todo, bk)
		need += bk.Bytes
		b.row(t.statusIcon(health.Info) + " " + bk.Project + t.dim.Render("  "+what))
		if to := m.landsIn(bk); to != "" {
			b.body = append(b.body, t.dim.Render(fit("  goes to "+tildePath(to), b.inner)))
		}
		if bk.Encrypted() && !m.haveKeyFor(bk.Recipient) {
			needKey = true
		}
	}
	if len(todo) > 0 {
		if need > 0 {
			b.kv("Needs", "about "+human.Bytes(need)+t.dim.Render(" · "+human.Bytes(m.bk.b.Free)+" free where backups are"))
		}
		switch {
		case m.facts.NoDocker:
			b.text("Docker isn't installed on this server. Install it first; the apps can't start without it.", t.warnText)
		default:
			b.text("The tunnel, if one of these is cloudflared, is restored but not started: while it still runs on the old server, starting it here would send visitors to both. Start it from the Docker tab when you're ready to switch.", t.subtle)
			restore := health.Fix{Label: "Restore " + human.Plural(len(todo), "app", "apps"), Special: "move:restore", Danger: true,
				Confirm: "Restore " + human.Plural(len(todo), "app", "apps") + " from their newest backups and start them? Apps already here aren't touched."}
			if needKey {
				b.text("Some of these are encrypted with a key that isn't on this server, so restoring asks you to paste the secret key, once.", t.subtle)
				restore.Input = &health.Input{Prompt: "Secret key", Arg: "identity", Kind: "agekey", Secret: true, Placeholder: "AGE-SECRET-KEY-1…"}
			}
			fixes = append(fixes, restore)
		}
	}
	for _, src := range offsite.Sources(cfg) {
		fixes = append(fixes, health.Fix{Label: "Fetch from " + offsite.SourceName(cfg, src), Special: "move:fetch:" + src})
	}
	if len(offsite.Sources(cfg)) == 0 {
		b.text("To fetch backups from S3 or over SSH, say where they are under Off-server copies in Settings. Over SSH opsie uses your own key, and only connects to a machine you've connected to before.", t.subtle)
	}
	if file := m.settingsCopy(); file != "" {
		b.kv("opsie's settings", "a copy from "+settingsWhen(file))
		back := health.Fix{Label: "Bring back opsie's settings", Special: "move:settings", Danger: true,
			Confirm: "Replace this server's opsie settings with the copy from " + settingsWhen(file) + "? The settings here now are kept beside them, as config.toml.before-restore."}
		if _, _, have := m.keyFile(); !have {
			back.Input = &health.Input{Prompt: "Secret key", Arg: "identity", Kind: "agekey", Secret: true, Placeholder: "AGE-SECRET-KEY-1…"}
		}
		fixes = append(fixes, back)
	}
	if m.moving().key != "" {
		b.text("The secret key you pasted isn't kept on this server. Keep it here and restores, and checks of the scheduled backups, work on their own.", t.subtle)
		fixes = append(fixes, health.Fix{Label: "Keep the key on this server", Special: "move:keepkey"})
	}
	m.reportLines("restoreall", b)
	if m.moving().screen == "restoreall" && len(m.moving().report) > 0 {
		b.text("Not brought over by a restore: folders an app mounts from outside its own, Docker itself, the firewall, SSH settings and system services. Images an app builds are built again from its folder, and one tagged latest may be newer than the old server ran.", t.dim)
	}
	word := human.Plural(len(todo), "app", "apps") + " to restore"
	if len(todo) == 0 {
		word = "nothing to restore"
	}
	return detail{title: "Restore from backups", headline: headline(t, health.Info, word, ""), body: b.body, fixes: fixes}
}

func settingsWhen(file string) string {
	mm := settingsRe.FindStringSubmatch(filepath.Base(file))
	if mm == nil {
		return "an unknown time"
	}
	at, err := time.ParseInLocation("20060102-150405", mm[1], time.Local)
	if err != nil {
		return "an unknown time"
	}
	return at.Format("2 Jan 15:04")
}

// runMove starts one of the long runs, with the popup showing it work.
func (m *Model) runMove(screen string, work func() moveDoneMsg) tea.Cmd {
	if m.modal != nil {
		m.modal.stage, m.modal.err, m.modal.started = stageRunning, "", time.Now()
	}
	m.moving().say("")
	return func() tea.Msg {
		msg := work()
		msg.screen = screen
		return msg
	}
}

func (m *Model) moveDone(msg moveDoneMsg) tea.Cmd {
	m.moving().say("")
	m.moving().screen, m.moving().report = msg.screen, msg.report
	if msg.key != "" {
		m.moving().key = msg.key
	}
	if msg.reload {
		cfg, err := config.Load(m.env.Paths.ConfigFile())
		m.env.Config, m.env.ConfigErr = cfg, err
		m.applyTheme()
		m.evaluate()
	}
	if m.modal != nil {
		m.modal.stage = stageView
	}
	kind := flashOK
	for _, l := range msg.report {
		if l.status == health.Crit {
			kind = flashError
		}
	}
	// Everything is read again, and then the outcome is said, so it isn't
	// replaced by the word that a check is running.
	m.gathering = false
	facts.Invalidate()
	again := tea.Batch(m.loadBackups(true), m.gather(), m.tabPoll())
	return tea.Batch(again, m.setFlash(msg.flash, kind, 8*time.Second))
}

// packAll backs up every app, copies each off the server, and sends
// opsie's settings along.
func (m *Model) packAll() tea.Cmd {
	if m.admin == nil || !m.admin.Alive() {
		return m.setFlash("Admin access is locked; unlock it first", flashError, 5*time.Second)
	}
	var apps []string
	for _, p := range m.projects() {
		if p.name != "" {
			apps = append(apps, p.name)
		}
	}
	admin, cfg, dir, version := m.admin, m.env.Config.Backup, m.backupDir(), m.env.Version
	logPath, cfgFile := m.env.Paths.AuditLog(), m.env.Paths.ConfigFile()
	stop := ""
	if cfg.Stop {
		stop = "1"
	}
	move := m.moving()
	return m.runMove("packall", func() moveDoneMsg {
		ctx, cancel := context.WithTimeout(context.Background(), moveTimeout)
		defer cancel()
		var report []moveLine
		failed := 0
		for i, app := range apps {
			move.say(fmt.Sprintf("%s (%d of %d)", app, i+1, len(apps)))
			args := map[string]string{"project": app, "dir": dir, "keep": strconv.Itoa(cfg.Keep), "recipient": cfg.EncryptTo, "stop": stop, "version": version}
			var info priv.BackupInfo
			err := admin.Call(ctx, "backup.create", args, &info)
			entry := audit.Entry{Action: "backup.create", Args: args, Result: "ok"}
			if err != nil {
				entry.Result, entry.Error = "failed", err.Error()
			}
			_ = audit.Log(logPath, entry)
			if err != nil && info.Path == "" {
				failed++
				report = append(report, moveLine{health.Crit, app + ": " + err.Error()})
				continue
			}
			line := moveLine{health.OK, app + ": backed up, " + human.Bytes(info.Size)}
			if err != nil {
				// Backed up, but the app didn't start again.
				failed++
				line = moveLine{health.Crit, app + ": " + err.Error()}
			}
			if offsite.Configured(cfg) {
				move.say(fmt.Sprintf("%s (%d of %d): copying off the server", app, i+1, len(apps)))
				files := []string{info.Path}
				if side := offsite.Sidecar(info.Path); side != "" {
					files = append(files, side)
				}
				if cerr := offsite.Copy(ctx, cfg, files, ""); cerr != nil {
					failed++
					line = moveLine{health.Crit, app + ": backed up, but " + cerr.Error()}
				} else if err == nil {
					line.text += ", copied off the server"
				}
			}
			report = append(report, line)
			if len(info.Skipped) > 0 {
				report = append(report, moveLine{health.Warn, app + ": not in the backup, mounted from outside its folder: " + strings.Join(info.Skipped, ", ")})
			}
		}
		if cfg.EncryptTo != "" {
			move.say("opsie's settings")
			if line, err := packSettings(ctx, cfg, cfgFile, dir); err != nil {
				failed++
				report = append(report, moveLine{health.Crit, "opsie's settings: " + err.Error()})
			} else {
				report = append(report, moveLine{health.OK, line})
			}
		} else {
			report = append(report, moveLine{health.Info, "opsie's settings weren't included: they only go along encrypted."})
		}
		flash := "Backed up " + human.Plural(len(apps)-failed, "app", "apps")
		if failed > 0 {
			flash = human.Plural(failed, "step", "steps") + " failed; see the list"
		}
		return moveDoneMsg{report: report, flash: flash}
	})
}

// packSettings saves an encrypted copy of config.toml among the backups
// and sends it where they go.
func packSettings(ctx context.Context, cfg config.Backup, cfgFile, dir string) (string, error) {
	data, err := os.ReadFile(cfgFile)
	if err != nil {
		return "", err
	}
	sealed, err := backup.Seal(data, cfg.EncryptTo)
	if err != nil {
		return "", err
	}
	file := filepath.Join(dir, "opsie-settings-"+time.Now().Format("20060102-150405")+".toml.age")
	if err := os.WriteFile(file, sealed, 0o600); err != nil {
		return "", err
	}
	// The last few are kept, like the backups.
	var old []string
	names, _ := os.ReadDir(dir)
	for _, n := range names {
		if settingsRe.MatchString(n.Name()) {
			old = append(old, n.Name())
		}
	}
	sort.Strings(old)
	for len(old) > keptSettings {
		os.Remove(filepath.Join(dir, old[0]))
		old = old[1:]
	}
	if offsite.Configured(cfg) {
		if err := offsite.Copy(ctx, cfg, []string{file}, ""); err != nil {
			return "", fmt.Errorf("saved, but %w", err)
		}
		return "opsie's settings: saved and copied off the server", nil
	}
	return "opsie's settings: saved", nil
}

// fetchBackups brings the newest backup of each app, and of opsie's
// settings, from an off-server copy into the backups folder.
func (m *Model) fetchBackups(source string) tea.Cmd {
	cfg, dir := m.env.Config.Backup, m.backupDir()
	move := m.moving()
	return m.runMove("restoreall", func() moveDoneMsg {
		ctx, cancel := context.WithTimeout(context.Background(), moveTimeout)
		defer cancel()
		where := offsite.SourceName(cfg, source)
		fail := func(err error) moveDoneMsg {
			return moveDoneMsg{report: []moveLine{{health.Crit, err.Error()}}, flash: "Couldn't fetch the backups"}
		}
		move.say("reading what's at " + where)
		all, err := offsite.List(ctx, cfg, source, "")
		if err != nil {
			return fail(err)
		}
		want := offsite.Newest(all)
		if len(want) == 0 {
			return moveDoneMsg{report: []moveLine{{health.Warn, "There are no opsie backups at " + where + "."}}, flash: "No backups there"}
		}
		// The folder is yours and private, like the backups in it.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fail(err)
		}
		var report []moveLine
		got, had, failed := 0, 0, 0
		for i, r := range want {
			if st, err := os.Stat(filepath.Join(dir, r.Name)); err == nil && (r.Size < 0 || st.Size() == r.Size) {
				had++
				continue
			}
			size := ""
			if r.Size > 0 {
				size = ", " + human.Bytes(uint64(r.Size))
			}
			move.say(fmt.Sprintf("%s (%d of %d%s)", r.Name, i+1, len(want), size))
			if err := offsite.Fetch(ctx, cfg, r, dir, ""); err != nil {
				failed++
				report = append(report, moveLine{health.Crit, r.Name + ": " + err.Error()})
				continue
			}
			got++
		}
		summary := "Fetched " + human.Plural(got, "file", "files") + " from " + where
		if had > 0 {
			summary += "; " + human.Plural(had, "was", "were") + " here already"
		}
		st := health.OK
		if failed > 0 {
			st = health.Warn
		}
		report = append([]moveLine{{st, summary + "."}}, report...)
		flash := "Fetched " + human.Plural(got, "file", "files")
		if failed > 0 {
			flash = human.Plural(failed, "file", "files") + " couldn't be fetched"
		}
		return moveDoneMsg{report: report, flash: flash}
	})
}

// restoreAll restores every app that isn't here from its newest backup.
// identity is a pasted secret key, for backups the key file doesn't open.
func (m *Model) restoreAll(identity string) tea.Cmd {
	if m.admin == nil || !m.admin.Alive() {
		return m.setFlash("Admin access is locked; unlock it first", flashError, 5*time.Second)
	}
	type job struct {
		bk   priv.BackupInfo
		args map[string]string
	}
	var jobs []job
	for _, bk := range m.newestBackups() {
		if m.appHere(bk) {
			continue
		}
		args := map[string]string{"project": bk.Project, "file": bk.Path, "dir": m.backupDir(), "apps": m.appsDir(), "hold": "1"}
		if bk.Encrypted() {
			if m.haveKeyFor(bk.Recipient) || identity == "" {
				args["keyfile"] = m.env.Paths.BackupKey()
			} else {
				args["identity"] = identity
			}
		}
		jobs = append(jobs, job{bk, args})
	}
	admin, logPath := m.admin, m.env.Paths.AuditLog()
	move := m.moving()
	return m.runMove("restoreall", func() moveDoneMsg {
		ctx, cancel := context.WithTimeout(context.Background(), moveTimeout)
		defer cancel()
		var report []moveLine
		done, failed, usedKey := 0, 0, false
		for i, j := range jobs {
			app := j.bk.Project
			move.say(fmt.Sprintf("%s (%d of %d)", app, i+1, len(jobs)))
			var res priv.RestoreResult
			err := admin.Call(ctx, "backup.restore", j.args, &res)
			entry := audit.Entry{Action: "backup.restore", Args: redact(j.args), Result: "ok"}
			if err != nil {
				entry.Result, entry.Error = "failed", err.Error()
			}
			_ = audit.Log(logPath, entry)
			switch {
			case err != nil && res.Folder == "":
				failed++
				text := err.Error()
				if errors.Is(err, backup.ErrWrongKey) || strings.Contains(text, backup.ErrWrongKey.Error()) {
					text = "the secret key doesn't open this backup"
				}
				report = append(report, moveLine{health.Crit, app + ": " + text})
				continue
			case err != nil:
				// In place, but it didn't start.
				failed++
				report = append(report, moveLine{health.Crit, app + ": " + err.Error()})
			case res.Held:
				report = append(report, moveLine{health.Warn, app + ": restored to " + tildePath(res.Folder) + " and not started, because it's the tunnel. Start it from the Docker tab once the old server's is stopped."})
			case res.Started:
				report = append(report, moveLine{health.OK, app + ": restored to " + tildePath(res.Folder) + " and started"})
			default:
				report = append(report, moveLine{health.Warn, app + ": restored to " + tildePath(res.Folder) + ", but there's no compose file in it to start it with"})
			}
			done++
			if j.args["identity"] != "" {
				usedKey = true
			}
			if len(j.bk.Skipped) > 0 {
				report = append(report, moveLine{health.Warn, app + ": wasn't in the backup, so isn't here: " + strings.Join(j.bk.Skipped, ", ")})
			}
		}
		out := moveDoneMsg{report: report, flash: "Restored " + human.Plural(done, "app", "apps")}
		if failed > 0 {
			out.flash = human.Plural(failed, "app", "apps") + " couldn't be restored; see the list"
		}
		if usedKey {
			out.key = identity
		}
		return out
	})
}

// restoreSettings puts back the copy of opsie's settings that came with
// the backups. What was here is kept beside it.
func (m *Model) restoreSettings(identity string) tea.Cmd {
	file, cfgFile := m.settingsCopy(), m.env.Paths.ConfigFile()
	if identity == "" {
		identity, _, _ = m.keyFile()
	}
	return m.runMove("restoreall", func() moveDoneMsg {
		fail := func(text string) moveDoneMsg {
			return moveDoneMsg{report: []moveLine{{health.Crit, "opsie's settings: " + text}}, flash: "Couldn't bring back the settings"}
		}
		sealed, err := os.ReadFile(file)
		if err != nil {
			return fail(err.Error())
		}
		data, err := backup.Open(sealed, identity)
		switch {
		case errors.Is(err, backup.ErrWrongKey), errors.Is(err, backup.ErrNeedKey):
			return fail("the secret key doesn't open them")
		case err != nil:
			return fail(err.Error())
		}
		var check config.Config
		if _, err := toml.Decode(string(data), &check); err != nil {
			return fail("the copy isn't a settings file opsie can read")
		}
		if err := os.MkdirAll(filepath.Dir(cfgFile), 0o700); err != nil {
			return fail(err.Error())
		}
		if old, err := os.ReadFile(cfgFile); err == nil {
			if err := os.WriteFile(cfgFile+".before-restore", old, 0o600); err != nil {
				return fail(err.Error())
			}
		}
		tmp := cfgFile + ".opsie-tmp"
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return fail(err.Error())
		}
		if err := os.Rename(tmp, cfgFile); err != nil {
			return fail(err.Error())
		}
		return moveDoneMsg{reload: true, flash: "Brought back opsie's settings",
			report: []moveLine{{health.OK, "opsie's settings are the ones from " + settingsWhen(file) + ". What was here is in " + tildePath(cfgFile+".before-restore") + "."}}}
	})
}

// keepKey saves the pasted secret key in this server's key file.
func (m *Model) keepKey() tea.Cmd {
	secret := strings.TrimSpace(m.moving().key)
	if secret == "" {
		return nil
	}
	err := os.MkdirAll(m.env.Paths.Config, 0o700)
	if err == nil {
		var f *os.File
		f, err = os.OpenFile(m.env.Paths.BackupKey(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = f.WriteString("# opsie backup key, kept " + time.Now().Format("2 Jan 2006 15:04") + " after a restore\n" + secret + "\n")
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
	}
	if err != nil {
		return m.setFlash("Couldn't keep the key: "+err.Error(), flashError, 6*time.Second)
	}
	m.moving().key = ""
	return m.setFlash("The secret key is kept in "+tildePath(m.env.Paths.BackupKey()), flashOK, 6*time.Second)
}

// moving is the state of the two screens.
func (m *Model) moving() *moveState {
	if m.mv == nil {
		m.mv = &moveState{}
	}
	return m.mv
}
