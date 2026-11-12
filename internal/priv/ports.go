package priv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// portLineRe is a short-syntax port in a compose file, like
// `- "8080:80"`, `- 0.0.0.0:5432:5432` or `- '27015:27015/udp'`.
var portLineRe = regexp.MustCompile(`^(\s*-\s*)(["']?)((?:[0-9.]+|\[[0-9a-fA-F:]+\]|::):)?([0-9]+)(:[0-9]+(?:-[0-9]+)?)(/(?:tcp|udp))?(["']?)(\s*(?:#.*)?)$`)

// BindLocal rewrites the line in a compose file that publishes port, so it
// listens on 127.0.0.1 only. It reports how many lines matched; only
// exactly one is changed.
func BindLocal(text, port, proto string) (string, int) {
	lines := strings.Split(text, "\n")
	at := -1
	n := 0
	for i, l := range lines {
		m := portLineRe.FindStringSubmatch(l)
		if m == nil || m[4] != port || m[2] != m[7] {
			continue
		}
		p := strings.TrimPrefix(m[6], "/")
		if p == "" {
			p = "tcp"
		}
		if p != proto {
			continue
		}
		n++
		at = i
	}
	if n != 1 {
		return text, n
	}
	m := portLineRe.FindStringSubmatch(lines[at])
	lines[at] = m[1] + m[2] + "127.0.0.1:" + m[4] + m[5] + m[6] + m[7] + m[8]
	return strings.Join(lines, "\n"), 1
}

// opBindLocal publishes a project's port on 127.0.0.1 only, by editing its
// compose file and recreating what changed.
func opBindLocal(ctx context.Context, args map[string]string) (any, error) {
	port, proto := args["port"], args["proto"]
	if _, err := fmt.Sscanf(port, "%d", new(int)); err != nil || (proto != "tcp" && proto != "udp") {
		return nil, fmt.Errorf("%q/%q isn't a port", port, proto)
	}
	base, err := composeArgs(ctx, args["project"])
	if err != nil {
		return nil, err
	}
	var files []string
	for i := 0; i+1 < len(base); i++ {
		if base[i] == "--file" {
			files = append(files, base[i+1])
		}
	}
	file, text, total := "", "", 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		out, n := BindLocal(string(data), port, proto)
		total += n
		if n == 1 {
			file, text = f, out
		}
	}
	switch {
	case total == 0:
		return nil, fmt.Errorf("couldn't find port %s/%s written as a simple \"%s:…\" line in the compose file (it may come from a variable or the long form); change it by hand to \"127.0.0.1:%s:…\"", port, proto, port, port)
	case total > 1:
		return nil, fmt.Errorf("port %s/%s is in the compose files more than once; change the right one by hand to \"127.0.0.1:%s:…\"", port, proto, port)
	}
	info, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	old, _ := os.ReadFile(file)
	keep := file + ".before-opsie-" + time.Now().Format("20060102-150405")
	if err := writeLike(keep, old, info); err != nil {
		return nil, err
	}
	if err := writeLike(file, []byte(text), info); err != nil {
		return nil, err
	}
	docker, err := lookPath("docker")
	if err != nil {
		return nil, ErrNoDocker
	}
	if out, err := exec.CommandContext(ctx, docker, append(base, "up", "--detach")...).CombinedOutput(); err != nil {
		// Put the file back the way it was, and the app with it.
		_ = writeLike(file, old, info)
		_ = os.Remove(keep)
		_ = exec.CommandContext(ctx, docker, append(base, "up", "--detach")...).Run()
		return nil, fmt.Errorf("compose up failed, so the file is back as it was: %s", lastLines(string(out), 2))
	}
	return keep, nil
}

// writeLike writes data to path with the mode and owner of info.
func writeLike(path string, data []byte, info os.FileInfo) error {
	tmp := path + ".opsie-tmp"
	if err := os.WriteFile(tmp, data, info.Mode().Perm()); err != nil {
		return err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(tmp, int(st.Uid), int(st.Gid))
	}
	return os.Rename(tmp, path)
}
