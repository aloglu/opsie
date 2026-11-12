package priv

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DirSize is a folder and how much it holds.
type DirSize struct {
	Path string `json:"path"`
	Size uint64 `json:"size"`
}

// DirUsage is what's inside a folder, biggest first. Files is what the
// folder's own files take, outside its subfolders.
type DirUsage struct {
	Path     string    `json:"path"`
	Size     uint64    `json:"size"`
	Files    uint64    `json:"files"`
	Children []DirSize `json:"children"`
	// More is how many smaller folders aren't listed, and their size.
	More     int    `json:"more"`
	MoreSize uint64 `json:"more_size"`
}

// maxChildren is how many subfolders a listing names.
const maxChildren = 12

// ParseDu reads `du -B1 -d1` output for dir.
func ParseDu(out, dir string) DirUsage {
	u := DirUsage{Path: dir}
	var children []DirSize
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		size, path, ok := strings.Cut(sc.Text(), "\t")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(size, 10, 64)
		if err != nil {
			continue
		}
		if filepath.Clean(path) == dir {
			u.Size = n
			continue
		}
		children = append(children, DirSize{Path: path, Size: n})
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Size > children[j].Size })
	var sum uint64
	for _, c := range children {
		sum += c.Size
	}
	if u.Size > sum {
		u.Files = u.Size - sum
	}
	if len(children) > maxChildren {
		for _, c := range children[maxChildren:] {
			u.More++
			u.MoreSize += c.Size
		}
		children = children[:maxChildren]
	}
	u.Children = children
	if u.Children == nil {
		u.Children = []DirSize{}
	}
	return u
}

// opDiskUsage measures the folders in a path, staying on its filesystem,
// at the lowest priority so it doesn't slow anything else down.
func opDiskUsage(ctx context.Context, args map[string]string) (any, error) {
	dir := args["path"]
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || strings.ContainsAny(dir, "\n\x00") {
		return nil, fmt.Errorf("%q isn't a folder path", dir)
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s isn't a folder", dir)
	}
	du, err := lookPath("du")
	if err != nil {
		return nil, err
	}
	argv := []string{du, "-x", "-B1", "-d1", "--", dir}
	if p, err := lookPath("ionice"); err == nil {
		argv = append([]string{p, "-c3"}, argv...)
	}
	if p, err := lookPath("nice"); err == nil {
		argv = append([]string{p, "-n19"}, argv...)
	}
	// du exits with an error for folders it can't read (like /proc's), but
	// still measures the rest.
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if len(out) == 0 && err != nil {
		return nil, err
	}
	return ParseDu(string(out), dir), nil
}

// Cleanable is space that can be freed safely.
type Cleanable struct {
	Journal    uint64   `json:"journal"`
	AptCache   uint64   `json:"apt_cache"`
	Autoremove []string `json:"autoremove"`
	// AutoremoveSize is what removing them frees, as apt reckons it.
	AutoremoveSize uint64 `json:"autoremove_size"`
}

var (
	remvRe  = regexp.MustCompile(`(?m)^Remv (\S+)`)
	freedRe = regexp.MustCompile(`After this operation, ([\d.,]+) ?([kMG]?B) disk space will be freed`)
)

// ParseAutoremove reads `apt-get -s autoremove`.
func ParseAutoremove(out string) ([]string, uint64) {
	var pkgs []string
	for _, m := range remvRe.FindAllStringSubmatch(out, -1) {
		pkgs = append(pkgs, m[1])
	}
	var size uint64
	if m := freedRe.FindStringSubmatch(out); m != nil {
		size = ParseSize(strings.ReplaceAll(m[1], ",", "") + m[2])
	}
	return pkgs, size
}

func dirBytes(ctx context.Context, path string) uint64 {
	out, err := output(ctx, "du", "-s", "-B1", "--", path)
	if err != nil && out == "" {
		return 0
	}
	n, _ := strconv.ParseUint(strings.Fields(out + " 0")[0], 10, 64)
	return n
}

func opCleanable(ctx context.Context, _ map[string]string) (any, error) {
	var c Cleanable
	for _, p := range []string{"/var/log/journal", "/run/log/journal"} {
		if _, err := os.Stat(p); err == nil {
			c.Journal += dirBytes(ctx, p)
		}
	}
	c.AptCache = dirBytes(ctx, "/var/cache/apt/archives")
	if out, err := simulateAutoremove(ctx); err == nil {
		c.Autoremove, c.AutoremoveSize = ParseAutoremove(out)
	}
	if c.Autoremove == nil {
		c.Autoremove = []string{}
	}
	return c, nil
}

// JournalKeep is how much of the system log trimming keeps.
const JournalKeep = "500M"

func opJournalVacuum(ctx context.Context, _ map[string]string) (any, error) {
	return nil, run(ctx, "journalctl", "--vacuum-size="+JournalKeep)
}

func opAptClean(ctx context.Context, _ map[string]string) (any, error) {
	return nil, run(ctx, "apt-get", "clean")
}

// opAutoremove removes the packages nothing needs anymore, but only if
// they're still exactly the ones that were shown.
func opAutoremove(ctx context.Context, args map[string]string) (any, error) {
	out, err := simulateAutoremove(ctx)
	if err != nil {
		return nil, err
	}
	pkgs, _ := ParseAutoremove(out)
	if strings.Join(pkgs, " ") != args["expect"] {
		return nil, fmt.Errorf("the packages to remove changed since they were shown; look again")
	}
	if len(pkgs) == 0 {
		return nil, nil
	}
	_, err = aptGet(ctx, "autoremove", "--purge")
	return nil, err
}

// simulateAutoremove asks apt what autoremove would do, in English so it
// can be read.
func simulateAutoremove(ctx context.Context) (string, error) {
	path, err := lookPath("apt-get")
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, path, "-s", "-o", "Debug::NoLocking=1", "autoremove", "--purge")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	return string(out), err
}
