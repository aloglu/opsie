// Package metrics samples the server's live numbers: CPU, memory, disks,
// network, and who's logged in.
package metrics

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"
)

// Snapshot is one sample of the server.
type Snapshot struct {
	At time.Time

	// CPU is the average busy percentage across cores since the last sample.
	CPU     float64
	PerCore []float64
	Cores   int
	Load    [3]float64

	Mem  Memory
	Swap Memory

	Disks    []Disk
	Net      Net
	Host     Host
	Sessions []Session
	Procs    int
}

// Memory is RAM or swap. Used counts what isn't available, so caches the
// kernel would give back don't make the server look full.
type Memory struct {
	Total, Used, Available uint64
	Percent                float64
}

// Disk is one mounted filesystem.
type Disk struct {
	Mount, Device, FSType string
	Total, Used, Free     uint64
	Percent, InodePercent float64
}

// Net is traffic summed over the real network interfaces.
type Net struct {
	// RxRate and TxRate are bytes per second; Ready is false until there
	// have been two samples to compare.
	RxRate, TxRate   float64
	RxTotal, TxTotal uint64
	Ready            bool
	Interfaces       []string
}

// Host describes the machine.
type Host struct {
	Hostname, OS, Kernel, Arch string
	Boot                       time.Time
	Uptime                     time.Duration
	Virtualization             string
}

// Session is a logged-in user.
type Session struct {
	ID                   string // logind session, empty from utmp
	User, Terminal, From string
	Started              time.Time
}

// Collector takes snapshots. Rates are computed between consecutive calls,
// so keep one Collector for the life of the program.
type Collector struct {
	mu sync.Mutex

	host     Host
	haveHost bool

	prevRx, prevTx uint64
	prevAt         time.Time

	sessionsCache []Session
	sessionsAt    time.Time
}

// NewCollector returns a collector primed so its first CPU reading is
// meaningful.
func NewCollector() *Collector {
	_, _ = cpu.Percent(0, true)
	return &Collector{}
}

// Collect takes a snapshot. Readings that fail are left at zero rather than
// failing the whole sample.
func (c *Collector) Collect(ctx context.Context) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	s := Snapshot{At: now}

	if per, err := cpu.PercentWithContext(ctx, 0, true); err == nil && len(per) > 0 {
		s.PerCore = per
		var sum float64
		for _, p := range per {
			sum += p
		}
		s.CPU = sum / float64(len(per))
	}
	s.Cores = len(s.PerCore)
	if s.Cores == 0 {
		s.Cores = runtime.NumCPU()
	}
	if l, err := load.AvgWithContext(ctx); err == nil {
		s.Load = [3]float64{l.Load1, l.Load5, l.Load15}
	}
	if v, err := mem.VirtualMemoryWithContext(ctx); err == nil && v.Total > 0 {
		used := v.Total - v.Available
		s.Mem = Memory{Total: v.Total, Used: used, Available: v.Available, Percent: percent(used, v.Total)}
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		s.Swap = Memory{Total: sw.Total, Used: sw.Used, Available: sw.Free, Percent: percent(sw.Used, sw.Total)}
	}
	s.Disks = disks(ctx)
	s.Net = c.network(ctx, now)
	s.Host = c.hostInfo(ctx, now)
	s.Sessions = c.sessions(ctx, now)
	s.Procs = countProcs()
	return s
}

