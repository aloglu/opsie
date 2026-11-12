package priv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
)

// ComposeFiles are the names docker compose looks for, in its order.
var ComposeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// FindCompose returns the compose file in dir, or "".
func FindCompose(dir string) string {
	for _, name := range ComposeFiles {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// minComposeFree is the least free space where Docker keeps images for
// starting an app, which may pull or build images first.
const minComposeFree = 1 << 30

// appDir checks that dir is a folder with a compose file inside the
// caller's home, where opsie lists apps from.
func appDir(dir, account string) (string, error) {
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || strings.ContainsAny(dir, "\n\x00") {
		return "", fmt.Errorf("%q isn't a folder path", dir)
	}
	u, err := user.Lookup(account)
	if err != nil {
		return "", err
	}
	home, err := filepath.EvalSymlinks(u.HomeDir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(real, home+string(filepath.Separator)) {
		return "", fmt.Errorf("opsie only starts apps from folders in %s", u.HomeDir)
	}
	if info, err := os.Stat(real); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s isn't a folder", dir)
	}
	if FindCompose(real) == "" {
		return "", fmt.Errorf("%s has no compose file", dir)
	}
	return real, nil
}

// opComposeUp starts the app in a folder with docker compose up.
func opComposeUp(ctx context.Context, args map[string]string) (any, error) {
	dir, err := appDir(args["dir"], caller(args))
	if err != nil {
		return nil, err
	}
	path, err := lookPath("docker")
	if err != nil {
		return nil, ErrNoDocker
	}
	if root, err := output(ctx, "docker", "info", "--format", "{{.DockerRootDir}}"); err == nil {
		var fs syscall.Statfs_t
		if syscall.Statfs(strings.TrimSpace(root), &fs) == nil && fs.Bavail*uint64(fs.Bsize) < minComposeFree {
			return nil, fmt.Errorf("less than 1 GB is free for Docker, which may not be enough to download or build this app's images; free up space first")
		}
	}
	cmd := exec.CommandContext(ctx, path, "compose", "--project-directory", dir, "up", "--detach")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("compose up failed: %s", lastLines(string(out), 3))
	}
	return lastLines(string(out), 1), nil
}

// ImageInfo is the image a container runs: how it was asked for, and the
// digests it was pulled with, for checking whether there's a newer one.
type ImageInfo struct {
	Container string   `json:"container"`
	Ref       string   `json:"ref"`
	Digests   []string `json:"digests"`
}

func opImages(ctx context.Context, _ map[string]string) (any, error) {
	if _, err := lookPath("docker"); err != nil {
		return nil, ErrNoDocker
	}
	ids, err := output(ctx, "docker", "ps", "-aq")
	if err != nil {
		return nil, err
	}
	out := []ImageInfo{}
	if len(strings.Fields(ids)) == 0 {
		return out, nil
	}
	list, err := output(ctx, "docker", append([]string{"inspect", "--type", "container", "--format", "{{.Name}}|{{.Config.Image}}|{{.Image}}"}, strings.Fields(ids)...)...)
	if err != nil {
		return nil, err
	}
	digests := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(list), "\n") {
		f := strings.Split(line, "|")
		if len(f) != 3 {
			continue
		}
		d, ok := digests[f[2]]
		if !ok {
			if raw, err := output(ctx, "docker", "image", "inspect", "--format", "{{range .RepoDigests}}{{.}} {{end}}", f[2]); err == nil {
				d = strings.Fields(raw)
			}
			digests[f[2]] = d
		}
		out = append(out, ImageInfo{Container: strings.TrimPrefix(f[0], "/"), Ref: f[1], Digests: d})
	}
	return out, nil
}
