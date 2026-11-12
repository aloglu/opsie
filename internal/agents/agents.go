// Package agents reads how much the AI coding agents on this server have
// been used, from the session logs they keep in your home folder: what
// their own /usage and /status screens show.
package agents

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Days is how far back usage is counted, today included.
const Days = 7

// Tokens is how much text went through a model. Input is what was sent
// fresh, CacheRead what was sent again from the provider's cache (cheap),
// CacheWrite what was stored there, Output what the model wrote.
type Tokens struct {
	Input, Output, CacheRead, CacheWrite uint64
}

func (t Tokens) Total() uint64 { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

func (t *Tokens) add(o Tokens) {
	t.Input += o.Input
	t.Output += o.Output
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
}

// Window is one of a plan's limits: how much of it is used, and when it
// starts over.
type Window struct {
	Name string
	// Used is a percentage.
	Used   float64
	Resets time.Time
}

// Agent is one agent's usage.
type Agent struct {
	// ID is "claude" or "codex"; Name is what it's called.
	ID, Name string
	Version  string
	Plan     string
	// Model is the one used last.
	Model      string
	LastActive time.Time
	Today      Tokens
	Week       Tokens
	// Daily is the tokens of each of the last Days days, today last.
	Daily [Days]uint64
	// Sessions are how many conversations had activity, today and in the
	// last Days days.
	SessionsToday, SessionsWeek int
	// Models is the week's tokens by model.
	Models map[string]uint64
	// Limits are the plan's limits as last known, and LimitsAt when that
	// was; LimitsNote says why there are none.
	Limits     []Window
	LimitsAt   time.Time
	LimitsNote string
}

// Read finds the agents installed for the user whose home this is and
// adds up their logs.
func Read(home string, now time.Time) []Agent {
	var out []Agent
	if dir := filepath.Join(home, ".claude"); isDir(dir) {
		out = append(out, readClaude(dir, now))
	}
	if dir := filepath.Join(home, ".codex"); isDir(dir) {
		out = append(out, readCodex(dir, now))
	}
	return out
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// dayIndex is which of the last Days days at is, or -1 if it's older.
func dayIndex(at, now time.Time) int {
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	at = at.In(now.Location())
	if !at.Before(today) {
		return Days - 1
	}
	back := int(today.Sub(at)/(24*time.Hour)) + 1
	if back >= Days {
		return -1
	}
	return Days - 1 - back
}

// recentLogs lists the .jsonl files under dir changed in the last Days
// days: only those can hold anything recent.
func recentLogs(dir string, now time.Time) []string {
	var files []string
	cutoff := now.Add(-(Days + 1) * 24 * time.Hour)
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(cutoff) {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// eachLine calls fn with every line of a file, however long.
func eachLine(path string, fn func(line []byte)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			fn(line)
		}
		if err != nil {
			return
		}
	}
}

// --- Claude Code ---

func readClaude(dir string, now time.Time) Agent {
	a := Agent{ID: "claude", Name: "Claude Code", Models: map[string]uint64{},
		LimitsNote: "Claude Code doesn't write its plan limits to disk."}
	seen := map[string]bool{}
	sessToday, sessWeek := map[string]bool{}, map[string]bool{}
	for _, path := range recentLogs(filepath.Join(dir, "projects"), now) {
		eachLine(path, func(line []byte) {
			// Only the model's answers carry usage; skip the rest unparsed.
			if !bytes.Contains(line, []byte(`"usage"`)) || !bytes.Contains(line, []byte(`"assistant"`)) {
				return
			}
			var row struct {
				Type      string    `json:"type"`
				Timestamp time.Time `json:"timestamp"`
				RequestID string    `json:"requestId"`
				SessionID string    `json:"sessionId"`
				Version   string    `json:"version"`
				Message   struct {
					ID    string `json:"id"`
					Model string `json:"model"`
					Usage *struct {
						Input      uint64 `json:"input_tokens"`
						Output     uint64 `json:"output_tokens"`
						CacheRead  uint64 `json:"cache_read_input_tokens"`
						CacheWrite uint64 `json:"cache_creation_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &row) != nil || row.Type != "assistant" || row.Message.Usage == nil {
				return
			}
			// An answer is written once per part, with the same usage each
			// time.
			if key := row.Message.ID + "/" + row.RequestID; key != "/" {
				if seen[key] {
					return
				}
				seen[key] = true
			}
			if strings.HasPrefix(row.Message.Model, "<") {
				return // a made-up answer, not a model's
			}
			day := dayIndex(row.Timestamp, now)
			if day < 0 {
				return
			}
			u := row.Message.Usage
			tok := Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
			a.Week.add(tok)
			a.Daily[day] += tok.Total()
			a.Models[row.Message.Model] += tok.Total()
			sessWeek[row.SessionID] = true
			if day == Days-1 {
				a.Today.add(tok)
				sessToday[row.SessionID] = true
			}
			if row.Timestamp.After(a.LastActive) {
				a.LastActive, a.Model = row.Timestamp, row.Message.Model
				if row.Version != "" {
					a.Version = row.Version
				}
			}
		})
	}
	a.SessionsToday, a.SessionsWeek = len(sessToday), len(sessWeek)
	return a
}

// ClaudeLimits asks Anthropic for the plan limits Claude Code's /usage
// shows, with the login Claude Code keeps in dir (~/.claude). The login
// token is only read and only sent to api.anthropic.com; when it has run
// out, Claude Code renews it the next time it's used, and until then
// there's nothing to ask with.
func ClaudeLimits(ctx context.Context, dir string) (limits []Window, plan string, err error) {
	data, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		return nil, "", errors.New("Claude Code isn't logged in with a Claude plan here")
	}
	var cred struct {
		OAuth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
			Plan        string `json:"subscriptionType"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(data, &cred) != nil || cred.OAuth.AccessToken == "" {
		return nil, "", errors.New("Claude Code isn't logged in with a Claude plan here")
	}
	plan = cred.OAuth.Plan
	if cred.OAuth.ExpiresAt > 0 && time.Now().After(time.UnixMilli(cred.OAuth.ExpiresAt)) {
		return nil, plan, errors.New("Claude Code's login has run out; it renews it the next time you use it")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", claudeUsageURL, nil)
	req.Header.Set("Authorization", "Bearer "+cred.OAuth.AccessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "opsie")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, plan, errors.New("couldn't reach Anthropic")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != 200 {
		return nil, plan, fmt.Errorf("Anthropic answered %s", resp.Status)
	}
	limits, err = parseClaudeLimits(body)
	return limits, plan, err
}

var claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

func parseClaudeLimits(body []byte) ([]Window, error) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return nil, errors.New("Anthropic's answer wasn't what was expected")
	}
	var out []Window
	for _, w := range []struct{ key, name string }{
		{"five_hour", "5 hours"}, {"seven_day", "Week"},
		{"seven_day_opus", "Week, Opus"}, {"seven_day_sonnet", "Week, Sonnet"},
	} {
		var v *struct {
			Utilization *float64   `json:"utilization"`
			ResetsAt    *time.Time `json:"resets_at"`
		}
		if json.Unmarshal(raw[w.key], &v) != nil || v == nil || v.Utilization == nil {
			continue
		}
		win := Window{Name: w.name, Used: *v.Utilization}
		if v.ResetsAt != nil {
			win.Resets = *v.ResetsAt
		}
		out = append(out, win)
	}
	if len(out) == 0 {
		return nil, errors.New("Anthropic's answer had no limits in it")
	}
	return out, nil
}

// --- Codex ---

func readCodex(dir string, now time.Time) Agent {
	a := Agent{ID: "codex", Name: "Codex", Models: map[string]uint64{}}
	sessToday, sessWeek := 0, 0
	for _, path := range recentLogs(filepath.Join(dir, "sessions"), now) {
		// A session counts its tokens up from zero, so each reading is
		// the difference from the one before.
		var prev Tokens
		model := ""
		today, week := false, false
		eachLine(path, func(line []byte) {
			switch {
			case bytes.Contains(line, []byte(`"token_count"`)):
			case bytes.Contains(line, []byte(`"turn_context"`)), bytes.Contains(line, []byte(`"session_meta"`)):
			default:
				return
			}
			var row struct {
				Timestamp time.Time `json:"timestamp"`
				Type      string    `json:"type"`
				Payload   struct {
					Type    string `json:"type"`
					Model   string `json:"model"`
					Version string `json:"cli_version"`
					Info    *struct {
						Total struct {
							Input     uint64 `json:"input_tokens"`
							Cached    uint64 `json:"cached_input_tokens"`
							CacheMade uint64 `json:"cache_write_input_tokens"`
							Output    uint64 `json:"output_tokens"`
						} `json:"total_token_usage"`
					} `json:"info"`
					Limits *struct {
						Primary   *codexWindow `json:"primary"`
						Secondary *codexWindow `json:"secondary"`
						Plan      string       `json:"plan_type"`
					} `json:"rate_limits"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &row) != nil {
				return
			}
			switch {
			case row.Type == "session_meta":
				if row.Payload.Version != "" && !row.Timestamp.Before(a.LastActive) {
					a.Version = row.Payload.Version
				}
				return
			case row.Type == "turn_context":
				if row.Payload.Model != "" {
					model = row.Payload.Model
				}
				return
			case row.Type != "event_msg" || row.Payload.Type != "token_count":
				return
			}
			if l := row.Payload.Limits; l != nil && l.Primary != nil && row.Timestamp.After(a.LimitsAt) {
				a.LimitsAt, a.Limits = row.Timestamp, nil
				for _, w := range []*codexWindow{l.Primary, l.Secondary} {
					if w != nil {
						a.Limits = append(a.Limits, w.window(now))
					}
				}
				if l.Plan != "" {
					a.Plan = l.Plan
				}
			}
			if row.Payload.Info == nil {
				return
			}
			t := row.Payload.Info.Total
			// Codex counts cached input as input too.
			cur := Tokens{Input: t.Input - min(t.Cached, t.Input), Output: t.Output, CacheRead: t.Cached, CacheWrite: t.CacheMade}
			delta := Tokens{Input: over(cur.Input, prev.Input), Output: over(cur.Output, prev.Output),
				CacheRead: over(cur.CacheRead, prev.CacheRead), CacheWrite: over(cur.CacheWrite, prev.CacheWrite)}
			prev = cur
			day := dayIndex(row.Timestamp, now)
			if day < 0 || delta.Total() == 0 {
				return
			}
			a.Week.add(delta)
			a.Daily[day] += delta.Total()
			if model != "" {
				a.Models[model] += delta.Total()
			}
			week = true
			if day == Days-1 {
				a.Today.add(delta)
				today = true
			}
			if row.Timestamp.After(a.LastActive) {
				a.LastActive = row.Timestamp
				if model != "" {
					a.Model = model
				}
			}
		})
		if today {
			sessToday++
		}
		if week {
			sessWeek++
		}
	}
	a.SessionsToday, a.SessionsWeek = sessToday, sessWeek
	if len(a.Limits) == 0 {
		a.LimitsNote = "Codex writes its limits down as it's used; it hasn't been used in the last week."
	}
	return a
}

// over is how far a count has gone past the one before; a count that
// went back started over.
func over(cur, prev uint64) uint64 {
	if cur < prev {
		return cur
	}
	return cur - prev
}

type codexWindow struct {
	Used    float64 `json:"used_percent"`
	Minutes int     `json:"window_minutes"`
	Resets  int64   `json:"resets_at"`
}

func (w codexWindow) window(now time.Time) Window {
	out := Window{Used: w.Used}
	switch {
	case w.Minutes == 300:
		out.Name = "5 hours"
	case w.Minutes == 10080:
		out.Name = "Week"
	case w.Minutes >= 1440:
		out.Name = fmt.Sprintf("%d days", w.Minutes/1440)
	default:
		out.Name = fmt.Sprintf("%d hours", max(w.Minutes/60, 1))
	}
	if w.Resets > 0 {
		out.Resets = time.Unix(w.Resets, 0)
		// A window that has started over since it was written is empty.
		if out.Resets.Before(now) {
			out.Used, out.Resets = 0, time.Time{}
		}
	}
	return out
}
