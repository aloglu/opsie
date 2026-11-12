// Package audit keeps a record of every change opsie makes to the server.
package audit

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Entry is one line of the audit log.
type Entry struct {
	Time   time.Time         `json:"time"`
	Action string            `json:"action"`
	Args   map[string]string `json:"args,omitempty"`
	// Result is "requested" before an action runs, then "ok" or "failed".
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

// Log appends e to the log at path as a line of JSON.
func Log(path string, e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil && info.Size() > maxSize {
		trim(path)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

// maxSize is how big the log grows before its older half goes: years of
// normal use.
const maxSize = 2 << 20

// trim keeps the newer half of the log.
func trim(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	half := data[len(data)/2:]
	if i := strings.IndexByte(string(half), '\n'); i >= 0 {
		half = half[i+1:]
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, half, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

// Done is an action with how it ended.
type Done struct {
	Entry
	// Unfinished is an action that started but has no result: opsie was
	// closed or the connection dropped while it ran.
	Unfinished bool
}

// Read returns the last n actions, newest first.
func Read(path string, n int) []Done {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var all []Done
	var open []int // indexes of requests without a result yet
	for _, line := range strings.Split(string(data), "\n") {
		var e Entry
		if json.Unmarshal([]byte(line), &e) != nil || e.Action == "" {
			continue
		}
		if e.Result == "requested" {
			open = append(open, len(all))
			all = append(all, Done{Entry: e, Unfinished: true})
			continue
		}
		matched := false
		for i, at := range open {
			if all[at].Action == e.Action && maps.Equal(all[at].Args, e.Args) {
				all[at] = Done{Entry: e}
				open = append(open[:i], open[i+1:]...)
				matched = true
				break
			}
		}
		if !matched {
			all = append(all, Done{Entry: e})
		}
	}
	slices.Reverse(all)
	if len(all) > n {
		all = all[:n]
	}
	return all
}
