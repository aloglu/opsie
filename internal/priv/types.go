// Package priv is opsie's privileged helper. The dashboard runs as you and
// starts one copy of itself under sudo (`sudo -n opsie helper`), which
// answers a fixed list of requests over its stdin and stdout. Nothing else
// in opsie runs as root, and the helper can only do what's listed in ops.go.
package priv

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Container is one Docker container, from `docker inspect`.
type Container struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Image    string    `json:"image"`
	State    string    `json:"state"` // running, exited, restarting, paused, created, dead
	Health   string    `json:"health,omitempty"`
	Exit     int       `json:"exit"`
	OOM      bool      `json:"oom,omitempty"`
	Error    string    `json:"error,omitempty"`
	Created  time.Time `json:"created"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Restarts int       `json:"restarts"`
	Policy   string    `json:"policy,omitempty"`
	Ports    []string  `json:"ports,omitempty"`
	// Compose labels.
	Project     string `json:"project,omitempty"`
	Service     string `json:"service,omitempty"`
	WorkingDir  string `json:"working_dir,omitempty"`
	ConfigFiles string `json:"config_files,omitempty"`
	// Mounts are the volumes and folders the container uses.
	Mounts []Mount `json:"mounts,omitempty"`
	// LogDriver is where its output goes; json-file keeps it in LogPath,
	// trimmed at LogMaxSize when that's set. LogSize counts rotated files.
	LogDriver  string `json:"log_driver,omitempty"`
	LogMaxSize string `json:"log_max_size,omitempty"`
	LogPath    string `json:"log_path,omitempty"`
	LogSize    uint64 `json:"log_size,omitempty"`
}

// LogUnlimited reports whether the container's log grows without limit.
func (c Container) LogUnlimited() bool {
	return (c.LogDriver == "json-file" || c.LogDriver == "") && c.LogMaxSize == "" && c.LogPath != ""
}

// Mount is a volume or folder in a container.
type Mount struct {
	Type        string `json:"type"` // volume, bind, tmpfs
	Name        string `json:"name,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination"`
	Driver      string `json:"driver,omitempty"`
}

// Stats is a container's resource use, from `docker stats`.
type Stats struct {
	Name     string  `json:"name"`
	CPU      float64 `json:"cpu"`
	MemUsed  uint64  `json:"mem_used"`
	MemLimit uint64  `json:"mem_limit"`
	MemPct   float64 `json:"mem_pct"`
	NetIO    string  `json:"net_io"`
	// NetRx and NetTx are exact byte counts since the container started,
	// when its network could be read. HostNet is set for containers that
	// share the server's network, which have no counts of their own.
	NetRx   uint64 `json:"net_rx,omitempty"`
	NetTx   uint64 `json:"net_tx,omitempty"`
	HaveNet bool   `json:"have_net,omitempty"`
	HostNet bool   `json:"host_net,omitempty"`
	BlockIO string `json:"block_io"`
	PIDs    int    `json:"pids"`
}

// Preflight is what's needed to pull or rebuild a project's images.
type Preflight struct {
	// Free is the space left where Docker keeps images; Images is the size
	// of the project's current images.
	Free, Images uint64
	MemAvailable uint64
}

// DiskUsage is one row of `docker system df`.
type DiskUsage struct {
	Type        string `json:"type"`
	Total       int    `json:"total"`
	Active      int    `json:"active"`
	Size        string `json:"size"`
	Reclaimable string `json:"reclaimable"`
}

// Firewall describes the active firewall, if any.
type Firewall struct {
	Tool    string `json:"tool"` // "ufw", "nftables", or "" for none
	Active  bool   `json:"active"`
	Rules   int    `json:"rules"`
	Default string `json:"default,omitempty"` // ufw's default policy line
}

type request struct {
	ID   uint64            `json:"id"`
	Op   string            `json:"op"`
	Args map[string]string `json:"args,omitempty"`
}

type response struct {
	ID   uint64          `json:"id"`
	Data json.RawMessage `json:"data,omitempty"`
	Err  string          `json:"err,omitempty"`
}

// ParseInspect reads `docker inspect` output for several containers.
func ParseInspect(out []byte) ([]Container, error) {
	var rows []struct {
		ID      string `json:"Id"`
		Name    string
		Created time.Time
		State   struct {
			Status     string
			ExitCode   int
			OOMKilled  bool
			Error      string
			StartedAt  time.Time
			FinishedAt time.Time
			Health     *struct{ Status string }
		}
		RestartCount int
		Config       struct {
			Image  string
			Labels map[string]string
		}
		HostConfig struct {
			RestartPolicy struct{ Name string }
			LogConfig     struct {
				Type   string
				Config map[string]string
			}
		}
		LogPath         string
		NetworkSettings struct {
			Ports map[string][]struct{ HostIp, HostPort string }
		}
		Mounts []struct{ Type, Name, Source, Destination, Driver string }
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("couldn't read docker inspect: %w", err)
	}
	list := make([]Container, 0, len(rows))
	for _, r := range rows {
		c := Container{
			ID: r.ID, Name: strings.TrimPrefix(r.Name, "/"), Image: r.Config.Image,
			State: r.State.Status, Exit: r.State.ExitCode, OOM: r.State.OOMKilled, Error: r.State.Error,
			Created: r.Created, Started: r.State.StartedAt, Finished: r.State.FinishedAt,
			Restarts: r.RestartCount, Policy: r.HostConfig.RestartPolicy.Name,
			LogDriver: r.HostConfig.LogConfig.Type, LogMaxSize: r.HostConfig.LogConfig.Config["max-size"], LogPath: r.LogPath,
			Project:     r.Config.Labels["com.docker.compose.project"],
			Service:     r.Config.Labels["com.docker.compose.service"],
			WorkingDir:  r.Config.Labels["com.docker.compose.project.working_dir"],
			ConfigFiles: r.Config.Labels["com.docker.compose.project.config_files"],
		}
		for _, mt := range r.Mounts {
			c.Mounts = append(c.Mounts, Mount{Type: mt.Type, Name: mt.Name, Source: mt.Source, Destination: mt.Destination, Driver: mt.Driver})
		}
		if r.State.Health != nil {
			c.Health = r.State.Health.Status
		}
		for port, binds := range r.NetworkSettings.Ports {
			for _, b := range binds {
				host := b.HostIp
				if host == "" || host == "0.0.0.0" || host == "::" {
					host = "*"
				}
				c.Ports = append(c.Ports, host+":"+b.HostPort+" → "+port)
			}
		}
		sort.Strings(c.Ports)
		c.Ports = slices.Compact(c.Ports)
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

// ParseStats reads `docker stats --no-stream --format '{{json .}}'` output.
func ParseStats(out string) []Stats {
	var list []Stats
	for _, line := range strings.Split(out, "\n") {
		var row struct{ Name, CPUPerc, MemUsage, MemPerc, NetIO, BlockIO, PIDs string }
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		st := Stats{Name: row.Name, NetIO: row.NetIO, BlockIO: row.BlockIO}
		st.CPU, _ = strconv.ParseFloat(strings.TrimSuffix(row.CPUPerc, "%"), 64)
		st.MemPct, _ = strconv.ParseFloat(strings.TrimSuffix(row.MemPerc, "%"), 64)
		st.PIDs, _ = strconv.Atoi(row.PIDs)
		if used, limit, ok := strings.Cut(row.MemUsage, " / "); ok {
			st.MemUsed, st.MemLimit = ParseSize(used), ParseSize(limit)
		}
		list = append(list, st)
	}
	return list
}

// ParseNetDev adds up /proc/net/dev for every interface but loopback.
func ParseNetDev(text string) (rx, tx uint64, ok bool) {
	for _, line := range strings.Split(text, "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(name) == "lo" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		r, err1 := strconv.ParseUint(f[0], 10, 64)
		t, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rx, tx, ok = rx+r, tx+t, true
	}
	return rx, tx, ok
}

// ParseSize reads Docker's sizes, like 166MiB, 3.7GiB or 154kB.
func ParseSize(s string) uint64 {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i < 0 {
		i = len(s)
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0
	}
	mult := map[string]float64{
		"": 1, "B": 1,
		"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40,
		"kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
	}[strings.TrimSpace(s[i:])]
	return uint64(n * mult)
}

// UnusedImage is an image no container uses.
type UnusedImage struct {
	// Ref is repository:tag, or "" for a leftover layer without a name.
	Ref string `json:"ref"`
	ID  string `json:"id"`
	// Size is what removing it frees: the layers no other image shares.
	Size uint64 `json:"size"`
}

// ParseUnused reads the images of `docker system df -v --format '{{json
// .Images}}'` that no container uses.
func ParseUnused(out string) []UnusedImage {
	var rows []struct{ Containers, ID, Repository, Tag, UniqueSize string }
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &rows) != nil {
		return nil
	}
	var list []UnusedImage
	for _, r := range rows {
		if n, err := strconv.Atoi(r.Containers); err != nil || n > 0 {
			continue
		}
		img := UnusedImage{ID: r.ID, Size: ParseSize(r.UniqueSize)}
		if r.Repository != "" && r.Repository != "<none>" {
			img.Ref = r.Repository
			if r.Tag != "" && r.Tag != "<none>" {
				img.Ref += ":" + r.Tag
			}
		}
		list = append(list, img)
	}
	return list
}

