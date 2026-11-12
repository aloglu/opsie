// Package systemd reads services and timers from systemctl. Reading needs
// no root, so the Services tab works even while admin access is locked.
package systemd

import (
	"context"
	"encoding/json"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Unit is a service and its current state.
type Unit struct {
	Name, Description  string
	Load, Active, Sub  string
	FileState          string // enabled, disabled, static, …
	Result             string
	MainPID            int
	Memory             uint64
	HaveMemory         bool
	CPU                time.Duration
	Tasks              int
	Restarts           int
	Since              time.Time // when it last became active
	CanReload, CanStop bool
	RefuseStop         bool
	Fragment           string
	TriggeredBy        string
	Type               string // simple, oneshot, forking, …
}

// Failed reports whether the unit is in the failed state.
func (u Unit) Failed() bool { return u.Active == "failed" }

// Running reports whether the unit has a process running.
func (u Unit) Running() bool { return u.Active == "active" && u.Sub == "running" }

// Short is the name without ".service".
func (u Unit) Short() string { return strings.TrimSuffix(u.Name, ".service") }

// Timer is a systemd timer.
type Timer struct {
	Name, Activates string
	Next, Last      time.Time
}

var props = []string{"Id", "Description", "LoadState", "ActiveState", "SubState", "UnitFileState", "Result", "MainPID",
	"MemoryCurrent", "CPUUsageNSec", "TasksCurrent", "NRestarts", "ActiveEnterTimestamp", "CanReload", "CanStop",
	"RefuseManualStop", "FragmentPath", "TriggeredBy", "Type"}

// Services lists every loaded service.
func Services(ctx context.Context) ([]Unit, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "list-units", "--type=service", "--all", "--output=json", "--no-pager").Output()
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Unit string `json:"unit"`
		Load string `json:"load"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	var names []string
	for _, r := range rows {
		if r.Load == "loaded" && !strings.Contains(r.Unit, "@.") {
			names = append(names, r.Unit)
		}
	}
	if len(names) == 0 {
		return []Unit{}, nil
	}
	args := append([]string{"show", "--timestamp=unix", "--no-pager"}, names...)
	for _, p := range props {
		args = append(args, "-p", p)
	}
	out, err = exec.CommandContext(ctx, "systemctl", args...).Output()
	if err != nil {
		return nil, err
	}
	units := ParseShow(string(out))
	sort.Slice(units, func(i, j int) bool { return units[i].Name < units[j].Name })
	return units, nil
}

// ParseShow reads `systemctl show --timestamp=unix` output for several
// units, which comes as blocks separated by blank lines.
func ParseShow(out string) []Unit {
	var units []Unit
	for _, chunk := range strings.Split(strings.TrimSpace(out), "\n\n") {
		p := map[string]string{}
		for _, line := range strings.Split(chunk, "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				p[k] = v
			}
		}
		if p["Id"] == "" || p["LoadState"] != "loaded" {
			continue
		}
		u := Unit{
			Name: p["Id"], Description: p["Description"], Load: p["LoadState"], Active: p["ActiveState"], Sub: p["SubState"],
			FileState: p["UnitFileState"], Result: p["Result"], Fragment: p["FragmentPath"], TriggeredBy: p["TriggeredBy"], Type: p["Type"],
			CanReload: p["CanReload"] == "yes", CanStop: p["CanStop"] != "no", RefuseStop: p["RefuseManualStop"] == "yes",
		}
		u.MainPID, _ = strconv.Atoi(p["MainPID"])
		if n, err := strconv.ParseUint(p["MemoryCurrent"], 10, 64); err == nil && n < 1<<62 {
			u.Memory, u.HaveMemory = n, true
		}
		if n, err := strconv.ParseUint(p["CPUUsageNSec"], 10, 64); err == nil && n < 1<<62 {
			u.CPU = time.Duration(n)
		}
		if n, err := strconv.ParseUint(p["TasksCurrent"], 10, 64); err == nil && n < 1<<31 {
			u.Tasks = int(n)
		}
		u.Restarts, _ = strconv.Atoi(p["NRestarts"])
		u.Since = unixStamp(p["ActiveEnterTimestamp"])
		units = append(units, u)
	}
	return units
}

func unixStamp(s string) time.Time {
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "@"), 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// Timers lists every timer.
func Timers(ctx context.Context) ([]Timer, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "list-timers", "--all", "--output=json", "--no-pager").Output()
	if err != nil {
		return nil, err
	}
	return ParseTimers(out)
}

// ParseTimers reads `systemctl list-timers --output=json`, whose times
// are microseconds since the epoch.
func ParseTimers(out []byte) ([]Timer, error) {
	var rows []struct {
		Unit      string  `json:"unit"`
		Activates string  `json:"activates"`
		Next      *uint64 `json:"next"`
		Last      *uint64 `json:"last"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	micros := func(v *uint64) time.Time {
		if v == nil || *v == 0 {
			return time.Time{}
		}
		return time.UnixMicro(int64(*v))
	}
	timers := make([]Timer, 0, len(rows))
	for _, r := range rows {
		timers = append(timers, Timer{Name: r.Unit, Activates: r.Activates, Next: micros(r.Next), Last: micros(r.Last)})
	}
	sort.SliceStable(timers, func(i, j int) bool {
		a, b := timers[i].Next, timers[j].Next
		if a.IsZero() != b.IsZero() {
			return !a.IsZero()
		}
		return a.Before(b)
	})
	return timers, nil
}
