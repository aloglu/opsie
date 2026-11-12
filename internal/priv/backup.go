package priv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aloglu/opsie/internal/backup"
)

// BackupInfo is one backup file, from the description saved next to it.
type BackupInfo struct {
	backup.Manifest
	Path string `json:"path"`
	Size uint64 `json:"size"`
	// SHA256 is the file's checksum when it was written.
	SHA256 string `json:"sha256,omitempty"`
}

// VerifyResult is what checking a backup found.
type VerifyResult struct {
	// Intact is whether the file still matches its checksum.
	Intact      bool `json:"intact"`
	HadChecksum bool `json:"had_checksum"`
	// Opened is whether it was read all the way through, which needs its
	// key when it's encrypted.
	Opened bool `json:"opened"`
	backup.Contents
}

// Encrypted reports whether the backup needs a key to restore.
func (b BackupInfo) Encrypted() bool { return strings.HasSuffix(b.Path, ".age") }

// SetAside is what a restore moved out of the way: the app's data as it
// was just before.
type SetAside struct {
	Project string    `json:"project"`
	At      time.Time `json:"at"`
	Backup  string    `json:"backup"`
	Paths   []string  `json:"paths"`
	Size    uint64    `json:"size"`
}

// Backups is what's in a backup folder.
type Backups struct {
	Dir   string       `json:"dir"`
	Free  uint64       `json:"free"`
	List  []BackupInfo `json:"list"`
	Aside []SetAside   `json:"aside"`
}

// RestoreResult says what a restore did.
type RestoreResult struct {
	Aside   []string `json:"aside"`
	Started bool     `json:"started"`
}