func percent(part, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

// Filesystems that aren't real storage.
var skipFS = map[string]bool{
	"tmpfs": true, "devtmpfs": true, "squashfs": true, "overlay": true,
	"efivarfs": true, "ramfs": true, "nsfs": true, "fuse.lxcfs": true,
}

func skipMount(mount string) bool {
	for _, prefix := range []string{"/snap/", "/var/lib/docker/", "/run/", "/sys", "/proc", "/dev"} {
		if strings.HasPrefix(mount, prefix) {
			return true
		}
	}
	return false
}

func disks(ctx context.Context) []Disk {
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []Disk
	for _, p := range parts {
		if skipFS[p.Fstype] || skipMount(p.Mountpoint) || seen[p.Device] {
			continue
		}
		u, err := disk.UsageWithContext(ctx, p.Mountpoint)
		if err != nil || u.Total == 0 {
			continue
		}
		seen[p.Device] = true
		out = append(out, Disk{
			Mount: p.Mountpoint, Device: p.Device, FSType: p.Fstype,
			Total: u.Total, Used: u.Used, Free: u.Free,
			Percent: u.UsedPercent, InodePercent: u.InodesUsedPercent,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Mount == "/" || out[j].Mount == "/" {
			return out[i].Mount == "/"
		}
		return out[i].Mount < out[j].Mount
	})
	return out
}

// virtualInterface reports interfaces whose traffic is already counted on
// a physical one, or never leaves the machine.
func virtualInterface(name string) bool {
	if name == "lo" {
		return true
	}
	for _, prefix := range []string{"veth", "docker", "br-", "virbr", "cni", "flannel", "wg", "tun", "tailscale"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func (c *Collector) network(ctx context.Context, now time.Time) Net {
	counters, err := psnet.IOCountersWithContext(ctx, true)
	if err != nil {
		return Net{}
	}
	var n Net
	for _, ct := range counters {
		if virtualInterface(ct.Name) {
			continue
		}
		n.RxTotal += ct.BytesRecv
		n.TxTotal += ct.BytesSent
		n.Interfaces = append(n.Interfaces, ct.Name)
	}
	if !c.prevAt.IsZero() {
		if dt := now.Sub(c.prevAt).Seconds(); dt > 0 && n.RxTotal >= c.prevRx && n.TxTotal >= c.prevTx {
			n.RxRate = float64(n.RxTotal-c.prevRx) / dt
			n.TxRate = float64(n.TxTotal-c.prevTx) / dt
			n.Ready = true
		}
	}
	c.prevRx, c.prevTx, c.prevAt = n.RxTotal, n.TxTotal, now
	return n
}

func (c *Collector) hostInfo(ctx context.Context, now time.Time) Host {
	if !c.haveHost {
		if info, err := host.InfoWithContext(ctx); err == nil {
			c.host = Host{
				Hostname: info.Hostname,
				OS:       prettyOS(info.Platform, info.PlatformVersion),
				Kernel:   info.KernelVersion,
				Arch:     info.KernelArch,
				Boot:     time.Unix(int64(info.BootTime), 0),
			}
			if info.VirtualizationRole == "guest" {
				c.host.Virtualization = info.VirtualizationSystem
			}
			c.haveHost = true
		}
	}
	h := c.host
	if !h.Boot.IsZero() {
		h.Uptime = now.Sub(h.Boot)
	}
	return h
}

// prettyOS prefers os-release's PRETTY_NAME, like "Debian GNU/Linux 13 (trixie)".
func prettyOS(platform, version string) string {
	if f, err := os.Open("/etc/os-release"); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			if v, ok := strings.CutPrefix(scanner.Text(), "PRETTY_NAME="); ok {
				return strings.Trim(v, `"'`)
			}
		}
	}
	return strings.TrimSpace(platform + " " + version)
}

// sessions lists logged-in users. Newer systems (Debian 13 and later) have
// no utmp, so systemd-logind is asked first; it's cached briefly because it
// means running loginctl.
func (c *Collector) sessions(ctx context.Context, now time.Time) []Session {
	if now.Sub(c.sessionsAt) < 10*time.Second {
		return c.sessionsCache
	}
	list, err := logindSessions(ctx)
	if err != nil {
		list = utmpSessions(ctx)
	}
	c.sessionsCache, c.sessionsAt = list, now
	return list
}

func logindSessions(ctx context.Context) ([]Session, error) {
	out, err := exec.CommandContext(ctx, "loginctl", "list-sessions", "--json=short").Output()
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Session string `json:"session"`
		Class   string `json:"class"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	var ids []string
	for _, r := range rows {
		if r.Class == "user" {
			ids = append(ids, r.Session)
		}
	}
	if len(ids) == 0 {
		return []Session{}, nil
	}
	args := append([]string{"show-session"}, ids...)
	args = append(args, "-p", "Id", "-p", "Name", "-p", "TTY", "-p", "RemoteHost", "-p", "Service", "-p", "Type", "-p", "Timestamp")
	out, err = exec.CommandContext(ctx, "loginctl", args...).Output()
	if err != nil {
		return nil, err
	}
	return parseLogind(string(out)), nil
}

// parseLogind reads `loginctl show-session` output for several sessions,
// which comes as blocks separated by blank lines. Sessions without a
// terminal (background jobs, lingering services) are skipped, like who does.
func parseLogind(out string) []Session {
	list := []Session{}
	for _, chunk := range strings.Split(strings.TrimSpace(out), "\n\n") {
		props := map[string]string{}
		for _, line := range strings.Split(chunk, "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				props[k] = v
			}
		}
		switch props["Type"] {
		case "tty", "x11", "wayland", "mir":
		default:
			continue
		}
		s := Session{ID: props["Id"], User: props["Name"], Terminal: props["TTY"], From: props["RemoteHost"]}
		if s.Terminal == "" {
			s.Terminal = props["Service"]
		}
		if t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", props["Timestamp"]); err == nil {
			s.Started = t
		}
		list = append(list, s)
	}
	return list
}

func utmpSessions(ctx context.Context) []Session {
	users, err := host.UsersWithContext(ctx)
	if err != nil {
		return nil
	}
	out := make([]Session, 0, len(users))
	for _, u := range users {
		out = append(out, Session{User: u.User, Terminal: u.Terminal, From: u.Host, Started: time.Unix(int64(u.Started), 0)})
	}
	return out
}

func countProcs() int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if name := e.Name(); e.IsDir() && name[0] >= '0' && name[0] <= '9' {
			n++
		}
	}
	return n
}
