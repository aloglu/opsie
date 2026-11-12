package priv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// The operations the helper performs, by name. Each one validates its own
// arguments; anything not listed here is refused.
var ops = map[string]func(ctx context.Context, args map[string]string) (any, error){
	"ping": func(context.Context, map[string]string) (any, error) { return "pong", nil },

	// Reading
	"sshd.config":       opSSHDConfig,
	"firewall.status":   opFirewall,
	"docker.list":       opDockerList,
	"docker.stats":      opDockerStats,
	"docker.df":         opDockerDF,
	"docker.unused":     opDockerUnused,
	"docker.logs":       opDockerLogs,
	"tunnel.status":     opTunnel,
	"disk.usage":        opDiskUsage,
	"disk.cleanable":    opCleanable,
	"docker.images":     opImages,
	"reboot.scheduled":  opScheduled,
	"cron.list":         opCron,
	"docker.logdefault": opLogDefault,
	"backup.list":       opBackupList,
	"backup.verify":     opBackupVerify,
	"docker.preflight":  opPreflight,
	"ssh.keys":          opKeys,
	"ssh.sessionkey":    opSessionKey,
	"auth.failures":     opFailures,
	"fail2ban.status":   opFail2ban,
	"net.listen":        opListen,
	"firewall.rules":    opRules,
	"sudo.status":       opSudo,
	"apt.status":        opApt,
	"restart.needed":    opStale,
	"unit.logs":         opUnitLogs,

	// Changing
	"unit.restart":         unitOp("restart"),
	"unit.reset-failed":    unitOp("reset-failed"),
	"docker.restart":       containerOp("restart"),
	"docker.start":         containerOp("start"),
	"docker.stop":          containerOp("stop"),
	"docker.remove":        containerOp("rm"),
	"docker.project":       opProject,
	"compose.update":       opComposeUpdate,
	"compose.up":           opComposeUp,
	"compose.bindlocal":    opBindLocal,
	"docker.log-limit":     opLogLimit,
	"docker.log-truncate":  opLogTruncate,
	"journal.vacuum":       opJournalVacuum,
	"backup.create":        opBackupCreate,
	"backup.restore":       opBackupRestore,
	"backup.delete":        opBackupDelete,
	"backup.discard":       opBackupDiscard,
	"system.reboot-at":     opRebootAt,
	"system.reboot-cancel": opRebootCancel,
	"apt.clean":            opAptClean,
	"apt.autoremove":       opAutoremove,
	"docker.prune":         opPrune,
	"ssh.passwords":        opSSHPasswords,
	"ssh.rootlogin":        opSSHRoot,
	"ssh.key-add":          opKeyAdd,
	"ssh.key-remove":       opKeyRemove,
	"session.terminate":    opTerminate,
	"ufw.allow":            opUFWAllow,
	"ufw.delete":           opUFWDelete,
	"ufw.deny":             opUFWDeny,
	"ufw.enable":           opUFWEnable,
	"ufw.disable":          opUFWDisable,
	"ufw.install":          opUFWInstall,
	"sudo.passwordless":    opSudoPasswordless,
	"fail2ban.install":     opFail2banInstall,
	"apt.update":           opAptUpdate,
	"apt.upgrade":          opAptUpgrade,
	"apt.auto-on":          opAutoOn,
	"systemd.reexec":       opReexecSystemd,
	"unit.start":           unitChange("start"),
	"unit.stop":            unitChange("stop"),
	"unit.reload":          unitChange("reload"),
	"unit.enable":          unitChange("enable"),
	"unit.disable":         unitChange("disable"),
	"proc.signal":          opSignal,
	"proc.renice":          opRenice,
	"service.install":      opServiceInstall,
	"service.refresh":      opServiceRefresh,
	"service.remove":       opServiceRemove,
	"system.reboot": func(ctx context.Context, _ map[string]string) (any, error) {
		return nil, run(ctx, "systemctl", "reboot")
	},
	"time.ntp-on": func(ctx context.Context, _ map[string]string) (any, error) {
		return nil, run(ctx, "timedatectl", "set-ntp", "true")
	},
}

