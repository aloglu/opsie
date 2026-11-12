package priv

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Package is an installed package with a newer version available.
type Package struct {
	Name     string `json:"name"`
	From     string `json:"from"`
	To       string `json:"to"`
	Suite    string `json:"suite"`
	Security bool   `json:"security,omitempty"`
}

// AptState is what apt knows about updates.
type AptState struct {
	Packages []Package `json:"packages"`
	// ListsAt is when the package lists were last downloaded.
	ListsAt time.Time `json:"lists_at"`
	// Auto is whether unattended-upgrades installs security updates.
	AutoInstalled bool      `json:"auto_installed"`
	AutoOn        bool      `json:"auto_on"`
	AutoAt        time.Time `json:"auto_at"` // last unattended run
	Free          uint64    `json:"free"`    // bytes free in /var/cache/apt
}

// Stale is a service still running code from a library an update
// replaced, so it needs a restart to pick up the fix.
type Stale struct {
	Unit string   `json:"unit"`
	Libs []string `json:"libs"`
	PIDs []int    `json:"pids"`
}

// ParseUpgradable reads `apt list --upgradable`.
func ParseUpgradable(out string) []Package {
	pkgs := []Package{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 6 || !strings.Contains(f[0], "/") || f[3] != "[upgradable" {
			continue
		}
		name, suites, _ := strings.Cut(f[0], "/")
		p := Package{Name: name, To: f[1], From: strings.TrimSuffix(f[5], "]"), Suite: strings.Split(suites, ",")[0]}
		for _, s := range strings.Split(suites, ",") {
			if strings.HasSuffix(s, "-security") {
				p.Security, p.Suite = true, s
			}
		}
		pkgs = append(pkgs, p)
	}
	sort.SliceStable(pkgs, func(i, j int) bool {
		if pkgs[i].Security != pkgs[j].Security {
			return pkgs[i].Security
		}
		return pkgs[i].Name < pkgs[j].Name
	})
	return pkgs
}

// ParseAutoUpgrades reads apt.conf settings for unattended upgrades:
// both the list refresh and the upgrade itself must be on.
func ParseAutoUpgrades(conf string) bool {
	vals := map[string]string{}
	re := regexp.MustCompile(`(APT::Periodic::[A-Za-z-]+)\s+"([^"]*)"`)
	for _, m := range re.FindAllStringSubmatch(conf, -1) {
		vals[m[1]] = m[2]
	}
	on := func(k string) bool { v := vals[k]; return v != "" && v != "0" }
	return on("APT::Periodic::Update-Package-Lists") && on("APT::Periodic::Unattended-Upgrade")
}

func modTime(path string) time.Time {
	if info, err := os.Stat(path); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

func opApt(ctx context.Context, _ map[string]string) (any, error) {
	out, err := output(ctx, "apt", "list", "--upgradable")
	if err != nil {
		return nil, err
	}
	s := AptState{Packages: ParseUpgradable(out)}
	s.ListsAt = modTime("/var/lib/apt/periodic/update-stamp")
	if t := modTime("/var/lib/apt/lists/partial"); t.After(s.ListsAt) {
		s.ListsAt = t
	}
	if t := modTime("/var/cache/apt/pkgcache.bin"); t.After(s.ListsAt) {
		s.ListsAt = t
	}
	_, err = lookPath("unattended-upgrade")
	s.AutoInstalled = err == nil
	var conf strings.Builder
	files, _ := filepath.Glob("/etc/apt/apt.conf.d/*")
	sort.Strings(files)
	for _, f := range files {
		if data, err := os.ReadFile(f); err == nil {
			conf.Write(data)
			conf.WriteByte('\n')
		}
	}
	s.AutoOn = s.AutoInstalled && ParseAutoUpgrades(conf.String())
	s.AutoAt = modTime("/var/lib/apt/periodic/unattended-upgrades-stamp")
	var st syscall.Statfs_t
	if syscall.Statfs("/var/cache/apt", &st) == nil {
		s.Free = st.Bavail * uint64(st.Bsize)
	}
	return s, nil
}

var pkgRe = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)

// minAptFree is the least free space opsie will start an upgrade with.
const minAptFree = 500 << 20

func checkAptRoom() error {
	var st syscall.Statfs_t
	if syscall.Statfs("/var/cache/apt", &st) == nil && st.Bavail*uint64(st.Bsize) < minAptFree {
		return errors.New("there's less than 500 MB free for downloading packages; free some space first")
	}
	return nil
}

