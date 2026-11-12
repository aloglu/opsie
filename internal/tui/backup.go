package tui

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/backup"
	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/offsite"
	"github.com/aloglu/opsie/internal/priv"
)

type backupsState struct {
	loading bool
	at      time.Time
	b       priv.Backups
	err     string
}

type backupsMsg struct {
	b   priv.Backups
	err error
}

// backupDir is where backups go.
func (m *Model) backupDir() string {
	home, _ := os.UserHomeDir()
	if d := strings.TrimSpace(m.env.Config.Backup.Dir); d != "" {
		return config.Expand(d, home)
	}
	return m.env.Paths.Backups
}

func (m *Model) loadBackups(again bool) tea.Cmd {
	s := &m.bk
	if s.loading || (!again && !s.at.IsZero() && time.Since(s.at) < 30*time.Second) {
		return nil
	}
	if m.admin == nil || !m.admin.Alive() {
		m.bk = backupsState{err: "Backups need admin access, which is locked: Docker's volumes belong to root.", at: time.Now()}
		return nil
	}
	s.loading = true
	admin, dir := m.admin, m.backupDir()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var b priv.Backups
		err := admin.Call(ctx, "backup.list", map[string]string{"dir": dir}, &b)
		return backupsMsg{b: b, err: err}
	}
}

func (m *Model) backupsUpdated(msg backupsMsg) {
	m.bk = backupsState{b: msg.b, at: time.Now()}
	if msg.err != nil {
		m.bk.err = msg.err.Error()
	}
}

// keyFile reads the secret keys kept on this server, and their public
// keys.
func (m *Model) keyFile() (secret string, public []string, ok bool) {
	data, err := os.ReadFile(m.env.Paths.BackupKey())
	if err != nil {
		return "", nil, false
	}
	return string(data), backup.PublicKeys(string(data)), true
}

