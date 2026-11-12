package priv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LogLimit is the default limit opsie sets for container logs: three files
// of 10 MB, so at most 30 MB per container.
var LogLimit = map[string]string{"max-size": "10m", "max-file": "3"}

const daemonJSON = "/etc/docker/daemon.json"

// SetLogLimit adds a default log limit to the text of daemon.json, keeping
// every other setting. It changes nothing when a limit or another log
// driver is already set.
func SetLogLimit(text string) (string, bool, error) {
	conf := map[string]any{}
	if strings.TrimSpace(text) != "" {
		if err := json.Unmarshal([]byte(text), &conf); err != nil {
			return "", false, fmt.Errorf("%s isn't valid JSON, so opsie won't touch it: %w", daemonJSON, err)
		}
	}
	if d, ok := conf["log-driver"].(string); ok && d != "json-file" {
		return text, false, nil // local and journald trim their own logs
	}
	opts, _ := conf["log-opts"].(map[string]any)
	if opts == nil {
		opts = map[string]any{}
	}
	if _, ok := opts["max-size"]; ok {
		return text, false, nil
	}
	for k, v := range LogLimit {
		if _, ok := opts[k]; !ok {
			opts[k] = v
		}
	}
	conf["log-driver"] = "json-file"
	conf["log-opts"] = opts
	out, err := json.MarshalIndent(conf, "", "  ")
	return string(out) + "\n", true, err
}

// DockerLogDefault is what daemon.json says about container logs.
type DockerLogDefault struct {
	Driver  string `json:"driver"`
	MaxSize string `json:"max_size"`
	// Limited is whether new containers get a limit.
	Limited bool `json:"limited"`
	// Pending is set when daemon.json changed after Docker started, so
	// Docker isn't using what it says yet.
	Pending bool `json:"pending,omitempty"`
}

// ParseLogDefault reads daemon.json's log settings.
func ParseLogDefault(text string) DockerLogDefault {
	var conf struct {
		Driver string            `json:"log-driver"`
		Opts   map[string]string `json:"log-opts"`
	}
	_ = json.Unmarshal([]byte(text), &conf)
	d := DockerLogDefault{Driver: conf.Driver, MaxSize: conf.Opts["max-size"]}
	if d.Driver == "" {
		d.Driver = "json-file"
	}
	d.Limited = d.Driver != "json-file" || d.MaxSize != ""
	return d
}

func opLogDefault(ctx context.Context, _ map[string]string) (any, error) {
	data, err := os.ReadFile(daemonJSON)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	d := ParseLogDefault(string(data))
	if info, err := os.Stat(daemonJSON); err == nil {
		if started, ok := dockerStarted(ctx); ok {
			d.Pending = info.ModTime().After(started)
		}
	}
	return d, nil
}

// dockerStarted is when docker.service last started.
func dockerStarted(ctx context.Context) (time.Time, bool) {
	out, err := output(ctx, "systemctl", "show", "--timestamp=unix", "-p", "ActiveEnterTimestamp", "--value", "docker.service")
	if err != nil {
		return time.Time{}, false
	}
	return parseUnixStamp(out)
}

// parseUnixStamp reads systemd's "@1793792067".
func parseUnixStamp(s string) (time.Time, bool) {
	n, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(s), "@"), 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}, false
	}
	return time.Unix(n, 0), true
}

// opLogLimit sets the default log limit in daemon.json. Docker reads it
// when it starts, so it takes effect after Docker restarts, for containers
// created from then on.
func opLogLimit(ctx context.Context, _ map[string]string) (any, error) {
	data, err := os.ReadFile(daemonJSON)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	out, changed, err := SetLogLimit(string(data))
	if err != nil || !changed {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(daemonJSON), 0o755); err != nil {
		return nil, err
	}
	tmp := daemonJSON + ".opsie-tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
		return nil, err
	}
	// Docker refuses to start with a bad file, so have it check this one.
	if _, err := lookPath("dockerd"); err == nil {
		if err := run(ctx, "dockerd", "--validate", "--config-file", tmp); err != nil {
			os.Remove(tmp)
			return nil, fmt.Errorf("docker didn't accept the change: %w", err)
		}
	}
	if len(data) > 0 {
		if err := os.WriteFile(daemonJSON+".before-opsie-"+time.Now().Format("20060102-150405"), data, 0o644); err != nil {
			os.Remove(tmp)
			return nil, err
		}
	}
	return nil, os.Rename(tmp, daemonJSON)
}

// opLogTruncate empties a container's log, and the files rotated from it.
func opLogTruncate(ctx context.Context, args map[string]string) (any, error) {
	name := args["container"]
	if !ValidContainer(name) {
		return nil, fmt.Errorf("%q isn't a container name", name)
	}
	out, err := output(ctx, "docker", "inspect", "--type", "container", "--format", "{{.LogPath}}", "--", name)
	if err != nil {
		return nil, err
	}
	path := strings.TrimSpace(out)
	root, err := output(ctx, "docker", "info", "--format", "{{.DockerRootDir}}")
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(strings.TrimSpace(root), "containers") + "/"
	if path == "" || filepath.Clean(path) != path || !strings.HasPrefix(path, dir) || !strings.HasSuffix(path, "-json.log") {
		return nil, errors.New("this container doesn't keep its log in a file opsie can empty")
	}
	freed := logSize(path)
	rotated, _ := filepath.Glob(path + ".*")
	for _, f := range rotated {
		_ = os.Remove(f)
	}
	// Docker keeps the file open, so it's emptied rather than removed.
	if err := os.Truncate(path, 0); err != nil {
		return nil, err
	}
	return freed, nil
}
