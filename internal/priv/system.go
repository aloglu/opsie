package priv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Vital units keep the server reachable or running at all. opsie won't
// stop or disable them; restarting is still fine.
var vital = map[string]bool{
	"ssh.service": true, "sshd.service": true, "dbus.service": true, "dbus-broker.service": true,
	"systemd-journald.service": true, "systemd-logind.service": true, "systemd-udevd.service": true,
	"systemd-networkd.service": true, "systemd-resolved.service": true, "networking.service": true,
	"NetworkManager.service": true, "polkit.service": true,
}

// Vital reports whether stopping or disabling a unit could cut the server
// off or break it.
func Vital(unit string) bool { return vital[unit] || strings.HasPrefix(unit, "user@") }

func unitChange(verb string) func(context.Context, map[string]string) (any, error) {
	return func(ctx context.Context, args map[string]string) (any, error) {
		unit := args["unit"]
		if !ValidUnit(unit) {
			return nil, fmt.Errorf("%q isn't a unit name", unit)
		}
		if (verb == "stop" || verb == "disable") && Vital(unit) {
			return nil, fmt.Errorf("opsie won't %s %s: the server needs it to stay reachable. Restart it instead", verb, unit)
		}
		return nil, run(ctx, "systemctl", verb, "--", unit)
	}
}

// opUnitLogs reads a unit's journal as "RFC3339 message" lines, the same
// shape as docker logs --timestamps, so one viewer shows both.
func opUnitLogs(ctx context.Context, args map[string]string) (any, error) {
	unit := args["unit"]
	if !ValidUnit(unit) {
		return nil, fmt.Errorf("%q isn't a unit name", unit)
	}
	tail, err := strconv.Atoi(args["tail"])
	if err != nil || tail <= 0 || tail > 2000 {
		tail = 1000
	}
	out, err := outputBytes(ctx, "journalctl", "--unit", unit, "--lines", strconv.Itoa(tail), "--output", "json", "--no-pager", "--quiet",
		"--output-fields", "MESSAGE,__REALTIME_TIMESTAMP")
	if err != nil {
		return nil, err
	}
	return JournalText(out), nil
}

// JournalText turns journalctl's JSON lines into timestamped text.
func JournalText(out []byte) string {
	var b strings.Builder
	for _, line := range bytes.Split(out, []byte("\n")) {
		var e struct {
			Message json.RawMessage `json:"MESSAGE"`
			Time    string          `json:"__REALTIME_TIMESTAMP"`
		}
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		var msg string
		if json.Unmarshal(e.Message, &msg) != nil {
			// Messages that aren't valid UTF-8 come as byte arrays.
			var raw []byte
			var nums []int
			if json.Unmarshal(e.Message, &nums) == nil {
				for _, n := range nums {
					raw = append(raw, byte(n))
				}
			}
			msg = strings.ToValidUTF8(string(raw), "�")
		}
		micros, _ := strconv.ParseInt(e.Time, 10, 64)
		for _, part := range strings.Split(strings.TrimRight(msg, "\n"), "\n") {
			b.WriteString(time.UnixMicro(micros).UTC().Format(time.RFC3339Nano))
			b.WriteByte(' ')
			b.WriteString(part)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// target finds the process a request names, making sure the PID still
// belongs to the same process the dashboard showed.
func target(args map[string]string) (int, error) {
	pid, err := strconv.Atoi(args["pid"])
	if err != nil || pid <= 1 {
		return 0, fmt.Errorf("%q isn't a process opsie will touch", args["pid"])
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, errors.New("that process has already exited")
	}
	start, ppid, ok := statStart(string(data))
	if !ok {
		return 0, errors.New("couldn't read that process")
	}
	if strconv.FormatUint(start, 10) != args["start"] {
		return 0, errors.New("that process has already exited (its number now belongs to another one)")
	}
	if pid == 2 || ppid == 2 {
		return 0, errors.New("that's a kernel thread")
	}
	for p := os.Getpid(); p > 1; {
		if p == pid {
			return 0, errors.New("that's part of your own session; quit opsie instead")
		}
		data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(p), "stat"))
		if err != nil {
			break
		}
		_, parent, ok := statStart(string(data))
		if !ok {
			break
		}
		p = parent
	}
	if unit := mainPIDOf(pid); unit != "" && Vital(unit) {
		return 0, fmt.Errorf("that's the main process of %s, which keeps the server reachable; restart the service instead", unit)
	}
	return pid, nil
}

func statStart(stat string) (start uint64, ppid int, ok bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, 0, false
	}
	f := strings.Fields(stat[i+1:])
	if len(f) < 20 {
		return 0, 0, false
	}
	start, err := strconv.ParseUint(f[19], 10, 64)
	ppid, err2 := strconv.Atoi(f[1])
	return start, ppid, err == nil && err2 == nil
}