func opAptUpdate(ctx context.Context, _ map[string]string) (any, error) {
	_, err := aptGet(ctx, "update")
	return nil, err
}

func opAptUpgrade(ctx context.Context, args map[string]string) (any, error) {
	if err := checkAptRoom(); err != nil {
		return nil, err
	}
	if p := args["package"]; p != "" {
		if !pkgRe.MatchString(p) {
			return nil, fmt.Errorf("%q isn't a package name", p)
		}
		_, err := aptGet(ctx, "install", "--only-upgrade", p)
		return nil, err
	}
	// --with-new-pkgs lets kernel updates in, which pull in a new package;
	// nothing is ever removed.
	_, err := aptGet(ctx, "upgrade", "--with-new-pkgs")
	return nil, err
}

const autoFile = "/etc/apt/apt.conf.d/20auto-upgrades"

func opAutoOn(ctx context.Context, _ map[string]string) (any, error) {
	if _, err := lookPath("unattended-upgrade"); err != nil {
		if _, err := aptGet(ctx, "install", "unattended-upgrades"); err != nil {
			return nil, err
		}
	}
	content := "APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"1\";\n"
	if err := writeFileAtomic(autoFile, []byte(content), 0o644); err != nil {
		return nil, err
	}
	return nil, run(ctx, "systemctl", "enable", "--now", "apt-daily.timer", "apt-daily-upgrade.timer")
}

var serviceRe = regexp.MustCompile(`^0::/system\.slice/(?:[^/]+\.slice/)*([^/]+\.service)$`)

// IsLibrary reports whether a deleted mapping is a replaced shared
// library, rather than shared memory or a scratch file.
func IsLibrary(path string) bool {
	if !(strings.HasPrefix(path, "/usr/lib/") || strings.HasPrefix(path, "/lib/") || strings.HasPrefix(path, "/usr/libexec/") || strings.HasPrefix(path, "/usr/bin/") || strings.HasPrefix(path, "/usr/sbin/")) {
		return false
	}
	base := filepath.Base(path)
	return strings.Contains(base, ".so") || strings.HasPrefix(path, "/usr/bin/") || strings.HasPrefix(path, "/usr/sbin/")
}

// opStale finds services running replaced libraries, by looking for
// deleted files in each process's memory map.
func opStale(context.Context, map[string]string) (any, error) {
	dirs, err := filepath.Glob("/proc/[0-9]*")
	if err != nil {
		return nil, err
	}
	byUnit := map[string]*Stale{}
	for _, dir := range dirs {
		pid, _ := strconv.Atoi(filepath.Base(dir))
		libs := deletedLibs(filepath.Join(dir, "maps"))
		if len(libs) == 0 {
			continue
		}
		unit := ""
		if data, err := os.ReadFile(filepath.Join(dir, "cgroup")); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if m := serviceRe.FindStringSubmatch(line); m != nil {
					unit = m[1]
				} else if line == "0::/init.scope" {
					unit = "systemd"
				}
			}
		}
		// Processes in containers, user sessions and the like aren't
		// services opsie can restart.
		if unit == "" || strings.HasPrefix(unit, "docker-") {
			continue
		}
		s := byUnit[unit]
		if s == nil {
			s = &Stale{Unit: unit}
			byUnit[unit] = s
		}
		s.PIDs = append(s.PIDs, pid)
		for _, l := range libs {
			if !contains(s.Libs, l) {
				s.Libs = append(s.Libs, l)
			}
		}
	}
	out := []Stale{}
	for _, s := range byUnit {
		sort.Strings(s.Libs)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Unit < out[j].Unit })
	return out, nil
}

func deletedLibs(maps string) []string {
	f, err := os.Open(maps)
	if err != nil {
		return nil
	}
	defer f.Close()
	var libs []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasSuffix(line, " (deleted)") {
			continue
		}
		i := strings.Index(line, " /")
		if i < 0 {
			continue
		}
		path := strings.TrimSuffix(line[i+1:], " (deleted)")
		if IsLibrary(path) && !contains(libs, path) {
			libs = append(libs, path)
		}
	}
	return libs
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func opReexecSystemd(ctx context.Context, _ map[string]string) (any, error) {
	return nil, run(ctx, "systemctl", "daemon-reexec")
}
