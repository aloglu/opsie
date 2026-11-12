package metrics

import (
	"bytes"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Proc is one process.
type Proc struct {
	PID, PPID int
	Name      string // the kernel's short name
	Cmd       string // full command line
	User      string
	State     string
	RSS       uint64
	// CPU is percent of one core since the last sample, like top.
	CPU     float64
	Nice    int
	Threads int
	// Start is the start time in clock ticks after boot; with the PID it
	// names this process even after the PID is reused.
	Start   uint64
	Started time.Time
	Kernel  bool
	// Unit is the systemd service it belongs to, Container the Docker
	// container ID, Scope the session or other cgroup.
	Unit, Container string
}

const clockTicks = 100 // USER_HZ, 100 on every Linux opsie runs on

// ProcSampler lists processes; CPU use is measured between samples.
type ProcSampler struct {
	mu    sync.Mutex
	prev  map[int]procTime
	at    time.Time
	users map[uint32]string
	boot  time.Time
	page  uint64
}

type procTime struct {
	start uint64
	ticks uint64
}

// NewProcSampler makes a sampler.
func NewProcSampler() *ProcSampler {
	return &ProcSampler{prev: map[int]procTime{}, users: map[uint32]string{}, boot: bootTime(), page: uint64(os.Getpagesize())}
}

func bootTime() time.Time {
	data, _ := os.ReadFile("/proc/stat")
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return time.Unix(n, 0)
		}
	}
	return time.Time{}
}

// Sample reads every process.
func (s *ProcSampler) Sample() []Proc {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(s.at).Seconds()
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	next := make(map[int]procTime, len(dirs))
	procs := make([]Proc, 0, len(dirs))
	for _, dir := range dirs {
		p, ticks, ok := s.read(dir)
		if !ok {
			continue
		}
		next[p.PID] = procTime{p.Start, ticks}
		if prev, ok := s.prev[p.PID]; ok && prev.start == p.Start && elapsed > 0 && ticks >= prev.ticks {
			p.CPU = float64(ticks-prev.ticks) / clockTicks / elapsed * 100
		}
		procs = append(procs, p)
	}
	s.prev, s.at = next, now
	return procs
}

func (s *ProcSampler) read(dir string) (Proc, uint64, bool) {
	pid, err := strconv.Atoi(filepath.Base(dir))
	if err != nil {
		return Proc{}, 0, false
	}
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return Proc{}, 0, false
	}
	p, ticks, ok := ParseStat(stat)
	if !ok || p.PID != pid {
		return Proc{}, 0, false
	}
	p.Started = s.boot.Add(time.Duration(p.Start) * time.Second / clockTicks)
	p.RSS *= s.page
	if info, err := os.Stat(dir); err == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			p.User = s.userName(st.Uid)
		}
	}
	if !p.Kernel {
		if cmd, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
			p.Cmd = strings.TrimSpace(string(bytes.ReplaceAll(cmd, []byte{0}, []byte{' '})))
		}
		if cg, err := os.ReadFile(filepath.Join(dir, "cgroup")); err == nil {
			p.Unit, p.Container = ParseCgroup(string(cg))
		}
	}
	return p, ticks, true
}

func (s *ProcSampler) userName(uid uint32) string {
	if name, ok := s.users[uid]; ok {
		return name
	}
	name := strconv.Itoa(int(uid))
	if u, err := user.LookupId(name); err == nil {
		name = u.Username
	}
	s.users[uid] = name
	return name
}

// ParseStat reads /proc/PID/stat. ticks is user plus system CPU time. RSS
// is in pages.
func ParseStat(data []byte) (Proc, uint64, bool) {
	text := string(data)
	open, close := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
	if open < 0 || close < open {
		return Proc{}, 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(text[:open]))
	if err != nil {
		return Proc{}, 0, false
	}
	f := strings.Fields(text[close+1:])
	if len(f) < 22 {
		return Proc{}, 0, false
	}
	num := func(i int) uint64 { n, _ := strconv.ParseUint(f[i], 10, 64); return n }
	p := Proc{PID: pid, Name: text[open+1 : close], State: f[0]}
	p.PPID = int(num(1))
	nice, _ := strconv.Atoi(f[16])
	p.Nice = nice
	p.Threads = int(num(17))
	p.Start = num(19)
	p.RSS = num(21)
	// Kernel threads are children of kthreadd (PID 2), or kthreadd itself.
	p.Kernel = pid == 2 || p.PPID == 2
	return p, num(11) + num(12), true
}

var (
	cgServiceRe = regexp.MustCompile(`/system\.slice/(?:[^/]+\.slice/)*([^/]+\.service)(?:/|$)`)
	cgDockerRe  = regexp.MustCompile(`docker[-/]([0-9a-f]{64})(?:\.scope)?`)
)

// ParseCgroup finds the service or container a process runs in.
func ParseCgroup(cg string) (unit, container string) {
	for _, line := range strings.Split(strings.TrimSpace(cg), "\n") {
		if !strings.HasPrefix(line, "0::") {
			continue
		}
		path := line[3:]
		if m := cgDockerRe.FindStringSubmatch(path); m != nil {
			return "", m[1]
		}
		if m := cgServiceRe.FindStringSubmatch(path); m != nil {
			return m[1], ""
		}
	}
	return "", ""
}
