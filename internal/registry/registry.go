// Package registry asks image registries (Docker Hub, GitHub's and
// others) what a tag points at now, to tell whether a newer image is out.
// It only reads manifests' digests with HEAD requests, which don't count
// against Docker Hub's pull limits.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aloglu/opsie/internal/priv"
)

// Every is how often each tag is asked about.
const Every = 6 * time.Hour

// Ref is an image reference split into its parts.
type Ref struct {
	Host, Repo, Tag string
}

// String is the reference written out the short way.
func (r Ref) String() string { return r.Host + "/" + r.Repo + ":" + r.Tag }

// ErrLocal means the image isn't from a registry: it was built here.
var ErrLocal = errors.New("built here")

// Parse reads an image reference like "nginx", "ghcr.io/me/app:1.2" or
// "localhost:5000/x". Pinned references (with @sha256) and local names
// can't have a newer version and return ErrLocal.
func Parse(ref string) (Ref, error) {
	if strings.Contains(ref, "@") {
		return Ref{}, ErrLocal
	}
	name, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name, tag = ref[:i], ref[i+1:]
	}
	host := "registry-1.docker.io"
	if first, rest, ok := strings.Cut(name, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		host, name = first, rest
	} else if !ok {
		name = "library/" + name
	}
	if host == "docker.io" || host == "index.docker.io" {
		host = "registry-1.docker.io"
	}
	if localHost(host) || tag == "local" || strings.HasPrefix(name, "local/") {
		return Ref{}, ErrLocal
	}
	return Ref{Host: host, Repo: name, Tag: tag}, nil
}

// localHost reports whether a registry runs on this machine, where its
// images were pushed from here.
var localHost = func(host string) bool {
	return strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.")
}

// Check is what a registry said about one image.
type Check struct {
	Ref       string    `json:"ref"`
	Container string    `json:"container"`
	At        time.Time `json:"at"`
	// Newer is whether the tag now points at an image this server
	// doesn't have.
	Newer  bool   `json:"newer"`
	Remote string `json:"remote,omitempty"`
	Err    string `json:"err,omitempty"`
}

var cache = struct {
	sync.Mutex
	digests map[string]cached
}{digests: map[string]cached{}}

type cached struct {
	digest string
	err    error
	at     time.Time
}

// Checks asks about every image the containers use, each tag at most
// once per Every. Images built here are left out.
func Checks(ctx context.Context, images []priv.ImageInfo) []Check {
	var out []Check
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, img := range images {
		ref, err := Parse(img.Ref)
		if err != nil || len(img.Digests) == 0 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			digest, at, err := remoteDigest(ctx, ref)
			c := Check{Ref: img.Ref, Container: img.Container, At: at}
			switch {
			case errors.Is(err, ErrLocal):
				return
			case err != nil:
				c.Err = err.Error()
			default:
				c.Remote = digest
				c.Newer = !hasDigest(img.Digests, digest)
			}
			mu.Lock()
			out = append(out, c)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

func hasDigest(local []string, digest string) bool {
	for _, d := range local {
		if _, after, ok := strings.Cut(d, "@"); ok && after == digest {
			return true
		}
	}
	return false
}

func remoteDigest(ctx context.Context, ref Ref) (string, time.Time, error) {
	key := ref.String()
	cache.Lock()
	c, ok := cache.digests[key]
	cache.Unlock()
	if ok && time.Since(c.at) < Every {
		return c.digest, c.at, c.err
	}
	digest, err := head(ctx, ref)
	if ctx.Err() != nil {
		return "", time.Now(), ctx.Err()
	}
	now := time.Now()
	cache.Lock()
	cache.digests[key] = cached{digest: digest, err: err, at: now}
	cache.Unlock()
	return digest, now, err
}

var client = &http.Client{Timeout: 20 * time.Second}

const accept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json"

// head asks for the tag's manifest digest, getting an anonymous token
// first when the registry wants one.
func head(ctx context.Context, ref Ref) (string, error) {
	u := "https://" + ref.Host + "/v2/" + ref.Repo + "/manifests/" + url.PathEscape(ref.Tag)
	do := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", accept)
		req.Header.Set("User-Agent", "opsie")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return client.Do(req)
	}
	resp, err := do("")
	if err != nil {
		return "", errors.New("couldn't reach " + ref.Host)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		token, err := anonymousToken(ctx, resp.Header.Get("WWW-Authenticate"))
		if err != nil {
			return "", ErrLocal // private or not there: nothing to compare with
		}
		if resp, err = do(token); err != nil {
			return "", errors.New("couldn't reach " + ref.Host)
		}
		resp.Body.Close()
	}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", ErrLocal
	case resp.StatusCode == http.StatusTooManyRequests:
		return "", errors.New(ref.Host + " asked opsie to slow down")
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("%s answered %d", ref.Host, resp.StatusCode)
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if !strings.HasPrefix(digest, "sha256:") {
		return "", errors.New(ref.Host + " didn't say which image the tag is")
	}
	return digest, nil
}

var challengeRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// anonymousToken follows a registry's Bearer challenge without a login.
func anonymousToken(ctx context.Context, challenge string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", errors.New("the registry wants a login")
	}
	params := map[string]string{}
	for _, m := range challengeRe.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Scheme != "https" {
		return "", errors.New("the registry's login address isn't usable")
	}
	q := realm.Query()
	for _, k := range []string{"service", "scope"} {
		if params[k] != "" {
			q.Set(k, params[k])
		}
	}
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token: %d", resp.StatusCode)
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", err
	}
	if t.Token == "" {
		t.Token = t.AccessToken
	}
	if t.Token == "" {
		return "", errors.New("no token")
	}
	return t.Token, nil
}
