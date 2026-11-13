package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aloglu/opsie/internal/audit"
	"github.com/aloglu/opsie/internal/facts"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/offsite"
	"github.com/aloglu/opsie/internal/priv"
	"github.com/aloglu/opsie/internal/sites"
	"github.com/aloglu/opsie/internal/update"
)

// fixTimeout is how long the dashboard waits for an action; the helper
// has its own, shorter limits per operation.
const fixTimeout = 35 * time.Minute

// redact hides a pasted secret key: it goes to the helper, never into
// the log.
func redact(args map[string]string) map[string]string {
	if _, ok := args["identity"]; !ok {
		return args
	}
	out := map[string]string{}
	for k, v := range args {
		out[k] = v
	}
	out["identity"] = "(hidden)"
	return out
}

// runFix carries out a fix.
func (m *Model) runFix(f health.Fix) tea.Cmd {
	switch {
	case f.Special == "unlock":
		m.modal = nil
		cmd := exec.Command("sudo", "-v", "-p", "\n  opsie needs your sudo password for admin access: ")
		return tea.ExecProcess(cmd, func(err error) tea.Msg { return unlockDoneMsg{err: err} })
	case f.Special == "update":
		m.modal = nil
		exe, err := update.Executable()
		if err != nil {
			return m.setFlash(err.Error(), flashError, 5*time.Second)
		}
		cmd := exec.Command(exe, "update", "--from-app")
		return tea.ExecProcess(cmd, func(err error) tea.Msg { return selfUpdatedMsg{err: err} })
	case f.Special == "update:check":
		return m.checkVersion()
	case f.Special == "notify:test":
		return m.testAlert(f)
	case f.Special == "notify:weekly":
		return m.sendWeekly(f)
	case f.Special == "logs" && f.Args["unit"] != "":
		return m.openUnitLogs(f.Args["unit"])
	case f.Special == "logs":
		return m.openLogs(f.Args["container"])
	case strings.HasPrefix(f.Special, "open:") && f.Special != "open:prune":
		// open:<tab>:<item>
		name, id, _ := strings.Cut(strings.TrimPrefix(f.Special, "open:"), ":")
		for _, tb := range tabs {
			if strings.EqualFold(tb.name, name) {
				cmd := m.switchTab(tb.id)
				return tea.Batch(cmd, m.openItem(tb.id, id))
			}
		}
		return nil
	case f.Special == "motd:on" || f.Special == "motd:off":
		return m.loginSummary(f.Special == "motd:on")
	case f.Special == "config:set" || f.Special == "config:unset" || f.Special == "config:add" || f.Special == "config:remove":
		return m.saveSetting(f)
	case strings.HasPrefix(f.Special, "explore:"):
		return m.explore(strings.TrimPrefix(f.Special, "explore:"), false)
	case strings.HasPrefix(f.Special, "explore!:"):
		return m.explore(strings.TrimPrefix(f.Special, "explore!:"), true)
	case f.Special == "clean":
		m.modal = &modal{key: "clean"}
		return m.loadClean()
	case f.Special == "speedtest:run":
		return m.runSpeedTest()
	case f.Special == "agents:refresh":
		m.modal = nil
		return tea.Batch(m.agentsPoll(true), m.setFlash("Reading the logs again…", flashInfo, 2*time.Second))
	case f.Special == "sites:recheck":
		sites.Forget()
		m.modal = nil
		return m.refresh()
	case strings.HasPrefix(f.Special, "backup:"):
		m.modal = &modal{key: f.Special}
		return m.loadBackups(false)
	case strings.HasPrefix(f.Special, "backupfile:"):
		m.modal = &modal{key: "bfile:" + strings.TrimPrefix(f.Special, "backupfile:")}
		return m.loadBackups(false)
	case strings.HasPrefix(f.Special, "backupcopy:"):
		m.modal = nil
		return tea.Batch(m.setFlash("Copying off the server…", flashOK, 4*time.Second), m.copyOff(strings.TrimPrefix(f.Special, "backupcopy:")))
	case f.Special == "backupkey:new":
		return m.newBackupKey()
	case f.Special == "backupkey:remove":
		return m.removeBackupKey()
	case f.Special == "backupkey:show":
		m.modal = &modal{key: "backupkey"}
		return nil
	case f.Special == "folder":
		return m.folderShell(f.Args["dir"])
	case f.Special == "shell":
		return m.shell(f.Args["container"])
	case strings.HasPrefix(f.Special, "tab:"):
		for _, tb := range tabs {
			if strings.EqualFold("tab:"+tb.name, f.Special) {
				return m.switchTab(tb.id)
			}
		}
		return nil
	case f.Special == "open:prune":
		m.tab = tabDocker
		return m.openItem(tabDocker, "prune")
	}
	if m.admin == nil || !m.admin.Alive() {
		return m.setFlash("Admin access is locked; unlock it first", flashError, 5*time.Second)
	}
	if m.modal != nil {
		m.modal.stage, m.modal.err, m.modal.started = stageRunning, "", time.Now()
	}
	admin, logPath := m.admin, m.env.Paths.AuditLog()
	logged := redact(f.Args)
	return func() tea.Msg {
		_ = audit.Log(logPath, audit.Entry{Action: f.Op, Args: logged, Result: "requested"})
		ctx, cancel := context.WithTimeout(context.Background(), fixTimeout)
		defer cancel()
		var out json.RawMessage
		err := admin.Call(ctx, f.Op, f.Args, &out)
		entry := audit.Entry{Action: f.Op, Args: logged, Result: "ok"}
		if err != nil {
			entry.Result, entry.Error = "failed", err.Error()
		}
		_ = audit.Log(logPath, entry)
		return fixDoneMsg{fix: f, out: out, err: err}
	}
}