// ErrNoDocker is returned when Docker isn't installed.
var ErrNoDocker = errors.New("docker isn't installed")

// lookPath finds a tool, also checking the sbin directories that aren't
// always on PATH.
func lookPath(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, dir := range []string{"/usr/sbin", "/sbin", "/usr/local/sbin"} {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s isn't installed", name)
}

func output(ctx context.Context, name string, args ...string) (string, error) {
	path, err := lookPath(name)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return string(out), errors.New(msg)
		}
		return string(out), err
	}
	return string(out), nil
}

func outputBytes(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := output(ctx, name, args...)
	return []byte(out), err
}

func run(ctx context.Context, name string, args ...string) error {
	_, err := output(ctx, name, args...)
	return err
}

func opSSHDConfig(ctx context.Context, _ map[string]string) (any, error) {
	out, err := output(ctx, "sshd", "-T")
	if err != nil {
		return nil, err
	}
	return ParseSSHD(out), nil
}

func opFirewall(ctx context.Context, _ map[string]string) (any, error) {
	if _, err := lookPath("ufw"); err == nil {
		out, err := output(ctx, "ufw", "status", "verbose")
		if err != nil {
			return nil, err
		}
		return ParseUFW(out), nil
	}
	if _, err := lookPath("nft"); err == nil {
		out, err := output(ctx, "nft", "list", "ruleset")
		if err != nil {
			return nil, err
		}
		fw := Firewall{Tool: "nftables", Active: strings.Contains(out, "hook input")}
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasSuffix(line, " accept") || strings.HasSuffix(line, " drop") || strings.HasSuffix(line, " reject") {
				fw.Rules++
			}
		}
		return fw, nil
	}
	return Firewall{}, nil
}

func opDockerList(ctx context.Context, _ map[string]string) (any, error) {
	if _, err := lookPath("docker"); err != nil {
		return nil, ErrNoDocker
	}
	ids, err := output(ctx, "docker", "ps", "-aq", "--no-trunc")
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(ids)
	if len(fields) == 0 {
		return []Container{}, nil
	}
	out, err := outputBytes(ctx, "docker", append([]string{"inspect", "--type", "container"}, fields...)...)
	if err != nil {
		// A container removed between the two calls makes inspect fail;
		// one retry catches up.
		if out, err = outputBytes(ctx, "docker", append([]string{"inspect", "--type", "container"}, fields...)...); err != nil {
			return nil, err
		}
	}
	list, err := ParseInspect(out)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].LogSize = logSize(list[i].LogPath)
	}
	return list, nil
}

// logSize adds up a container's log and the older files rotated from it.
func logSize(path string) uint64 {
	if path == "" {
		return 0
	}
	files, _ := filepath.Glob(path + "*")
	var n uint64
	for _, f := range files {
		if info, err := os.Stat(f); err == nil && info.Mode().IsRegular() {
			n += uint64(info.Size())
		}
	}
	return n
}

func opDockerStats(ctx context.Context, _ map[string]string) (any, error) {
	out, err := output(ctx, "docker", "stats", "--no-stream", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	list := ParseStats(out)
	if list == nil {
		list = []Stats{}
	}
	containerNet(ctx, list)
	return list, nil
}

// containerNet fills in exact network counts from each container's own
// network, which `docker stats` only gives rounded to three digits.
func containerNet(ctx context.Context, list []Stats) {
	if len(list) == 0 {
		return
	}
	names := make([]string, len(list))
	for i, s := range list {
		names[i] = s.Name
	}
	out, err := output(ctx, "docker", append([]string{"inspect", "--type", "container", "--format", "{{.Name}} {{.State.Pid}}"}, names...)...)
	if err != nil {
		return
	}
	pids := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if name, pid, ok := strings.Cut(strings.TrimSpace(line), " "); ok && pid != "0" {
			pids[strings.TrimPrefix(name, "/")] = pid
		}
	}
	hostNS, _ := os.Readlink("/proc/1/ns/net")
	for i := range list {
		pid, ok := pids[list[i].Name]
		if !ok {
			continue
		}
		if ns, err := os.Readlink("/proc/" + pid + "/ns/net"); err == nil && ns == hostNS {
			list[i].HostNet = true
			continue
		}
		data, err := os.ReadFile("/proc/" + pid + "/net/dev")
		if err != nil {
			continue
		}
		list[i].NetRx, list[i].NetTx, list[i].HaveNet = ParseNetDev(string(data))
	}
}