// mainPIDOf names the vital service whose main process pid is, if any.
func mainPIDOf(pid int) string {
	cg, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return ""
	}
	m := serviceRe.FindStringSubmatch(strings.TrimSpace(strings.Split(string(cg), "\n")[0]))
	if m == nil || !Vital(m[1]) {
		return ""
	}
	out, err := output(context.Background(), "systemctl", "show", "--property", "MainPID", "--value", "--", m[1])
	if err == nil && strings.TrimSpace(out) == strconv.Itoa(pid) {
		return m[1]
	}
	return ""
}

func opSignal(_ context.Context, args map[string]string) (any, error) {
	sig := map[string]syscall.Signal{"TERM": syscall.SIGTERM, "KILL": syscall.SIGKILL, "HUP": syscall.SIGHUP}[args["signal"]]
	if sig == 0 {
		return nil, fmt.Errorf("%q isn't a signal opsie sends", args["signal"])
	}
	pid, err := target(args)
	if err != nil {
		return nil, err
	}
	if err := syscall.Kill(pid, sig); err != nil {
		return nil, err
	}
	if sig == syscall.SIGHUP {
		return "sent", nil
	}
	// Say whether it actually went away.
	for range 20 {
		time.Sleep(100 * time.Millisecond)
		if syscall.Kill(pid, 0) != nil {
			return "exited", nil
		}
		if data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat")); err == nil {
			if i := strings.LastIndexByte(string(data), ')'); i >= 0 && strings.HasPrefix(strings.TrimSpace(string(data)[i+1:]), "Z") {
				return "exited", nil // a zombie waiting for its parent
			}
		}
	}
	return "running", nil
}

func opRenice(_ context.Context, args map[string]string) (any, error) {
	nice, err := strconv.Atoi(args["nice"])
	if err != nil || nice < -20 || nice > 19 {
		return nil, errors.New("priority goes from -20 to 19")
	}
	pid, err := target(args)
	if err != nil {
		return nil, err
	}
	// Linux keeps a priority per thread, so set every thread's.
	tasks, _ := filepath.Glob(filepath.Join("/proc", strconv.Itoa(pid), "task", "[0-9]*"))
	if len(tasks) == 0 {
		tasks = []string{strconv.Itoa(pid)}
	}
	for _, task := range tasks {
		tid, _ := strconv.Atoi(filepath.Base(task))
		if err := syscall.Setpriority(syscall.PRIO_PROCESS, tid, nice); err != nil && tid == pid {
			return nil, err
		}
	}
	return nil, nil
}

// Scheduled is a restart or shutdown systemd has been asked to do later.
type Scheduled struct {
	At   time.Time `json:"at,omitzero"`
	Mode string    `json:"mode,omitempty"` // "reboot" or "poweroff"
}

// ParseScheduled reads /run/systemd/shutdown/scheduled.
func ParseScheduled(text string) Scheduled {
	var s Scheduled
	for _, line := range strings.Split(text, "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "USEC":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				s.At = time.UnixMicro(n)
			}
		case "MODE":
			s.Mode = v
		}
	}
	return s
}

func opScheduled(context.Context, map[string]string) (any, error) {
	data, err := os.ReadFile("/run/systemd/shutdown/scheduled")
	if errors.Is(err, os.ErrNotExist) {
		return Scheduled{}, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseScheduled(string(data)), nil
}

var clockRe = regexp.MustCompile(`^([01]?\d|2[0-3]):[0-5]\d$`)

// ValidClock reports whether s is a time of day like "04:00".
func ValidClock(s string) bool { return clockRe.MatchString(s) }

// opRebootAt has systemd restart the server at the next given time of day.
// Logins are refused for the last five minutes before it, as with any
// scheduled shutdown.
func opRebootAt(ctx context.Context, args map[string]string) (any, error) {
	at := args["at"]
	if !ValidClock(at) {
		return nil, fmt.Errorf("%q isn't a time like 04:00", at)
	}
	return nil, run(ctx, "shutdown", "--reboot", at, "opsie: scheduled restart")
}

func opRebootCancel(ctx context.Context, _ map[string]string) (any, error) {
	return nil, run(ctx, "shutdown", "-c", "opsie: scheduled restart cancelled")
}