// newBackupKey makes a key pair, adds the secret key to the key file and
// encrypts backups to the public key from now on. Older keys stay in the
// file, so older backups can still be restored.
func (m *Model) newBackupKey() tea.Cmd {
	secret, public, err := backup.NewKey()
	if err == nil {
		err = os.MkdirAll(m.env.Paths.Config, 0o700)
	}
	if err == nil {
		var f *os.File
		f, err = os.OpenFile(m.env.Paths.BackupKey(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = f.WriteString("# opsie backup key, made " + time.Now().Format("2 Jan 2006 15:04") + "\n# public key: " + public + "\n" + secret + "\n")
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
	}
	if err == nil {
		err = config.Set(m.env.Paths.ConfigFile(), "backup.encrypt_to", public)
	}
	if err != nil {
		return m.setFlash("Couldn't make a key: "+err.Error(), flashError, 6*time.Second)
	}
	cfg, cerr := config.Load(m.env.Paths.ConfigFile())
	m.env.Config, m.env.ConfigErr = cfg, cerr
	m.modal = &modal{key: "backupkey"}
	return m.setFlash("Made a backup key. Save the secret key somewhere safe", flashOK, 6*time.Second)
}

func (m *Model) removeBackupKey() tea.Cmd {
	if err := os.Remove(m.env.Paths.BackupKey()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return m.setFlash("Couldn't remove the key: "+err.Error(), flashError, 6*time.Second)
	}
	m.modal = nil
	return m.setFlash("Removed the secret key from this server", flashOK, 5*time.Second)
}

// encryptionWord says how backups are encrypted, for the Settings row.
func (m *Model) encryptionWord() string {
	if m.env.Config.Backup.EncryptTo == "" {
		return "off"
	}
	if _, pub, ok := m.keyFile(); ok && slices.Contains(pub, m.env.Config.Backup.EncryptTo) {
		return "on · key on this server"
	}
	return "on"
}

// shortKey shows the start and end of a long key.
func shortKey(k string) string {
	if len(k) <= 20 {
		return k
	}
	return k[:9] + "…" + k[len(k)-6:]
}

// chunks splits s into lines of at most w, for keys that have to be
// shown whole.
func chunks(s string, w int) []string {
	w = max(w, 8)
	var out []string
	for len(s) > w {
		out = append(out, s[:w])
		s = s[w:]
	}
	return append(out, s)
}

func (m *Model) encryptionDetail(b *detailBuilder) detail {
	t := m.th
	plain := lipgloss.NewStyle()
	to := m.env.Config.Backup.EncryptTo
	_, pub, haveFile := m.keyFile()
	if to == "" {
		b.text("Backups hold everything your apps keep: databases, uploads, settings and any passwords in them. Encrypted, a backup can only be opened with its secret key, so a copy that ends up somewhere else, like cloud storage, is safe.", plain)
		b.text("opsie uses age, a widely used encryption tool. Make a key here, or use the public key of one you already have.", t.subtle)
		return detail{title: "Backup encryption", headline: headline(t, health.Info, "off", "backups aren't encrypted"), body: b.body, fixes: []health.Fix{
			{Label: "Make a key", Special: "backupkey:new"},
			{Label: "Use my own key", Special: "config:set", Args: map[string]string{"key": "backup.encrypt_to"},
				Input: &health.Input{Prompt: "Public key", Arg: "value", Kind: "agepub", Placeholder: "age1…"}},
		}}
	}
	b.text("New backups are encrypted. Only the secret key opens them: without it, they can't be restored, by you or anyone.", plain)
	b.kv("Public key", shortKey(to))
	var fixes []health.Fix
	switch {
	case haveFile && slices.Contains(pub, to):
		b.kv("Secret key", "on this server, in "+tildePath(m.env.Paths.BackupKey()))
		b.text("Restores here use it on their own. Keep a copy somewhere else too, like a password manager: if this server is lost, the backups you copied off it need it. Once you have, you can remove it from the server; restores then ask you to paste it.", t.subtle)
		fixes = append(fixes, health.Fix{Label: "Show the secret key", Special: "backupkey:show"},
			health.Fix{Label: "Remove the key from this server", Special: "backupkey:remove", Danger: true,
				Confirm: "Remove the secret key from this server? Only do this if you've saved it somewhere else: without it, encrypted backups can't be restored. Restores will ask you to paste it."})
	case haveFile:
		b.kv("Secret key", "not on this server")
		b.text("The key file here has other keys, not this one. Restoring a backup made with this key asks you to paste its secret key.", t.warnText)
	default:
		b.kv("Secret key", "not on this server")
		b.text("Restoring an encrypted backup asks you to paste its secret key.", t.subtle)
	}
	fixes = append(fixes,
		health.Fix{Label: "Make a new key", Special: "backupkey:new", Danger: true,
			Confirm: "Make a new key and encrypt backups to it from now on? Backups made before still need the old secret key; it stays in the key file if it's there."},
		health.Fix{Label: "Turn encryption off", Special: "config:unset", Args: map[string]string{"key": "backup.encrypt_to"}, Danger: true,
			Confirm: "Stop encrypting new backups? The ones already made stay encrypted and still need their key."})
	return detail{title: "Backup encryption", headline: headline(t, health.OK, "on", ""), body: b.body, fixes: fixes}
}

func (m *Model) backupKeyDetail(b *detailBuilder) detail {
	t := m.th
	secret, _, ok := m.keyFile()
	if !ok {
		b.text("There's no secret key on this server.", t.subtle)
		return detail{title: "Secret key", body: b.body}
	}
	b.text("Anyone with this key can open your backups, and without it nobody can. Save it in a password manager, or copy the file "+tildePath(m.env.Paths.BackupKey())+" off the server.", t.warnText)
	var keys []string
	for _, line := range strings.Split(secret, "\n") {
		if strings.HasPrefix(line, "AGE-SECRET-KEY-") {
			keys = append(keys, line)
		}
	}
	for i, k := range keys {
		b.para = true
		if len(keys) > 1 {
			word := "Older key"
			if i == len(keys)-1 {
				word = "Newest key"
			}
			b.row(t.bold.Render(word))
		}
		if b.para {
			b.body = append(b.body, "")
			b.para = false
		}
		for _, c := range chunks(k, b.inner) {
			b.body = append(b.body, t.bold.Render(c))
		}
	}
	b.text("If it wraps, copy all of it: spaces and line breaks are ignored when you paste it back.", t.dim)
	return detail{title: "Secret key", headline: headline(t, health.Info, "keep it safe", ""), body: b.body}
}

// projectBackups are the backups of one app, newest first.
func (m *Model) projectBackups(project string) []priv.BackupInfo {
	var out []priv.BackupInfo
	for _, b := range m.bk.b.List {
		if b.Project == project {
			out = append(out, b)
		}
	}
	return out
}

func (m *Model) backupWhen(at time.Time) string {
	return at.Local().Format("2 Jan 15:04")
}

func (m *Model) backupDetail(project string, b *detailBuilder) detail {
	t := m.th
	plain := lipgloss.NewStyle()
	title := "Backups of " + project
	var containers []priv.Container
	running := 0
	for _, c := range m.facts.Containers {
		if c.Project == project {
			containers = append(containers, c)
			if c.State == "running" {
				running++
			}
		}
	}
	if len(containers) == 0 {
		b.text("This app has no containers anymore.", t.subtle)
	}
	var folder string
	var volumes, skipped []string
	seen := map[string]bool{}
	for _, c := range containers {
		if folder == "" {
			folder = c.WorkingDir
		}
		for _, mt := range c.Mounts {
			switch {
			case mt.Type == "volume" && mt.Name != "" && !seen[mt.Name]:
				seen[mt.Name] = true
				volumes = append(volumes, mt.Name)
			case mt.Type == "bind" && !seen[mt.Source]:
				seen[mt.Source] = true
				if folder != "" && mt.Source != folder && !strings.HasPrefix(mt.Source, folder+"/") {
					skipped = append(skipped, mt.Source)
				}
			}
		}
	}
	b.text("A backup is one file with the app's folder and its Docker volumes: everything it keeps.", plain)
	if folder != "" {
		b.kv("Folder", tildePath(folder))
	}
	if len(volumes) > 0 {
		b.kv("Volumes", strings.Join(volumes, ", "))
	} else {
		b.kv("Volumes", "none")
	}
	if len(skipped) > 0 {
		b.text("Not in backups: "+strings.Join(skipped, ", ")+", mounted from outside the app's folder. Back those up yourself if they hold data.", t.warnText)
	}
	enc := "off" + t.dim.Render(" · turn it on in Settings")
	if to := m.env.Config.Backup.EncryptTo; to != "" {
		enc = "on" + t.dim.Render(" · "+shortKey(to))
	}
	b.kv("Encryption", enc)
	b.kv("Saved in", tildePath(m.backupDir())+t.dim.Render(" · keeps the last "+human.Plural(m.env.Config.Backup.Keep, "backup", "backups")))
	cfg := m.env.Config.Backup
	if offsite.Configured(cfg) {
		b.kv("Copies to", strings.Join(offsite.Where(cfg), ", "))
		if cfg.EncryptTo == "" {
			b.text("Copies leave the server unencrypted. Turn on encryption in Settings so whoever holds them can't read your data.", t.warnText)
		}
	} else {
		b.kv("Copies to", "only this server"+t.dim.Render(" · set up copies in Settings"))
	}
	nightlyOn := slices.Contains(cfg.Nightly, project)
	nightly := health.Fix{Label: "Back up every night", Special: "config:add", Args: map[string]string{"key": "backup.nightly", "value": project},
		Done: project + " is backed up every night at " + cfg.At}
	if nightlyOn {
		word := "on · " + cfg.At
		for _, r := range m.watcher.Backups {
			if r.Project != project {
				continue
			}
			if r.OK {
				word += t.dim.Render(" · last " + human.Duration(m.now.Sub(r.At)) + " ago")
			} else {
				word += t.dangerText.Render(" · last one failed")
			}
		}
		b.kv("Nightly", word)
		if !m.watcher.Running(m.now) {
			b.text("Nightly backups are run by the background checks, which aren't running.", t.warnText)
		}
		nightly = health.Fix{Label: "Stop nightly backups", Special: "config:remove", Args: map[string]string{"key": "backup.nightly", "value": project},
			Done: project + " isn't backed up every night anymore"}
	}

	var fixes []health.Fix
	if folder != "" {
		args := func(stop string) map[string]string {
			return map[string]string{"project": project, "dir": m.backupDir(), "keep": strconv.Itoa(m.env.Config.Backup.Keep),
				"recipient": m.env.Config.Backup.EncryptTo, "stop": stop, "version": m.env.Version}
		}
		if running > 0 {
			fixes = append(fixes,
				health.Fix{Label: "Back up now", Op: "backup.create", Args: args("1"), Danger: true, Done: "Backed up " + project,
					Confirm: "Stop " + project + " while it's copied, then start it again? Stopping first means databases are saved whole. It's usually down for under a minute; a big app takes longer."},
				health.Fix{Label: "Back up without stopping", Op: "backup.create", Args: args(""), Done: "Backed up " + project})
			b.text("Without stopping, a database that's writing while it's copied may need repairing after a restore.", t.dim)
		} else {
			fixes = append(fixes, health.Fix{Label: "Back up now", Op: "backup.create", Args: args(""), Done: "Backed up " + project})
		}
	}

	if folder != "" {
		fixes = append(fixes, nightly)
	}
	s := m.bk
	b.para = true
	b.row(t.bold.Render("Backups"))
	switch {
	case s.err != "":
		b.text(s.err, t.dangerText)
		if m.admin == nil || !m.admin.Alive() {
			fixes = append([]health.Fix{{Label: "Unlock admin access", Special: "unlock"}}, fixes...)
		}
	case s.at.IsZero() || s.loading:
		b.text("Looking for backups…", t.dim)
	default:
		list := m.projectBackups(project)
		if len(list) == 0 {
			b.text("None yet.", t.subtle)
		}
		for _, bk := range list {
			label := m.backupWhen(bk.Created) + "  " + human.Bytes(bk.Size)
			if bk.Encrypted() {
				label += "  encrypted"
			}
			fixes = append(fixes, health.Fix{Label: label, Special: "backupfile:" + bk.Path})
		}
		if s.b.Free > 0 {
			b.text(human.Bytes(s.b.Free)+" free where backups go.", t.dim)
		}
		for _, a := range s.b.Aside {
			if a.Project != project {
				continue
			}
			b.text("A restore "+human.Duration(m.now.Sub(a.At))+" ago set the app's earlier data aside ("+human.Bytes(a.Size)+"), in case you want it back:", t.subtle)
			for _, p := range a.Paths {
				b.row("  " + tildePath(p))
			}
			fixes = append(fixes, health.Fix{Label: "Delete the set-aside data", Op: "backup.discard", Danger: true,
				Args: map[string]string{"project": project, "dir": m.backupDir()}, Done: "Deleted the set-aside data",
				Confirm: "Delete " + project + "'s data from before the restore (" + human.Bytes(a.Size) + ")? This can't be undone."})
		}
	}
	return detail{title: title, headline: headline(t, health.Info, "backups", ""), body: b.body, fixes: fixes}
}

func (m *Model) backupFileDetail(path string, b *detailBuilder) detail {
	t := m.th
	var bk priv.BackupInfo
	found := false
	for _, x := range m.bk.b.List {
		if x.Path == path {
			bk, found = x, true
		}
	}
	if !found {
		if m.bk.loading || m.bk.at.IsZero() {
			b.text("Looking for backups…", t.dim)
		} else {
			b.text("This backup isn't there anymore.", t.subtle)
		}
		return detail{title: "Backup", body: b.body}
	}
	b.kv("Made", bk.Created.Local().Format("Mon 2 Jan 2006 15:04")+t.dim.Render(" · "+human.Duration(m.now.Sub(bk.Created))+" ago"))
	b.kv("Size", human.Bytes(bk.Size))
	if bk.Folder != "" {
		b.kv("Folder", tildePath(bk.Folder))
	}
	if len(bk.Volumes) > 0 {
		b.kv("Volumes", strings.Join(bk.Volumes, ", "))
	}
	if !bk.Created.IsZero() && bk.Folder != "" {
		b.kv("App was", map[bool]string{true: "stopped while copied", false: "running while copied"}[bk.Stopped])
	}
	b.kv("File", tildePath(bk.Path))
	args := map[string]string{"project": bk.Project, "file": bk.Path, "dir": m.backupDir()}
	restore := health.Fix{Label: "Restore", Op: "backup.restore", Args: args, Danger: true, Done: "Restored " + bk.Project + " from " + m.backupWhen(bk.Created),
		Confirm: "Replace " + bk.Project + "'s folder and volumes with this backup from " + m.backupWhen(bk.Created) + "? The app stops while it's restored and starts again after. What's there now is set aside, not deleted, so you can go back."}
	if bk.Encrypted() {
		_, pub, have := m.keyFile()
		switch {
		case have && (bk.Recipient == "" || slices.Contains(pub, bk.Recipient)):
			b.kv("Encrypted", "yes · the key is on this server")
			args["keyfile"] = m.env.Paths.BackupKey()
		default:
			b.kv("Encrypted", "yes · "+shortKey(bk.Recipient))
			b.text("Its secret key isn't on this server, so restoring asks you to paste it.", t.subtle)
			restore.Input = &health.Input{Prompt: "Secret key", Arg: "identity", Kind: "agekey", Secret: true, Placeholder: "AGE-SECRET-KEY-1…"}
		}
	} else {
		b.kv("Encrypted", "no")
	}
	if len(bk.Skipped) > 0 {
		b.text("Not in this backup: "+strings.Join(bk.Skipped, ", ")+".", t.warnText)
	}
	check := health.Fix{Label: "Check this backup", Op: "backup.verify", Args: map[string]string{"file": bk.Path, "dir": m.backupDir()}}
	if k, ok := args["keyfile"]; ok {
		check.Args["keyfile"] = k
	} else if bk.Encrypted() {
		check.Label = "Check it's intact"
	}
	fixes := []health.Fix{restore, check}
	if offsite.Configured(m.env.Config.Backup) {
		fixes = append(fixes, health.Fix{Label: "Copy off the server", Special: "backupcopy:" + bk.Path})
	}
	fixes = append(fixes, []health.Fix{
		{Label: "Delete this backup", Op: "backup.delete", Args: map[string]string{"file": bk.Path, "dir": m.backupDir()}, Danger: true,
			Done: "Deleted the backup", Confirm: "Delete the backup of " + bk.Project + " from " + m.backupWhen(bk.Created) + "? This can't be undone."},
		{Label: "‹ Backups of " + bk.Project, Special: "backup:" + bk.Project},
	}...)
	return detail{title: "Backup of " + bk.Project, headline: headline(t, health.Info, m.backupWhen(bk.Created), ""), body: b.body, fixes: fixes}
}

type copyDoneMsg struct {
	name string
	err  error
}

// copyOff sends a backup to the off-server destinations. It runs as you,
// with your SSH key.
func (m *Model) copyOff(path string) tea.Cmd {
	b := m.env.Config.Backup
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		defer cancel()
		files := []string{path}
		if side := offsite.Sidecar(path); side != "" {
			files = append(files, side)
		}
		return copyDoneMsg{name: filepath.Base(path), err: offsite.Copy(ctx, b, files, "")}
	}
}

func (m *Model) copyDone(msg copyDoneMsg) tea.Cmd {
	if msg.err != nil {
		return m.setFlash(msg.err.Error(), flashError, 10*time.Second)
	}
	return m.setFlash("Copied "+msg.name+" to "+strings.Join(offsite.Where(m.env.Config.Backup), " and "), flashOK, 6*time.Second)
}