// ParseDF reads `docker system df --format '{{json .}}'` output.
func ParseDF(out string) []DiskUsage {
	var list []DiskUsage
	for _, line := range strings.Split(out, "\n") {
		var row struct{ Type, TotalCount, Active, Size, Reclaimable string }
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		total, _ := strconv.Atoi(row.TotalCount)
		active, _ := strconv.Atoi(row.Active)
		list = append(list, DiskUsage{Type: row.Type, Total: total, Active: active, Size: row.Size, Reclaimable: row.Reclaimable})
	}
	return list
}

// ParseUFW reads `ufw status verbose` output.
func ParseUFW(out string) Firewall {
	fw := Firewall{Tool: "ufw"}
	inRules := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Status:"):
			fw.Active = strings.TrimSpace(strings.TrimPrefix(line, "Status:")) == "active"
		case strings.HasPrefix(line, "Default:"):
			fw.Default = strings.TrimSpace(strings.TrimPrefix(line, "Default:"))
		case strings.HasPrefix(line, "--"):
			inRules = true
		case inRules && line != "":
			fw.Rules++
		}
	}
	return fw
}

// ParseSSHD reads `sshd -T` output into lowercase keys.
func ParseSSHD(out string) map[string]string {
	cfg := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		key = strings.ToLower(key)
		if _, dup := cfg[key]; dup {
			// Some keys (port, listenaddress) repeat; keep them all.
			cfg[key] += " " + value
			continue
		}
		cfg[key] = value
	}
	return cfg
}

var (
	unitRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:\\-]*\.(service|socket|timer|mount|path|target)$`)
	containerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	projectRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
)

// ValidProject reports whether name looks like a Compose project name.
func ValidProject(name string) bool { return len(name) < 256 && projectRe.MatchString(name) }

// ValidUnit reports whether name looks like a systemd unit.
func ValidUnit(name string) bool { return len(name) < 256 && unitRe.MatchString(name) }

// ValidContainer reports whether name looks like a container name or ID.
func ValidContainer(name string) bool { return len(name) < 256 && containerRe.MatchString(name) }
