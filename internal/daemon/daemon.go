// Package daemon is opsie's background service: it runs the same checks as
// the dashboard on a schedule, keeps a history, and sends alerts.
package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/facts"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/history"
	"github.com/aloglu/opsie/internal/metrics"
	"github.com/aloglu/opsie/internal/notify"
	"github.com/aloglu/opsie/internal/priv"
	"github.com/aloglu/opsie/internal/watch"
)

// Options say where the service reads and writes.
type Options struct {
	ConfigPath string
	User       string // whose server this is, for checks about "you"
	StateDir   string
	Version    string
	Log        io.Writer
	// Dev is a development build, whose backups go in their own folder.
	Dev bool
}

// Status is what the service last did, for the dashboard to show.
type Status struct {
	Version   string    `json:"version"`
	PID       int       `json:"pid"`
	Started   time.Time `json:"started"`
	Checked   time.Time `json:"checked"`
	Every     int       `json:"every"` // seconds between checks
	Crit      int       `json:"crit"`
	Warn      int       `json:"warn"`
	Targets   bool      `json:"targets"`
	Problems  []string  `json:"problems,omitempty"` // config mistakes
	SendError string    `json:"send_error,omitempty"`
	LastSent  time.Time `json:"last_sent,omitzero"`
	// Issues are the results that need attention, worst first, for the
	// login summary.
	Issues []Issue `json:"issues,omitempty"`
}

// Issue is one result that needs attention.
type Issue struct {
	Status  health.Status `json:"status"`
	Title   string        `json:"title"`
	Summary string        `json:"summary,omitempty"`
}

// Fresh reports whether the status is recent enough to trust: the service
// wrote it within a few of its checks.
func (s Status) Fresh(now time.Time) bool {
	every := time.Duration(max(s.Every, 30)) * time.Second
	return !s.Checked.IsZero() && now.Sub(s.Checked) < 3*every+time.Minute
}

const (
	statusFile  = "status.json"
	stateFile   = "state.json"
	historyFile = "history.jsonl"
	// historyMax is how big the history grows before its older half goes.
	historyMax = 1 << 20
)

// Run checks until ctx ends.
func Run(ctx context.Context, opt Options) error {
	if os.Geteuid() != 0 {
		return errors.New("the background service runs as root; install it from the dashboard, or run it with sudo")
	}
	if err := os.MkdirAll(opt.StateDir, 0o755); err != nil {
		return err
	}
	logf := func(format string, a ...any) {
		if opt.Log != nil {
			fmt.Fprintf(opt.Log, format+"\n", a...)
		}
	}
	state := loadState(filepath.Join(opt.StateDir, stateFile))
	st := Status{Version: opt.Version, PID: os.Getpid(), Started: time.Now()}
	collector := metrics.NewCollector()
	collector.Collect(ctx) // the first reading gives rates something to compare
	rec := &history.Recorder{Dir: opt.StateDir}
	logf("opsie %s watching, state in %s", opt.Version, opt.StateDir)
	nb := &nightly{opt: opt, call: priv.Local{}.Call, logf: logf}
	go nb.loop(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
		cfg, cfgErr := config.Load(opt.ConfigPath)
		st.Problems = cfg.Problems()
		if cfgErr != nil {
			st.Problems = append([]string{cfgErr.Error()}, st.Problems...)
		}
		now := time.Now()
		results, snap, f := check(ctx, collector, cfg, opt.User, ReadBackups(opt.StateDir))
		if err := rec.Add(history.FromSnapshot(snap)); err != nil {
			logf("couldn't record the history: %v", err)
		}

		prev := state.Clone()
		msg, events := state.Step(results, now, cfg.Notify)
		st.Targets = cfg.Notify.Targets()
		if msg != nil {
			msg.Title = watch.Title(snap.Host.Hostname)
			logf("%s", strings.ReplaceAll(msg.Text(), "\n", " · "))
			if st.Targets {
				sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				err := notify.Send(sendCtx, cfg.Notify, *msg)
				cancel()
				if err != nil {
					// Forget this round so it's tried again next time.
					state, events = prev, nil
					st.SendError = err.Error()
					logf("couldn't send: %v", err)
				} else {
					st.SendError, st.LastSent = "", now
				}
			}
		}
		if f.FailuresErr == "" {
			for _, m := range loginAlerts(opt.StateDir, f, cfg.Notify, snap.Host.Hostname, now) {
				logf("%s", strings.ReplaceAll(m.Text(), "\n", " · "))
				if st.Targets {
					sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					if err := notify.Send(sendCtx, cfg.Notify, m); err != nil {
						st.SendError = err.Error()
						logf("couldn't send: %v", err)
					}
					cancel()
				}
			}
		}
		appendHistory(filepath.Join(opt.StateDir, historyFile), events)
		if st.Targets && cfg.Notify.Weekly != "" {
			weeklyStep(opt.StateDir, cfg.Notify.Weekly, now, func() notify.Message {
				samples, _ := history.Read(opt.StateDir, now.Add(-7*24*time.Hour))
				return Weekly(snap.Host.Hostname, now, snap, results, ReadHistory(opt.StateDir, 500), samples, cfg.Backup.Nightly, ReadBackups(opt.StateDir))
			}, func(m notify.Message) error {
				sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				return notify.Send(sendCtx, cfg.Notify, m)
			})
		}
		saveJSON(filepath.Join(opt.StateDir, stateFile), state, 0o600)
		st.Checked, st.Every = now, cfg.CheckSeconds
		st.Crit, st.Warn = health.Issues(results)
		st.Issues = nil
		for _, r := range results {
			if r.Status >= health.Warn {
				st.Issues = append(st.Issues, Issue{Status: r.Status, Title: r.Title, Summary: r.Summary})
			}
		}
		saveJSON(filepath.Join(opt.StateDir, statusFile), st, 0o644)

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Duration(cfg.CheckSeconds)*time.Second - time.Second):
		}
	}
}