var (
	backupMu     sync.Mutex // one backup or restore at a time
	backupNameRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_.-]*)-(\d{8}-\d{6})\.tar\.gz(\.age)?$`)
	asideNameRe  = regexp.MustCompile(`(^opsie-before-restore-|\.before-restore-)\d{8}-\d{6}$`)
)

// Where backups may be kept: the caller's home, or folders that are
// usually other disks.
var backupRoots = []string{"/mnt", "/media", "/srv", "/var/backups"}

// backupDir checks a backup folder and makes it if it's missing, owned by
// the caller so they can copy backups off the server.
func backupDir(dir, account string, create bool) (string, error) {
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || strings.ContainsAny(dir, "\n\x00") {
		return "", fmt.Errorf("%q isn't a folder path", dir)
	}
	u, err := user.Lookup(account)
	if err != nil {
		return "", err
	}
	// Resolve what exists of the path; the rest gets made.
	existing, rest := dir, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = filepath.Dir(existing)
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	real = filepath.Join(real, rest)
	allowed := false
	roots := append([]string{u.HomeDir}, backupRoots...)
	for _, r := range roots {
		if rr, err := filepath.EvalSymlinks(r); err == nil && strings.HasPrefix(real, rr+string(filepath.Separator)) {
			allowed = true
		}
	}
	if !allowed {
		return "", fmt.Errorf("backups go in a folder in %s, or under %s", u.HomeDir, strings.Join(backupRoots, ", "))
	}
	if info, err := os.Stat(real); err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("%s isn't a folder", dir)
		}
		return real, nil
	}
	if !create {
		return real, nil
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	// Make each missing level, owned by the caller.
	var missing []string
	for p := real; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(p); err == nil {
			break
		}
		missing = append([]string{p}, missing...)
	}
	for _, p := range missing {
		if err := os.Mkdir(p, 0o700); err != nil {
			return "", err
		}
		if err := os.Chown(p, uid, gid); err != nil {
			return "", err
		}
	}
	return real, nil
}

func owner(account string) (uid, gid int) {
	u, err := user.Lookup(account)
	if err != nil {
		return 0, 0
	}
	uid, _ = strconv.Atoi(u.Uid)
	gid, _ = strconv.Atoi(u.Gid)
	return uid, gid
}

func freeAt(dir string) uint64 {
	var fs syscall.Statfs_t
	if syscall.Statfs(dir, &fs) != nil {
		return 0
	}
	return fs.Bavail * uint64(fs.Bsize)
}

// projectParts are a project's containers and what they keep data in.
type projectParts struct {
	containers []Container
	running    []string
	folder     string
	volumes    []string // local named volumes
	skipped    []string // folders mounted from outside the compose folder
}

func readProject(ctx context.Context, project string) (projectParts, error) {
	var p projectParts
	if !ValidProject(project) {
		return p, fmt.Errorf("%q isn't a project name", project)
	}
	ids, err := output(ctx, "docker", "ps", "-aq", "--no-trunc", "--filter", "label=com.docker.compose.project="+project)
	if err != nil {
		return p, err
	}
	if len(strings.Fields(ids)) == 0 {
		return p, fmt.Errorf("project %s has no containers", project)
	}
	out, err := outputBytes(ctx, "docker", append([]string{"inspect", "--type", "container"}, strings.Fields(ids)...)...)
	if err != nil {
		return p, err
	}
	if p.containers, err = ParseInspect(out); err != nil {
		return p, err
	}
	seen := map[string]bool{}
	for _, c := range p.containers {
		if c.State == "running" || c.State == "restarting" {
			p.running = append(p.running, c.ID)
		}
		if p.folder == "" {
			p.folder = c.WorkingDir
		}
	}
	if real, err := filepath.EvalSymlinks(p.folder); err == nil {
		p.folder = real
	}
	if p.folder == "" || !filepath.IsAbs(p.folder) {
		return p, fmt.Errorf("project %s wasn't started with docker compose from a folder, so there's no folder to back up", project)
	}
	if info, err := os.Stat(p.folder); err != nil || !info.IsDir() {
		return p, fmt.Errorf("the folder %s is gone", p.folder)
	}
	for _, c := range p.containers {
		for _, mt := range c.Mounts {
			switch {
			case mt.Type == "volume" && (mt.Driver == "local" || mt.Driver == "") && mt.Name != "" && !seen["v:"+mt.Name]:
				seen["v:"+mt.Name] = true
				p.volumes = append(p.volumes, mt.Name)
			case mt.Type == "bind" && mt.Source != "" && !seen["b:"+mt.Source]:
				seen["b:"+mt.Source] = true
				if mt.Source != p.folder && !strings.HasPrefix(mt.Source, p.folder+"/") {
					p.skipped = append(p.skipped, mt.Source)
				}
			}
		}
	}
	sort.Strings(p.volumes)
	sort.Strings(p.skipped)
	return p, nil
}

// projectRunning lists a project's running containers, and whether it has
// any containers at all.
func projectRunning(ctx context.Context, project string) (running []string, any bool, err error) {
	all, err := output(ctx, "docker", "ps", "-aq", "--no-trunc", "--filter", "label=com.docker.compose.project="+project)
	if err != nil {
		return nil, false, err
	}
	up, err := output(ctx, "docker", "ps", "-q", "--no-trunc", "--filter", "label=com.docker.compose.project="+project)
	if err != nil {
		return nil, false, err
	}
	return strings.Fields(up), len(strings.Fields(all)) > 0, nil
}

var validVolume = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func volumePath(ctx context.Context, name string) (string, error) {
	if !validVolume.MatchString(name) {
		return "", fmt.Errorf("%q isn't a volume name", name)
	}
	out, err := output(ctx, "docker", "volume", "inspect", "--format", "{{.Mountpoint}}", name)
	if err != nil {
		return "", err
	}
	mp := strings.TrimSpace(out)
	if !filepath.IsAbs(mp) || filepath.Base(mp) != "_data" {
		return "", fmt.Errorf("volume %s isn't kept on this disk in the usual way", name)
	}
	return mp, nil
}

// startAgain starts containers that were running before.
func startAgain(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	// The original request may have timed out; this mustn't be skipped.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return run(ctx, "docker", append([]string{"start"}, ids...)...)
}

func opBackupCreate(ctx context.Context, args map[string]string) (any, error) {
	backupMu.Lock()
	defer backupMu.Unlock()
	account := caller(args)
	// Check everything before making the backup folder.
	dir, err := backupDir(args["dir"], account, false)
	if err != nil {
		return nil, err
	}
	p, err := readProject(ctx, args["project"])
	if err != nil {
		return nil, err
	}
	if dir == p.folder || strings.HasPrefix(dir, p.folder+"/") {
		return nil, fmt.Errorf("backups can't go inside the app's own folder (%s); pick a backup folder outside it in Settings", p.folder)
	}
	recipient := strings.TrimSpace(args["recipient"])
	if recipient != "" {
		if _, err := backup.ParseRecipient(recipient); err != nil {
			return nil, err
		}
	}
	keep, _ := strconv.Atoi(args["keep"])
	if keep < 1 || keep > 100 {
		keep = 5
	}
	if dir, err = backupDir(args["dir"], account, true); err != nil {
		return nil, err
	}
	trees := []backup.Tree{{Prefix: "folder", Path: p.folder}}
	for _, v := range p.volumes {
		mp, err := volumePath(ctx, v)
		if err != nil {
			return nil, err
		}
		trees = append(trees, backup.Tree{Prefix: "volumes/" + v, Path: mp})
	}
	size := backup.Size(trees)
	if free := freeAt(dir); free < size+256<<20 {
		return nil, fmt.Errorf("not enough room: the app takes up to %s and %s is free where backups go", bytesWord(size), bytesWord(free))
	}

	now := time.Now()
	host, _ := os.Hostname()
	m := backup.Manifest{Format: backup.Format, Project: args["project"], Created: now.UTC(), Host: host, Opsie: args["version"],
		Folder: p.folder, Volumes: p.volumes, Bytes: size, Stopped: args["stop"] == "1", Recipient: recipient, Skipped: p.skipped}
	name := m.Project + "-" + now.Format("20060102-150405") + ".tar.gz"
	if recipient != "" {
		name += ".age"
	}
	final := filepath.Join(dir, name)
	tmp := filepath.Join(dir, "."+name+".partial")
	uid, gid := owner(account)

	if m.Stopped && len(p.running) > 0 {
		if err := run(ctx, "docker", append([]string{"stop"}, p.running...)...); err != nil {
			_ = startAgain(p.running)
			return nil, fmt.Errorf("couldn't stop the app to back it up: %w", err)
		}
	}
	sum, werr := writeFile(tmp, m, trees, recipient)
	var serr error
	if m.Stopped {
		serr = startAgain(p.running)
	}
	if werr != nil {
		os.Remove(tmp)
		if serr != nil {
			return nil, fmt.Errorf("the backup failed (%v), and the app didn't start again: %w", werr, serr)
		}
		return nil, fmt.Errorf("the backup failed: %w", werr)
	}
	_ = os.Chown(tmp, uid, gid)
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	info := BackupInfo{Manifest: m, Path: final, SHA256: sum}
	if st, err := os.Stat(final); err == nil {
		info.Size = uint64(st.Size())
	}
	side := sidecar(final)
	if data, err := json.MarshalIndent(info, "", "  "); err == nil && os.WriteFile(side, data, 0o600) == nil {
		_ = os.Chown(side, uid, gid)
	}
	pruneBackups(dir, m.Project, keep)
	if serr != nil {
		return info, fmt.Errorf("backed up, but the app didn't start again: %w", serr)
	}
	return info, nil
}

// writeFile writes a backup and returns its checksum.
func writeFile(path string, m backup.Manifest, trees []backup.Tree, recipient string) (string, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	err = backup.Write(io.MultiWriter(f, h), m, trees, recipient)
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return hex.EncodeToString(h.Sum(nil)), err
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// opBackupVerify checks a backup: that the file is as it was written, and,
// when it can be opened, that all of it reads back.
func opBackupVerify(_ context.Context, args map[string]string) (any, error) {
	_, file, err := backupFile(args)
	if err != nil {
		return nil, err
	}
	var res VerifyResult
	var info BackupInfo
	if data, err := os.ReadFile(sidecar(file)); err == nil {
		_ = json.Unmarshal(data, &info)
	}
	if info.SHA256 != "" {
		sum, err := fileSum(file)
		if err != nil {
			return nil, err
		}
		res.HadChecksum = true
		if sum != info.SHA256 {
			return res, errors.New("this backup is damaged: the file has changed since it was written")
		}
		res.Intact = true
	}
	key, err := readKey(args)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(file, ".age") && strings.TrimSpace(key) == "" {
		if !res.HadChecksum {
			return nil, errors.New("this backup is encrypted and its key isn't here, and it has no checksum to compare, so there's nothing to check it with")
		}
		return res, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	_, c, err := backup.Verify(f, key)
	if err != nil {
		return res, err
	}
	res.Opened, res.Contents = true, c
	res.Intact = true
	return res, nil
}

func sidecar(path string) string {
	return strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(path, ".age"), ".gz"), ".tar") + ".json"
}

func bytesWord(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", n>>10)
}

// listBackups reads the descriptions in dir.
func listBackups(dir string) []BackupInfo {
	var out []BackupInfo
	names, _ := os.ReadDir(dir)
	for _, n := range names {
		if !backupNameRe.MatchString(n.Name()) || !n.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, n.Name())
		var info BackupInfo
		if data, err := os.ReadFile(sidecar(path)); err == nil {
			_ = json.Unmarshal(data, &info)
		}
		if info.Project == "" {
			// No description: what the name says.
			mm := backupNameRe.FindStringSubmatch(n.Name())
			info.Project = mm[1]
			info.Created, _ = time.ParseInLocation("20060102-150405", mm[2], time.Local)
		}
		info.Path = path
		if st, err := n.Info(); err == nil {
			info.Size = uint64(st.Size())
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

func pruneBackups(dir, project string, keep int) {
	n := 0
	for _, b := range listBackups(dir) {
		if b.Project != project {
			continue
		}
		if n++; n > keep {
			os.Remove(b.Path)
			os.Remove(sidecar(b.Path))
		}
	}
}

func asideRecord(dir, project string) string {
	return filepath.Join(dir, project+".set-aside.json")
}

func readAside(dir string) []SetAside {
	var out []SetAside
	names, _ := os.ReadDir(dir)
	for _, n := range names {
		if !strings.HasSuffix(n.Name(), ".set-aside.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, n.Name()))
		if err != nil {
			continue
		}
		var a SetAside
		if json.Unmarshal(data, &a) == nil && a.Project+".set-aside.json" == n.Name() {
			out = append(out, a)
		}
	}
	return out
}

func opBackupList(_ context.Context, args map[string]string) (any, error) {
	dir, err := backupDir(args["dir"], caller(args), false)
	if err != nil {
		return nil, err
	}
	b := Backups{Dir: dir, List: listBackups(dir), Aside: readAside(dir)}
	if b.List == nil {
		b.List = []BackupInfo{}
	}
	probe := dir
	for {
		if _, err := os.Stat(probe); err == nil || probe == "/" {
			break
		}
		probe = filepath.Dir(probe)
	}
	b.Free = freeAt(probe)
	return b, nil
}

// backupFile checks that file is a backup in dir.
func backupFile(args map[string]string) (dir, file string, err error) {
	if dir, err = backupDir(args["dir"], caller(args), false); err != nil {
		return "", "", err
	}
	file = args["file"]
	if filepath.Dir(file) != dir || !backupNameRe.MatchString(filepath.Base(file)) {
		return "", "", fmt.Errorf("%s isn't a backup in %s", file, dir)
	}
	info, err := os.Lstat(file)
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("%s isn't a backup file", file)
	}
	return dir, file, nil
}

func opBackupDelete(_ context.Context, args map[string]string) (any, error) {
	_, file, err := backupFile(args)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(file); err != nil {
		return nil, err
	}
	os.Remove(sidecar(file))
	return nil, nil
}

// readKey reads the secret key for a restore: typed in, or from a key
// file in the caller's home.
func readKey(args map[string]string) (string, error) {
	if k := args["identity"]; k != "" {
		return k, nil
	}
	path := args["keyfile"]
	if path == "" {
		return "", nil
	}
	u, err := user.Lookup(caller(args))
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	home, _ := filepath.EvalSymlinks(u.HomeDir)
	if !strings.HasPrefix(real, home+"/") {
		return "", errors.New("the key file has to be in your home folder")
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// restoreFolder checks where a backup wants its folder to go back to.
func restoreFolder(folder string) error {
	if !filepath.IsAbs(folder) || filepath.Clean(folder) != folder || strings.Count(folder, "/") < 2 {
		return fmt.Errorf("this backup's folder %q isn't somewhere opsie will restore to", folder)
	}
	for _, sys := range []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/run", "/sbin", "/sys", "/usr", "/var/lib/docker", "/var/lib/containerd"} {
		if folder == sys || strings.HasPrefix(folder, sys+"/") {
			return fmt.Errorf("this backup's folder is %s, a system folder; opsie won't restore there", folder)
		}
	}
	return nil
}

func opBackupRestore(ctx context.Context, args map[string]string) (any, error) {
	backupMu.Lock()
	defer backupMu.Unlock()
	dir, file, err := backupFile(args)
	if err != nil {
		return nil, err
	}
	key, err := readKey(args)
	if err != nil {
		return nil, err
	}
	stamp := time.Now().Format("20060102-150405")

	// Unpack everything next to where it goes first, so a damaged backup
	// or a wrong key fails before anything is touched.
	// fresh is a volume made for this restore, whose empty folder isn't
	// worth keeping.
	type swap struct {
		staged, live, aside string
		fresh               bool
	}
	var swaps []swap
	var created []string // volumes made for this restore
	cleanup := func() {
		for _, s := range swaps {
			os.RemoveAll(s.staged)
		}
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m, err := backup.Extract(f, key, func(m backup.Manifest) (map[string]string, error) {
		if m.Project != args["project"] {
			return nil, fmt.Errorf("this backup is of %s, not %s", m.Project, args["project"])
		}
		if err := restoreFolder(m.Folder); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(m.Folder), 0o755); err != nil {
			return nil, err
		}
		need := m.Bytes + 256<<20
		if free := freeAt(filepath.Dir(m.Folder)); free < need {
			return nil, fmt.Errorf("not enough room to restore: it needs about %s and %s is free", bytesWord(need), bytesWord(free))
		}
		dirs := map[string]string{}
		folderStage := filepath.Join(filepath.Dir(m.Folder), "."+filepath.Base(m.Folder)+".opsie-restore-"+stamp)
		dirs["folder"] = folderStage
		swaps = append(swaps, swap{staged: folderStage, live: m.Folder, aside: m.Folder + ".before-restore-" + stamp})
		for _, v := range m.Volumes {
			mp, err := volumePath(ctx, v)
			if err != nil {
				// Gone since: make it again, labelled as compose would.
				short := strings.TrimPrefix(v, m.Project+"_")
				if err := run(ctx, "docker", "volume", "create", "--label", "com.docker.compose.project="+m.Project,
					"--label", "com.docker.compose.volume="+short, v); err != nil {
					return nil, fmt.Errorf("couldn't make volume %s: %w", v, err)
				}
				created = append(created, v)
				if mp, err = volumePath(ctx, v); err != nil {
					return nil, err
				}
			}
			base := filepath.Dir(mp)
			if free := freeAt(base); free < need {
				return nil, fmt.Errorf("not enough room for the volumes: they need about %s and %s is free", bytesWord(need), bytesWord(free))
			}
			stage := filepath.Join(base, "opsie-restore-"+stamp)
			dirs["volumes/"+v] = stage
			swaps = append(swaps, swap{stage, mp, filepath.Join(base, "opsie-before-restore-"+stamp), slices.Contains(created, v)})
		}
		return dirs, nil
	})
	if err != nil {
		cleanup()
		for _, v := range created {
			_ = run(context.Background(), "docker", "volume", "rm", v)
		}
		switch {
		case errors.Is(err, backup.ErrWrongKey), errors.Is(err, backup.ErrNeedKey):
			return nil, err
		}
		return nil, fmt.Errorf("nothing was changed: %w", err)
	}

	// Stop the app, swap its data for the backup's, start it again.
	running, haveContainers, err := projectRunning(ctx, m.Project)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("nothing was changed: %w", err)
	}
	if len(running) > 0 {
		if err := run(ctx, "docker", append([]string{"stop"}, running...)...); err != nil {
			_ = startAgain(running)
			cleanup()
			return nil, fmt.Errorf("nothing was changed: couldn't stop the app: %w", err)
		}
	}
	var applied []swap
	failed := false
	for _, s := range swaps {
		if s.fresh {
			_ = os.Remove(s.live) // empty; Remove refuses if it isn't
		}
		if _, err := os.Lstat(s.live); err == nil {
			if os.Rename(s.live, s.aside) != nil {
				failed = true
				break
			}
		} else {
			s.aside = ""
		}
		if os.Rename(s.staged, s.live) != nil {
			if s.aside != "" {
				_ = os.Rename(s.aside, s.live)
			}
			failed = true
			break
		}
		applied = append(applied, s)
	}
	if failed {
		for i := len(applied) - 1; i >= 0; i-- {
			s := applied[i]
			_ = os.Rename(s.live, s.staged)
			if s.aside != "" {
				_ = os.Rename(s.aside, s.live)
			}
		}
		cleanup()
		_ = startAgain(running)
		return nil, errors.New("nothing was changed: couldn't move the restored files into place")
	}
	var res RestoreResult
	for _, s := range applied {
		if s.aside != "" {
			res.Aside = append(res.Aside, s.aside)
		}
	}

	if len(res.Aside) > 0 {
		a := SetAside{Project: m.Project, At: time.Now(), Backup: file, Paths: res.Aside}
		for _, path := range res.Aside {
			a.Size += backup.Size([]backup.Tree{{Path: path}})
		}
		// Earlier set-aside copies stay listed until they're deleted.
		for _, old := range readAside(dir) {
			if old.Project == m.Project {
				a.Paths = append(a.Paths, old.Paths...)
				a.Size += old.Size
			}
		}
		if data, err := json.MarshalIndent(a, "", "  "); err == nil {
			rec := asideRecord(dir, m.Project)
			if os.WriteFile(rec, data, 0o600) == nil {
				uid, gid := owner(caller(args))
				_ = os.Chown(rec, uid, gid)
			}
		}
	}

	switch {
	case len(running) > 0:
		if err := startAgain(running); err != nil {
			return res, fmt.Errorf("restored, but the app didn't start again: %w", err)
		}
		res.Started = true
	case !haveContainers:
		// No containers anymore: bring the app up from its folder.
		if docker, err := lookPath("docker"); err == nil && FindCompose(m.Folder) != "" {
			cmd := exec.CommandContext(ctx, docker, "compose", "--project-directory", m.Folder, "--project-name", m.Project, "up", "--detach")
			cmd.Dir = m.Folder
			if out, err := cmd.CombinedOutput(); err != nil {
				return res, fmt.Errorf("restored, but starting the app failed: %s", lastLines(string(out), 2))
			}
			res.Started = true
		}
	}
	return res, nil
}

// opBackupDiscard deletes what a project's restores set aside.
func opBackupDiscard(_ context.Context, args map[string]string) (any, error) {
	dir, err := backupDir(args["dir"], caller(args), false)
	if err != nil {
		return nil, err
	}
	project := args["project"]
	if !ValidProject(project) {
		return nil, fmt.Errorf("%q isn't a project name", project)
	}
	rec := asideRecord(dir, project)
	data, err := os.ReadFile(rec)
	if err != nil {
		return nil, err
	}
	var a SetAside
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	for _, p := range a.Paths {
		// Only what a restore named, by the names it uses.
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || !asideNameRe.MatchString(filepath.Base(p)) {
			return nil, fmt.Errorf("%s isn't something a restore set aside", p)
		}
		if strings.HasPrefix(filepath.Base(p), "opsie-before-restore-") {
			// A volume's old data sits next to the volume's _data.
			if info, err := os.Stat(filepath.Join(filepath.Dir(p), "_data")); err != nil || !info.IsDir() {
				return nil, fmt.Errorf("%s isn't in a Docker volume", p)
			}
		}
	}
	for _, p := range a.Paths {
		if err := os.RemoveAll(p); err != nil {
			return nil, err
		}
	}
	return nil, os.Remove(rec)
}
