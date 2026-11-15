package offsite

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/aloglu/opsie/internal/config"
)

// sshRe is a destination like backup@nas.example:/srv/backups/harbor. It's
// strict because the folder is also given to a shell on the other side.
var sshRe = regexp.MustCompile(`^([A-Za-z0-9._-]+)@([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\]):([A-Za-z0-9._/~-]+)$`)

// ValidSSH reports whether s is a destination opsie can copy to.
func ValidSSH(s string) bool {
	_, _, err := sshParts(s)
	return err == nil
}

// sshParts takes a destination apart into who@where and the folder
// there. The folder is given to the other side's shell in quotes, where
// "~" wouldn't mean home, so "~/backups" becomes "backups": a folder
// that doesn't start with "/" is already looked for in home. "~" anywhere
// else isn't taken, since it would make a folder of that name.
func sshParts(dest string) (host, dir string, err error) {
	m := sshRe.FindStringSubmatch(dest)
	if m == nil || strings.HasPrefix(dest, "-") {
		return "", "", fmt.Errorf("%q isn't a destination like user@host:/folder", dest)
	}
	dir = m[3]
	if dir == "~" || dir == "~/" {
		dir = "."
	} else if rest, ok := strings.CutPrefix(dir, "~/"); ok {
		dir = rest
	}
	if strings.Contains(dir, "~") {
		return "", "", fmt.Errorf("%q has a ~ that isn't at the start of the folder", dest)
	}
	return m[1] + "@" + m[2], dir, nil
}

// Configured reports whether copies have a place to go.
func Configured(b config.Backup) bool {
	return b.SSH != "" || (b.S3.Bucket != "" && b.S3.Endpoint != "" && b.S3.KeyID != "" && b.S3.Secret != "")
}

// Where names the destinations, for showing.
func Where(b config.Backup) []string {
	var out []string
	if b.SSH != "" {
		out = append(out, b.SSH)
	}
	if b.S3.Bucket != "" {
		out = append(out, "s3://"+b.S3.Bucket+"/"+strings.Trim(b.S3.Prefix, "/"))
	}
	return out
}

// Copy sends files to every destination that's set. asUser is whose SSH
// key is used, when this runs as root.
func Copy(ctx context.Context, b config.Backup, files []string, asUser string) error {
	if !Configured(b) {
		return ErrNoDestination
	}
	var errs []string
	if b.SSH != "" {
		if err := copySSH(ctx, b.SSH, b.SSHPort, files, asUser); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if b.S3.Bucket != "" {
		s := S3{Endpoint: b.S3.Endpoint, Region: b.S3.Region, Bucket: b.S3.Bucket, Prefix: b.S3.Prefix, KeyID: b.S3.KeyID, Secret: b.S3.Secret}
		for _, f := range files {
			if err := s.Put(ctx, f); err != nil {
				errs = append(errs, "S3: "+err.Error())
				break
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("copying off the server failed: %s", strings.Join(errs, "; "))
	}
	// With the new copies there, the oldest make room, if a limit is set.
	for _, f := range files {
		if err := Prune(ctx, b, f, b.KeepRemote, asUser); err != nil {
			return fmt.Errorf("copied, but the older copies off the server couldn't be cleared: %w", err)
		}
	}
	return nil
}

// asCaller runs a command as account when this process is root, so it uses
// their SSH key and known hosts.
func asCaller(ctx context.Context, account, name string, args ...string) *exec.Cmd {
	if os.Geteuid() == 0 && account != "" && account != "root" {
		return exec.CommandContext(ctx, "runuser", append([]string{"-u", account, "--", name}, args...)...)
	}
	return exec.CommandContext(ctx, name, args...)
}

func sshOptions(port int) []string {
	// BatchMode never asks for a password; StrictHostKeyChecking refuses
	// a host whose key isn't known yet, rather than trusting it blindly.
	opts := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=20"}
	if port > 0 && port != 22 {
		opts = append(opts, "-o", "Port="+strconv.Itoa(port))
	}
	return opts
}

func copySSH(ctx context.Context, dest string, port int, files []string, asUser string) error {
	host, dir, err := sshParts(dest)
	if err != nil {
		return err
	}
	mk := asCaller(ctx, asUser, "ssh", append(sshOptions(port), "--", host, "mkdir -p -- '"+dir+"'")...)
	if out, err := mk.CombinedOutput(); err != nil {
		return fmt.Errorf("SSH to %s: %s", host, sshProblem(string(out), err))
	}
	args := append(sshOptions(port), "-q", "-p", "--")
	args = append(args, files...)
	args = append(args, host+":"+dir+"/")
	if out, err := asCaller(ctx, asUser, "scp", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("copying to %s: %s", host, sshProblem(string(out), err))
	}
	return nil
}

// sshProblem turns ssh's complaints into something to act on.
func sshProblem(out string, err error) string {
	out = strings.TrimSpace(out)
	switch {
	case strings.Contains(out, "Host key verification failed"), strings.Contains(out, "No ED25519 host key is known"), strings.Contains(out, "No RSA host key is known"):
		return "its host key isn't known yet. Connect once yourself with ssh, check the fingerprint and accept it, then try again"
	case strings.Contains(out, "Permission denied"):
		return "it refused your SSH key. Add your public key to its authorized_keys"
	case out != "":
		lines := strings.Split(out, "\n")
		return lines[len(lines)-1]
	}
	return err.Error()
}

// Sidecar is the description saved next to a backup, which goes along.
func Sidecar(backup string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(backup, ".age"), ".gz"), ".tar")
	if _, err := os.Stat(base + ".json"); err != nil {
		return ""
	}
	return filepath.Clean(base + ".json")
}
