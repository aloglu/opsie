// Package motd adds and removes the line in your shell's login file that
// prints opsie's summary when you log in.
package motd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	begin = "# >>> opsie login summary >>>"
	end   = "# <<< opsie login summary <<<"
)

// File is the login file the hook goes in for a shell: the one the shell
// reads when you log in over SSH.
func File(home, shell string) string {
	switch filepath.Base(shell) {
	case "zsh":
		return filepath.Join(home, ".zprofile")
	case "fish":
		return filepath.Join(home, ".config", "fish", "conf.d", "opsie.fish")
	}
	// bash reads the first of these that exists, and only that one.
	for _, name := range []string{".bash_profile", ".bash_login"} {
		if p := filepath.Join(home, name); exists(p) {
			return p
		}
	}
	return filepath.Join(home, ".profile")
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func block(exe, shell string) string {
	q := shellQuote(exe)
	if filepath.Base(shell) == "fish" {
		return fmt.Sprintf("%s\nif status is-login; and isatty stdout; and test -x %s\n    %s motd\nend\n%s\n", begin, q, q, end)
	}
	return fmt.Sprintf("%s\nif [ -t 1 ] && [ -x %s ]; then %s motd; fi\n%s\n", begin, q, q, end)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Installed reports whether the hook is in the login file.
func Installed(home, shell string) bool {
	data, err := os.ReadFile(File(home, shell))
	return err == nil && strings.Contains(string(data), begin)
}

// Install adds the hook, running exe, or updates it if it's there.
func Install(home, shell, exe string) (string, error) {
	path := File(home, shell)
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return path, err
	}
	text := strip(string(data))
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if text != "" {
		text += "\n"
	}
	text += block(exe, shell)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, err
	}
	return path, write(path, text)
}

// Remove takes the hook out again, leaving the rest of the file as it was.
func Remove(home, shell string) (string, bool, error) {
	path := File(home, shell)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, false, nil
	}
	if err != nil {
		return path, false, err
	}
	if !strings.Contains(string(data), begin) {
		return path, false, nil
	}
	text := strip(string(data))
	if filepath.Base(shell) == "fish" && strings.TrimSpace(text) == "" {
		return path, true, os.Remove(path) // opsie's own file
	}
	return path, true, write(path, text)
}

// strip removes the hook and the blank line before it.
func strip(text string) string {
	i := strings.Index(text, begin)
	if i < 0 {
		return text
	}
	j := strings.Index(text[i:], end)
	if j < 0 {
		return text // a broken hook; leave it for the user
	}
	after := strings.TrimPrefix(text[i+j+len(end):], "\n")
	before := strings.TrimRight(text[:i], "\n")
	if before != "" {
		before += "\n"
	}
	return before + after
}

// write replaces path keeping its permissions.
func write(path, text string) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp := path + ".opsie-tmp"
	if err := os.WriteFile(tmp, []byte(text), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
