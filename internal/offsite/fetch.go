package offsite

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aloglu/opsie/internal/config"
)

// Remote is a file at an off-server destination.
type Remote struct {
	// Source is "s3" or "ssh".
	Source string
	Name   string
	Size   int64
}

// nameRe is what opsie copies off the server: a backup, the description
// beside it, or its own settings. Names come from the other side, and
// become file names here, so nothing else is taken.
var nameRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_.-]*)-(\d{8}-\d{6})\.(tar\.gz(?:\.age)?|json|toml\.age)$`)

// parse takes a name apart: whose it is, when it was made, and whether
// it's a backup ("backup"), the description beside one ("json") or a
// copy of opsie's settings ("settings").
func parse(name string) (app, stamp, kind string, ok bool) {
	m := nameRe.FindStringSubmatch(name)
	if m == nil {
		return "", "", "", false
	}
	switch m[3] {
	case "json":
		kind = "json"
	case "toml.age":
		kind = "settings"
	default:
		kind = "backup"
	}
	return m[1], m[2], kind, true
}

// emptySum is the checksum of a request with nothing in it.
const emptySum = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// Sources names the destinations that are set, "s3" and "ssh".
func Sources(b config.Backup) []string {
	var out []string
	if b.S3.Bucket != "" && b.S3.Endpoint != "" && b.S3.KeyID != "" && b.S3.Secret != "" {
		out = append(out, "s3")
	}
	if b.SSH != "" {
		out = append(out, "ssh")
	}
	return out
}

// SourceName is a source as it's shown.
func SourceName(b config.Backup, source string) string {
	if source == "s3" {
		return "s3://" + b.S3.Bucket + "/" + strings.Trim(b.S3.Prefix, "/")
	}
	return b.SSH
}

func s3Of(b config.Backup) S3 {
	return S3{Endpoint: b.S3.Endpoint, Region: b.S3.Region, Bucket: b.S3.Bucket, Prefix: b.S3.Prefix, KeyID: b.S3.KeyID, Secret: b.S3.Secret}
}

// List reads what's at a source. asUser is whose SSH key is used, when
// this runs as root.
func List(ctx context.Context, b config.Backup, source, asUser string) ([]Remote, error) {
	var all []Remote
	var err error
	if source == "s3" {
		all, err = s3Of(b).List(ctx)
	} else {
		all, err = listSSH(ctx, b.SSH, b.SSHPort, asUser)
	}
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, r := range all {
		if nameRe.MatchString(r.Name) {
			r.Source = source
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Fetch brings one file from a source into dir. It arrives under another
// name and takes its own once it's whole, so a download that stops
// half-way never passes for a backup.
func Fetch(ctx context.Context, b config.Backup, r Remote, dir, asUser string) error {
	if !nameRe.MatchString(r.Name) {
		return fmt.Errorf("%q isn't a backup's name", r.Name)
	}
	final := filepath.Join(dir, r.Name)
	tmp := filepath.Join(dir, "."+r.Name+".partial")
	defer os.Remove(tmp)
	var err error
	if r.Source == "s3" {
		err = s3Of(b).Get(ctx, r.Name, tmp)
	} else {
		err = fetchSSH(ctx, b.SSH, b.SSHPort, r.Name, tmp, asUser)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// bucketURL is where a bucket is listed.
func (s S3) bucketURL() (string, error) {
	u, err := s.objectURL("")
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(strings.TrimRight(s.Endpoint, "/"), ".amazonaws.com") && !strings.Contains(s.Bucket, ".") {
		u.Path = "/"
	} else {
		u.Path = "/" + s.Bucket
	}
	return u.String(), nil
}

// List reads the names under the prefix.
func (s S3) List(ctx context.Context) ([]Remote, error) {
	base, err := s.bucketURL()
	if err != nil {
		return nil, err
	}
	prefix := strings.Trim(s.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	var out []Remote
	token := ""
	for page := 0; page < 100; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
		if err != nil {
			return nil, err
		}
		q := req.URL.Query()
		q.Set("list-type", "2")
		q.Set("prefix", prefix)
		if token != "" {
			q.Set("continuation-token", token)
		}
		req.URL.RawQuery = canonicalQuery(q)
		s.sign(req, emptySum, time.Now().UTC())
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("the storage refused to list the backups (%s): %s", resp.Status, s3Error(string(body)))
		}
		var list struct {
			Contents []struct {
				Key  string
				Size int64
			}
			IsTruncated           bool
			NextContinuationToken string
		}
		if err := xml.Unmarshal(body, &list); err != nil {
			return nil, errors.New("the storage's list of backups can't be read")
		}
		for _, c := range list.Contents {
			name := strings.TrimPrefix(c.Key, prefix)
			if !strings.Contains(name, "/") {
				out = append(out, Remote{Name: name, Size: c.Size})
			}
		}
		if !list.IsTruncated || list.NextContinuationToken == "" {
			break
		}
		token = list.NextContinuationToken
	}
	return out, nil
}

// Get downloads one file to dest.
func (s S3) Get(ctx context.Context, name, dest string) error {
	u, err := s.objectURL(name)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	s.sign(req, emptySum, time.Now().UTC())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("the storage refused the download of %s (%s): %s", path.Base(name), resp.Status, s3Error(string(body)))
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && resp.ContentLength >= 0 && n != resp.ContentLength {
		err = fmt.Errorf("the download of %s stopped early", path.Base(name))
	}
	return err
}

func sshParts(dest string) (host, dir string, err error) {
	m := sshRe.FindStringSubmatch(dest)
	if m == nil || strings.HasPrefix(dest, "-") {
		return "", "", fmt.Errorf("%q isn't a destination like user@host:/folder", dest)
	}
	return m[1] + "@" + m[2], m[3], nil
}

func listSSH(ctx context.Context, dest string, port int, asUser string) ([]Remote, error) {
	host, dir, err := sshParts(dest)
	if err != nil {
		return nil, err
	}
	cmd := asCaller(ctx, asUser, "ssh", append(sshOptions(port), "--", host, "ls -1 -- '"+dir+"'")...)
	var errOut strings.Builder
	cmd.Stderr = &errOut
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("SSH to %s: %s", host, sshProblem(errOut.String(), err))
	}
	var out []Remote
	for _, line := range strings.Split(string(data), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			out = append(out, Remote{Name: name, Size: -1})
		}
	}
	return out, nil
}

func fetchSSH(ctx context.Context, dest string, port int, name, to, asUser string) error {
	host, dir, err := sshParts(dest)
	if err != nil {
		return err
	}
	args := append(sshOptions(port), "-q", "-p", "--", host+":"+dir+"/"+name, to)
	if out, err := asCaller(ctx, asUser, "scp", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("copying %s from %s: %s", name, host, sshProblem(string(out), err))
	}
	return nil
}

// Newest picks, from what a source has, the latest backup of each app
// with the description beside it, and the latest copy of opsie's
// settings. Names sort by their date, so the last of each is the newest.
func Newest(all []Remote) []Remote {
	have := map[string]Remote{}
	for _, r := range all {
		have[r.Name] = r
	}
	type group struct{ app, kind string }
	last := map[group]Remote{}
	stamps := map[group]string{}
	for _, r := range all {
		app, stamp, kind, ok := parse(r.Name)
		if !ok || kind == "json" {
			continue
		}
		g := group{app, kind}
		if stamp > stamps[g] {
			last[g], stamps[g] = r, stamp
		}
	}
	var out []Remote
	for g, r := range last {
		out = append(out, r)
		if side, ok := have[g.app+"-"+stamps[g]+".json"]; ok && g.kind == "backup" {
			out = append(out, side)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Prune deletes, at every destination, the older copies of what name is
// a copy of, so that keep of them stay: an app's backups with the
// descriptions beside them, or opsie's settings. Only names opsie gives
// its own files are ever deleted, and never the newest keep. keep of 0
// deletes nothing.
func Prune(ctx context.Context, b config.Backup, name string, keep int, asUser string) error {
	app, _, kind, ok := parse(filepath.Base(name))
	if !ok || kind == "json" || keep < 1 {
		return nil
	}
	var errs []string
	for _, source := range Sources(b) {
		all, err := List(ctx, b, source, asUser)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		var stamps []string
		for _, r := range all {
			if a, stamp, k, ok := parse(r.Name); ok && a == app && k == kind {
				stamps = append(stamps, stamp)
			}
		}
		sort.Strings(stamps)
		if len(stamps) <= keep {
			continue
		}
		old := map[string]bool{}
		for _, stamp := range stamps[:len(stamps)-keep] {
			old[stamp] = true
		}
		var gone []string
		for _, r := range all {
			a, stamp, k, ok := parse(r.Name)
			if ok && a == app && old[stamp] && (k == kind || (kind == "backup" && k == "json")) {
				gone = append(gone, r.Name)
			}
		}
		if source == "s3" {
			for _, n := range gone {
				if err := s3Of(b).Delete(ctx, n); err != nil {
					errs = append(errs, err.Error())
					break
				}
			}
		} else if err := removeSSH(ctx, b.SSH, b.SSHPort, gone, asUser); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// Delete removes one file from the bucket.
func (s S3) Delete(ctx context.Context, name string) error {
	u, err := s.objectURL(name)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.String(), nil)
	if err != nil {
		return err
	}
	s.sign(req, emptySum, time.Now().UTC())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("the storage refused to delete %s (%s): %s", name, resp.Status, s3Error(string(body)))
	}
	return nil
}

// removeSSH deletes files in the destination's folder. The names are
// ones nameRe allows, so they're safe to hand to the shell there.
func removeSSH(ctx context.Context, dest string, port int, names []string, asUser string) error {
	if len(names) == 0 {
		return nil
	}
	host, dir, err := sshParts(dest)
	if err != nil {
		return err
	}
	cmd := "rm -f --"
	for _, n := range names {
		if !nameRe.MatchString(n) {
			return fmt.Errorf("%q isn't a backup's name", n)
		}
		cmd += " '" + dir + "/" + n + "'"
	}
	if out, err := asCaller(ctx, asUser, "ssh", append(sshOptions(port), "--", host, cmd)...).CombinedOutput(); err != nil {
		return fmt.Errorf("SSH to %s: %s", host, sshProblem(string(out), err))
	}
	return nil
}
