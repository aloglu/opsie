// Package update finds newer opsie releases, collects what changed in each,
// and installs the newest one in place of the running binary.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/aloglu/opsie/internal/app"
)

const checkInterval = 24 * time.Hour

// Overridable for tests and mirrors.
var (
	APIBase      = envOr("OPSIE_API_BASE", "https://api.github.com")
	DownloadBase = envOr("OPSIE_DOWNLOAD_BASE", "https://github.com/"+app.Repo+"/releases")
	httpClient   = &http.Client{Timeout: 30 * time.Second}
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Release is a published GitHub release.
type Release struct {
	Tag        string    `json:"tag_name"`
	Name       string    `json:"name"`
	Body       string    `json:"body"`
	URL        string    `json:"html_url"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Published  time.Time `json:"published_at"`
}

// Newer reports whether latest is a newer release than current.
func Newer(current, latest string) bool {
	return app.IsRelease(current) && app.IsRelease(latest) && semver.Compare(latest, current) > 0
}

func getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "opsie-updater")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errors.New("no releases have been published yet")
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return errors.New("GitHub is rate-limiting requests from this server; try again in a while")
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("GitHub answered %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Releases lists recent releases, newest first.
func Releases(ctx context.Context) ([]Release, error) {
	var list []Release
	if err := getJSON(ctx, APIBase+"/repos/"+app.Repo+"/releases?per_page=50", &list); err != nil {
		return nil, err
	}
	return list, nil
}

// Pending returns the published releases newer than current, newest first.
func Pending(current string, releases []Release) []Release {
	var out []Release
	for _, r := range releases {
		if !r.Draft && !r.Prerelease && Newer(current, r.Tag) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return semver.Compare(out[i].Tag, out[j].Tag) > 0 })
	return out
}

// Highlights pulls the bullet points out of release notes.
func Highlights(notes string, limit int) []string {
	var out []string
	for _, line := range strings.Split(notes, "\n") {
		line = strings.TrimSpace(line)
		text, ok := strings.CutPrefix(line, "- ")
		if !ok {
			text, ok = strings.CutPrefix(line, "* ")
		}
		if !ok {
			continue
		}
		text = strings.NewReplacer("**", "", "`", "").Replace(strings.TrimSpace(text))
		if text == "" {
			continue
		}
		out = append(out, text)
		if len(out) == limit {
			break
		}
	}
	return out
}

type meta struct {
	CheckedAt time.Time `json:"update_checked_at"`
	Latest    string    `json:"latest_version"`
}

func loadMeta(path string) meta {
	var m meta
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

func saveMeta(path string, m meta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Cached returns the newest release seen so far if it's newer than
// current, without contacting GitHub.
func Cached(metaPath, current string) (string, bool) {
	m := loadMeta(metaPath)
	return m.Latest, Newer(current, m.Latest)
}

// Daily returns the newest release if it's newer than current, asking
// GitHub at most once a day and answering from metaPath otherwise.
func Daily(ctx context.Context, metaPath, current string) (string, bool) {
	if !app.IsRelease(current) {
		return "", false
	}
	m := loadMeta(metaPath)
	if time.Since(m.CheckedAt) < checkInterval {
		return m.Latest, Newer(current, m.Latest)
	}
	var latest Release
	if err := getJSON(ctx, APIBase+"/repos/"+app.Repo+"/releases/latest", &latest); err != nil {
		// Don't ask again for a day even when offline or rate-limited.
		m.CheckedAt = time.Now()
		_ = saveMeta(metaPath, m)
		return m.Latest, Newer(current, m.Latest)
	}
	m = meta{CheckedAt: time.Now(), Latest: latest.Tag}
	_ = saveMeta(metaPath, m)
	return m.Latest, Newer(current, m.Latest)
}

// Forget clears the remembered latest version, after an update.
func Forget(metaPath, installed string) {
	_ = saveMeta(metaPath, meta{CheckedAt: time.Now(), Latest: installed})
}