func (m *Model) fixDone(msg fixDoneMsg) tea.Cmd {
	if msg.err != nil {
		if errors.Is(msg.err, priv.ErrHelperGone) {
			m.admin, m.adminErr = nil, msg.err
		}
		if m.modal != nil {
			m.modal.stage = stageView
			m.modal.err = msg.err.Error()
		}
		return m.setFlash("Failed: "+msg.err.Error(), flashError, 6*time.Second)
	}
	done := msg.fix.Done
	switch msg.fix.Op {
	case "docker.prune", "journal.vacuum", "apt.clean", "apt.autoremove":
		m.forgetDisk()
	case "backup.verify":
		var v priv.VerifyResult
		if json.Unmarshal(msg.out, &v) == nil {
			switch {
			case v.Opened:
				done = "The backup is good: it opens and reads back in full, " + human.Plural(v.Files, "file", "files") + ", " + human.Bytes(v.Bytes)
			case v.Intact:
				done = "The file is intact: it matches the checksum made when it was written. Its key isn't here to open it"
			}
		}
	case "compose.bindlocal":
		var kept string
		if json.Unmarshal(msg.out, &kept) == nil && kept != "" {
			done += "; the old compose file is " + filepath.Base(kept)
		}
	case "backup.create", "backup.restore", "backup.delete", "backup.discard":
		m.bk = backupsState{}
		m.forgetDisk()
	}
	var copyCmd tea.Cmd
	if msg.fix.Op == "backup.create" && offsite.Configured(m.env.Config.Backup) {
		var info priv.BackupInfo
		if json.Unmarshal(msg.out, &info) == nil && info.Path != "" {
			done += "; copying it off the server…"
			copyCmd = m.copyOff(info.Path)
		}
	}
	if msg.fix.Op == "docker.prune" {
		var freed uint64
		if json.Unmarshal(msg.out, &freed) == nil {
			done = "Freed " + human.Bytes(freed)
		}
		m.dock.dfAt = time.Time{} // measure again
	}
	// Removing a container or stopping a project leaves the detail view
	// with nothing to show, so close it after any action.
	m.modal = nil
	m.gathering = false
	facts.Invalidate()
	return tea.Batch(m.setFlash(done, flashOK, 5*time.Second), m.gather(), m.tabPoll(), copyCmd)
}

// shell opens an interactive shell in a container, handing the terminal
// over until it exits.
func (m *Model) shell(container string) tea.Cmd {
	if !priv.ValidContainer(container) {
		return nil
	}
	m.modal = nil
	_ = audit.Log(m.env.Paths.AuditLog(), audit.Entry{Action: "docker.shell", Args: map[string]string{"container": container}, Result: "requested"})
	script := `printf '\n  Shell in %s. Type exit to go back to opsie.\n\n' "$1"
exec sudo docker exec -it "$1" sh -c 'if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi'`
	cmd := exec.Command("sh", "-c", script, "opsie-shell", container)
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return shellDoneMsg{container: container, err: err} })
}

// tabPoll refreshes whatever the current tab shows on its own schedule.
func (m *Model) tabPoll() tea.Cmd {
	switch {
	case m.tab == tabDocker:
		return m.pollDocker()
	case m.wantsServices():
		return m.pollServices()
	case m.tab == tabProcs:
		return m.pollProcs()
	case m.tab == tabStorage:
		return m.storagePoll()
	case m.tab == tabNetwork:
		return m.networkPoll()
	case m.tab == tabAgents:
		return m.agentsPoll(false)
	}
	return nil
}

// refresh checks everything again now.
func (m *Model) refresh() tea.Cmd {
	m.gathering = false
	return tea.Batch(m.gather(), m.tabPoll(), m.setFlash("Checking…", flashInfo, 2*time.Second))
}