func opDockerDF(ctx context.Context, _ map[string]string) (any, error) {
	out, err := output(ctx, "docker", "system", "df", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	return ParseDF(out), nil
}

// maxLogLines caps what a log request returns.
const maxLogLines = 2000

func opDockerLogs(ctx context.Context, args map[string]string) (any, error) {
	name := args["container"]
	if !ValidContainer(name) {
		return nil, fmt.Errorf("%q isn't a container name", name)
	}
	tail, err := strconv.Atoi(args["tail"])
	if err != nil || tail <= 0 || tail > maxLogLines {
		tail = 500
	}
	path, err := lookPath("docker")
	if err != nil {
		return nil, ErrNoDocker
	}
	// Containers log to both streams; show them interleaved as written.
	out, err := exec.CommandContext(ctx, path, "logs", "--timestamps", "--tail", strconv.Itoa(tail), "--", name).CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil, err
	}
	return string(out), nil
}

// opProject starts, stops or restarts every container in a Compose project.
func opProject(ctx context.Context, args map[string]string) (any, error) {
	project, action := args["project"], args["action"]
	if !ValidProject(project) {
		return nil, fmt.Errorf("%q isn't a project name", project)
	}
	switch action {
	case "start", "stop", "restart":
	default:
		return nil, fmt.Errorf("unknown project action %q", action)
	}
	ids, err := output(ctx, "docker", "ps", "-aq", "--filter", "label=com.docker.compose.project="+project)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(ids)
	if len(fields) == 0 {
		return nil, fmt.Errorf("project %s has no containers", project)
	}
	return nil, run(ctx, "docker", append([]string{action}, fields...)...)
}

// composeArgs finds how a project was started, from its containers' labels,
// so it can be brought up again the same way.
func composeArgs(ctx context.Context, project string) ([]string, error) {
	if !ValidProject(project) {
		return nil, fmt.Errorf("%q isn't a project name", project)
	}
	out, err := output(ctx, "docker", "ps", "-a", "--filter", "label=com.docker.compose.project="+project, "--format",
		`{{.Label "com.docker.compose.project.working_dir"}}|{{.Label "com.docker.compose.project.config_files"}}`)
	if err != nil {
		return nil, err
	}
	dir, files, _ := strings.Cut(strings.TrimSpace(strings.Split(out, "\n")[0]), "|")
	if dir == "" || files == "" {
		return nil, fmt.Errorf("project %s wasn't started with docker compose, so there's no compose file to use", project)
	}
	args := []string{"compose", "--project-name", project, "--project-directory", dir}
	for _, f := range strings.Split(files, ",") {
		if _, err := os.Stat(f); err != nil {
			return nil, fmt.Errorf("the compose file %s is gone", f)
		}
		args = append(args, "--file", f)
	}
	return args, nil
}

// opComposeUpdate pulls newer images (or rebuilds) and recreates the
// project's containers. It returns the tail of compose's output.
func opComposeUpdate(ctx context.Context, args map[string]string) (any, error) {
	base, err := composeArgs(ctx, args["project"])
	if err != nil {
		return nil, err
	}
	var steps [][]string
	switch args["mode"] {
	case "pull":
		steps = [][]string{{"pull", "--ignore-buildable"}, {"up", "--detach"}}
	case "build":
		steps = [][]string{{"build", "--pull"}, {"up", "--detach"}}
	case "recreate":
		// Same images, new containers: they pick up Docker's current
		// defaults, like the log limit.
		steps = [][]string{{"up", "--detach", "--force-recreate"}}
	default:
		return nil, fmt.Errorf("unknown update mode %q", args["mode"])
	}
	path, err := lookPath("docker")
	if err != nil {
		return nil, ErrNoDocker
	}
	var log strings.Builder
	for _, step := range steps {
		cmd := exec.CommandContext(ctx, path, append(append([]string{}, base...), step...)...)
		out, err := cmd.CombinedOutput()
		log.Write(out)
		if err != nil {
			return nil, fmt.Errorf("%s failed: %s", strings.Join(step, " "), lastLines(log.String(), 3))
		}
	}
	return lastLines(log.String(), 1), nil
}

