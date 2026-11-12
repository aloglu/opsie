// Package sites checks that public websites answer, the way a visitor
// would see them: over the internet, through Cloudflare when they're
// behind a tunnel.
package sites

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aloglu/opsie/internal/priv"
)

// Every is how often each site is checked; results in between are reused.
const Every = 2 * time.Minute

// Result is one check of one site.
type Result struct {
	URL  string `json:"url"`
	Host string `json:"host"`
	// Route is whether the site came from a tunnel's hostnames rather
	// than the settings.
	Route  bool          `json:"route"`
	At     time.Time     `json:"at"`
	Status int           `json:"status,omitempty"`
	Took   time.Duration `json:"took"`
	// Err says why there was no answer.
	Err string `json:"err,omitempty"`
	// CertExpires is when the site's certificate runs out.
	CertExpires time.Time `json:"cert_expires,omitzero"`
}

// Up reports whether the site answered with something other than a
// server error.
func (r Result) Up() bool { return r.Err == "" && r.Status > 0 && r.Status < 500 }

// Problem says what's wrong in a few words, or "" when it's up.
func (r Result) Problem() string {
	switch {
	case r.Err != "":
		return r.Err
	case r.Status == 530:
		return "Cloudflare can't reach the tunnel (530)"
	case r.Status == 502 || r.Status == 504:
		return "the app behind it didn't answer (" + http.StatusText(r.Status) + ")"
	case r.Status >= 500:
		return "server error " + strconv.Itoa(r.Status)
	}
	return ""
}

// Target is a site to check.
type Target struct {
	URL   string
	Route bool
}

// Targets lists the sites to check: every public hostname of the tunnels
// running here, and the extra addresses from the settings.
func Targets(tunnels []priv.Tunnel, extra []string) []Target {
	seen := map[string]bool{}
	var out []Target
	add := func(u string, route bool) {
		if !seen[u] {
			seen[u] = true
			out = append(out, Target{URL: u, Route: route})
		}
	}
	for _, t := range tunnels {
		if !t.Running {
			continue // the tunnel's own check covers it
		}
		for _, r := range t.Routes {
			if strings.ContainsAny(r.Host, "*") {
				continue // a wildcard has no one address to visit
			}
			add("https://"+r.Host+r.Path, true)
		}
	}
	for _, e := range extra {
		if u, err := url.Parse(strings.TrimSpace(e)); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			add(u.String(), false)
		}
	}
	return out
}

var cache = struct {
	sync.Mutex
	results map[string]Result
}{results: map[string]Result{}}

// Check visits each target, reusing results younger than Every.
func Check(ctx context.Context, targets []Target) []Result {
	out := make([]Result, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, t := range targets {
		cache.Lock()
		r, ok := cache.results[t.URL]
		cache.Unlock()
		if ok && time.Since(r.At) < Every {
			r.Route = t.Route
			out[i] = r
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := visit(ctx, t.URL)
			r.Route = t.Route
			if ctx.Err() == nil {
				cache.Lock()
				cache.results[t.URL] = r
				cache.Unlock()
			}
			out[i] = r
		}()
	}
	wg.Wait()
	sort.SliceStable(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

// Forget drops the reused results, so the next check visits again.
func Forget() {
	cache.Lock()
	cache.results = map[string]Result{}
	cache.Unlock()
}

var client = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return nil
	},
}

func visit(ctx context.Context, raw string) Result {
	r := Result{URL: raw, At: time.Now()}
	u, err := url.Parse(raw)
	if err != nil {
		r.Err = "not an address"
		return r
	}
	r.Host = u.Hostname()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		r.Err = "not an address"
		return r
	}
	req.Header.Set("User-Agent", "opsie-site-check")
	start := time.Now()
	resp, err := client.Do(req)
	r.Took = time.Since(start)
	if err != nil {
		r.Err = reason(err)
		return r
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	r.Status = resp.StatusCode
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		r.CertExpires = resp.TLS.PeerCertificates[0].NotAfter
	}
	return r
}

// reason turns a request error into a few words.
func reason(err error) string {
	var dns *net.DNSError
	var cert *tls.CertificateVerificationError
	var timeout interface{ Timeout() bool }
	switch {
	case errors.As(err, &dns):
		if dns.IsNotFound {
			return "the name doesn't exist in DNS"
		}
		return "DNS didn't answer"
	case errors.As(err, &cert):
		return "its certificate isn't valid"
	case errors.As(err, &timeout) && timeout.Timeout():
		return "no answer in 15 seconds"
	case strings.Contains(err.Error(), "connection refused"):
		return "nothing answers on its port"
	case strings.Contains(err.Error(), "too many redirects"):
		return "it redirects in a loop"
	}
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	return msg
}