func check(ctx context.Context, c *metrics.Collector, cfg config.Config, user string, runs []health.BackupRun) ([]health.Result, metrics.Snapshot, facts.Facts) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	snap := c.Collect(ctx)
	f := facts.GatherFor(ctx, priv.Local{}, nil, user)
	f.CheckOnline(ctx, cfg)
	return health.Evaluate(health.Readings{Snap: snap, HaveSnap: true, Facts: f, HaveFacts: true, Thresholds: cfg.Thresholds, Sites: cfg.Sites, PublicPorts: cfg.Docker.PublicPorts,
		Nightly: health.Nightly{Apps: cfg.Backup.Nightly, At: cfg.Backup.At, Runs: runs, Running: true}, HaveNightly: true}), snap, f
}

func loadState(path string) *watch.State {
	s := &watch.State{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, s)
	}
	return s
}

func saveJSON(path string, v any, mode os.FileMode) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, mode) == nil {
		_ = os.Chmod(tmp, mode)
		_ = os.Rename(tmp, path)
	}
}

func appendHistory(path string, events []watch.Event) {
	if len(events) == 0 {
		return
	}
	if info, err := os.Stat(path); err == nil && info.Size() > historyMax {
		trimHistory(path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range events {
		_ = enc.Encode(e)
	}
}

// trimHistory keeps the newer half of the history.
func trimHistory(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	half := data[len(data)/2:]
	if i := strings.IndexByte(string(half), '\n'); i >= 0 {
		half = half[i+1:]
	}
	_ = os.WriteFile(path, half, 0o644)
}

// ReadStatus reads what the service in dir last did.
func ReadStatus(dir string) (Status, error) {
	var s Status
	data, err := os.ReadFile(filepath.Join(dir, statusFile))
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

// ReadHistory returns up to n of the newest events, newest first.
func ReadHistory(dir string, n int) []watch.Event {
	f, err := os.Open(filepath.Join(dir, historyFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	var all []watch.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e watch.Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			all = append(all, e)
		}
	}
	out := make([]watch.Event, 0, n)
	for i := len(all) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, all[i])
	}
	return out
}

func readJSON(path string, v any) {
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, v)
	}
}