// opPreflight checks whether there's room to pull or rebuild a project's
// images before trying. It compares free space where Docker keeps its data
// with the size of the images the project uses now, since new versions
// are usually about the same size.
func opPreflight(ctx context.Context, args map[string]string) (any, error) {
	project := args["project"]
	if !ValidProject(project) {
		return nil, fmt.Errorf("%q isn't a project name", project)
	}
	root, err := output(ctx, "docker", "info", "--format", "{{.DockerRootDir}}")
	if err != nil {
		return nil, err
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(strings.TrimSpace(root), &fs); err != nil {
		return nil, err
	}
	p := Preflight{Free: fs.Bavail * uint64(fs.Bsize)}

	images, err := output(ctx, "docker", "ps", "-a", "--filter", "label=com.docker.compose.project="+project, "--format", "{{.Image}}")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, img := range strings.Fields(images) {
		if seen[img] {
			continue
		}
		seen[img] = true
		if size, err := output(ctx, "docker", "image", "inspect", "--format", "{{.Size}}", img); err == nil {
			n, _ := strconv.ParseUint(strings.TrimSpace(size), 10, 64)
			p.Images += n
		}
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, "MemAvailable:"); ok {
				kb, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
				p.MemAvailable = kb * 1024
			}
		}
	}
	return p, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, " · "))
}

// opDockerUnused lists the images no container uses, running or stopped:
// what `docker system df` counts as reclaimable.
func opDockerUnused(ctx context.Context, _ map[string]string) (any, error) {
	out, err := output(ctx, "docker", "system", "df", "--verbose", "--format", "{{json .Images}}")
	if err != nil {
		return nil, err
	}
	return ParseUnused(out), nil
}

// opPrune frees Docker disk space. "safe" removes leftover image layers and
// build cache, which nothing can use again; "unused" also removes every
// image no container uses; "all" removes stopped containers and unused
// networks first, and so their images too. Volumes are never touched.
func opPrune(ctx context.Context, args map[string]string) (any, error) {
	var steps [][]string
	switch args["mode"] {
	case "safe":
		steps = [][]string{{"image", "prune", "--force"}, {"builder", "prune", "--force"}}
	case "unused":
		steps = [][]string{{"image", "prune", "--all", "--force"}, {"builder", "prune", "--force"}}
	case "all":
		steps = [][]string{{"system", "prune", "--all", "--force"}, {"builder", "prune", "--all", "--force"}}
	default:
		return nil, fmt.Errorf("unknown prune mode %q", args["mode"])
	}
	var total uint64
	for _, step := range steps {
		out, err := output(ctx, "docker", step...)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(out, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Total reclaimed space:"); ok {
				total += ParseSize(v)
			} else if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Total:"); ok {
				total += ParseSize(v)
			}
		}
	}
	return total, nil
}

func unitOp(verb string) func(context.Context, map[string]string) (any, error) {
	return func(ctx context.Context, args map[string]string) (any, error) {
		unit := args["unit"]
		if !ValidUnit(unit) {
			return nil, fmt.Errorf("%q isn't a unit name", unit)
		}
		return nil, run(ctx, "systemctl", verb, "--", unit)
	}
}

func containerOp(verb string) func(context.Context, map[string]string) (any, error) {
	return func(ctx context.Context, args map[string]string) (any, error) {
		name := args["container"]
		if !ValidContainer(name) {
			return nil, fmt.Errorf("%q isn't a container name", name)
		}
		return nil, run(ctx, "docker", verb, "--", name)
	}
}

// Known reports whether the helper has an operation.
func Known(op string) bool {
	_, ok := ops[op]
	return ok
}
