package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Set writes one setting into the file at path, keeping everything else in
// it as it was: comments, order and other settings. key is dotted, like
// "notify.ntfy_url"; a nil value removes the setting so the default
// applies. The result is checked before it's written, so a mistake never
// leaves a file opsie can't read.
func Set(path, key string, value any) error {
	table, name := "", key
	if i := strings.LastIndex(key, "."); i >= 0 {
		table, name = key[:i], key[i+1:]
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text, err := edit(string(data), table, name, value)
	if err != nil {
		return err
	}
	var check Config
	if _, err := toml.Decode(text, &check); err != nil {
		return fmt.Errorf("the change would break %s: %w", filepath.Base(path), err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// The file can hold tokens, so a new one is private.
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp := path + ".opsie-tmp"
	if err := os.WriteFile(tmp, []byte(text), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var headerRe = regexp.MustCompile(`^\s*\[\s*([A-Za-z0-9_.-]+)\s*\]\s*(#.*)?$`)

// edit sets or removes name in table (or at the top when table is "").
func edit(text, table, name string, value any) (string, error) {
	line := ""
	if value != nil {
		var err error
		if line, err = encodeLine(name, value); err != nil {
			return "", err
		}
	}
	lines := strings.Split(text, "\n")
	if text == "" {
		lines = nil
	}
	// The table's region: from its header (or the top) to the next header.
	start, end := 0, len(lines)
	found := table == ""
	for i, l := range lines {
		m := headerRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if found && i >= start {
			end = i
			break
		}
		if m[1] == table {
			found, start = true, i+1
		}
	}
	if !found {
		if value == nil {
			return text, nil
		}
		out := strings.TrimRight(text, "\n")
		if out != "" {
			out += "\n\n"
		}
		return out + "[" + table + "]\n" + line + "\n", nil
	}
	keyRe := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(name) + `\s*=`)
	for i := start; i < end; i++ {
		if !keyRe.MatchString(lines[i]) {
			continue
		}
		if strings.Contains(lines[i], `"""`) || strings.Contains(lines[i], `'''`) {
			return "", fmt.Errorf("%s spans several lines in the file; change it there by hand", name)
		}
		if value == nil {
			lines = append(lines[:i], lines[i+1:]...)
		} else {
			lines[i] = line + trailingComment(lines[i])
		}
		return strings.Join(lines, "\n"), nil
	}
	if value == nil {
		return text, nil
	}
	// Add it after the region's last setting, before any blank lines.
	at := end
	for at > start && strings.TrimSpace(lines[at-1]) == "" {
		at--
	}
	lines = append(lines[:at], append([]string{line}, lines[at:]...)...)
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, nil
}

// encodeLine writes `name = value` the way the toml package would.
func encodeLine(name string, value any) (string, error) {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(map[string]any{name: value}); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}

// trailingComment keeps a comment after a value, like `level = "warn"  # or crit`.
func trailingComment(line string) string {
	inString := byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inString != 0:
			if c == '\\' && inString == '"' {
				i++
			} else if c == inString {
				inString = 0
			}
		case c == '"' || c == '\'':
			inString = c
		case c == '#':
			j := i
			for j > 0 && (line[j-1] == ' ' || line[j-1] == '\t') {
				j--
			}
			return line[j:]
		}
	}
	return ""
}

// FileValues reads which settings the file itself sets, by dotted key.
func FileValues(path string) map[string]any {
	out := map[string]any{}
	var raw map[string]any
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return out
	}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			if sub, ok := v.(map[string]any); ok {
				walk(prefix+k+".", sub)
				continue
			}
			out[prefix+k] = v
		}
	}
	walk("", raw)
	return out
}

// Values is every effective setting by dotted key, defaults included.
func (c Config) Values() map[string]any {
	var b bytes.Buffer
	_ = toml.NewEncoder(&b).Encode(c)
	var raw map[string]any
	_, _ = toml.Decode(b.String(), &raw)
	out := map[string]any{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			if sub, ok := v.(map[string]any); ok {
				walk(prefix+k+".", sub)
				continue
			}
			out[prefix+k] = v
		}
	}
	walk("", raw)
	return out
}

// ValidHours reports whether s is a range like "23:00-08:00".
func ValidHours(s string) bool {
	_, _, ok := parseHours(s)
	return ok
}
