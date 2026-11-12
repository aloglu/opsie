// Package speedtest measures the server's internet connection against
// Cloudflare's speed test (speed.cloudflare.com): how long a round trip
// takes, and how fast it downloads and uploads.
package speedtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	base = "https://speed.cloudflare.com"
	// Each direction runs for this long on this many connections, or
	// until it has moved its cap of data: a fast line would otherwise
	// move gigabytes.
	streams  = 4
	duration = 8 * time.Second
	warmup   = time.Second
	downCap  = 500 << 20
	upCap    = 250 << 20
	downPart = 50 << 20
	upPart   = 10 << 20
	// keep is how many results are remembered.
	keep = 20
)

// Result is one finished test.
type Result struct {
	At time.Time `json:"at"`
	// Down and Up are in bits per second.
	Down float64 `json:"down"`
	Up   float64 `json:"up"`
	// Ping is the usual round trip; Jitter is how much it varies.
	Ping   time.Duration `json:"ping"`
	Jitter time.Duration `json:"jitter"`
	// Server is the Cloudflare data center that answered, like "FRA".
	Server string `json:"server,omitempty"`
	// Moved is how much data the test used.
	Moved uint64 `json:"moved"`
}

// Test is a test in progress. Its progress can be read while Run runs.
type Test struct {
	phase atomic.Value // string
	moved atomic.Uint64
	since atomic.Int64 // when the phase's counting began, in ns
	// URL is the server to test against; empty is Cloudflare's.
	URL string
}

// Progress is what the test is doing now, "ping", "download" or "upload",
// and the speed so far in bits per second.
func (t *Test) Progress() (phase string, bps float64) {
	phase, _ = t.phase.Load().(string)
	since := t.since.Load()
	if since == 0 {
		return phase, 0
	}
	if secs := time.Since(time.Unix(0, since)).Seconds(); secs > 0.2 {
		bps = float64(t.moved.Load()) * 8 / secs
	}
	return phase, bps
}

func (t *Test) url(path string) string {
	if t.URL != "" {
		return t.URL + path
	}
	return base + path
}

// Run does the whole test. It takes about twenty seconds.
func (t *Test) Run(ctx context.Context) (Result, error) {
	client := &http.Client{Transport: &http.Transport{
		MaxConnsPerHost:     streams * 2,
		MaxIdleConnsPerHost: streams * 2,
		ForceAttemptHTTP2:   false,
		Proxy:               http.ProxyFromEnvironment,
	}}
	defer client.CloseIdleConnections()
	res := Result{At: time.Now()}

	t.phase.Store("ping")
	res.Server = t.server(ctx, client)
	ping, jitter, err := t.ping(ctx, client)
	if err != nil {
		return res, fmt.Errorf("couldn't reach the speed test: %w", err)
	}
	res.Ping, res.Jitter = ping, jitter

	t.phase.Store("download")
	down, moved, err := t.measure(ctx, downCap, func(ctx context.Context, count func(int)) error {
		req, _ := http.NewRequestWithContext(ctx, "GET", t.url(fmt.Sprintf("/__down?bytes=%d", downPart)), nil)
		req.Header.Set("User-Agent", "opsie")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("the speed test answered %s", resp.Status)
		}
		buf := make([]byte, 64<<10)
		for {
			n, err := resp.Body.Read(buf)
			count(n)
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
		}
	})
	if err != nil {
		return res, fmt.Errorf("download: %w", err)
	}
	res.Down, res.Moved = down, moved

	t.phase.Store("upload")
	up, moved, err := t.measure(ctx, upCap, func(ctx context.Context, count func(int)) error {
		req, _ := http.NewRequestWithContext(ctx, "POST", t.url("/__up"), &zeros{left: upPart, count: count})
		req.Header.Set("User-Agent", "opsie")
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = upPart
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != 200 {
			return fmt.Errorf("the speed test answered %s", resp.Status)
		}
		return nil
	})
	if err != nil {
		return res, fmt.Errorf("upload: %w", err)
	}
	res.Up = up
	res.Moved += moved
	t.phase.Store("")
	t.since.Store(0)
	return res, nil
}

// server asks which data center is answering; "" when it won't say.
func (t *Test) server(ctx context.Context, client *http.Client) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", t.url("/cdn-cgi/trace"), nil)
	req.Header.Set("User-Agent", "opsie")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	for _, line := range strings.Split(string(body), "\n") {
		if v, ok := strings.CutPrefix(line, "colo="); ok && len(v) <= 8 {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ping times a few empty requests on one connection. The first opens the
// connection, so it isn't counted.
func (t *Test) ping(ctx context.Context, client *http.Client) (ping, jitter time.Duration, err error) {
	var times []time.Duration
	for i := 0; i < 9; i++ {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		req, _ := http.NewRequestWithContext(rctx, "GET", t.url("/__down?bytes=0"), nil)
		req.Header.Set("User-Agent", "opsie")
		start := time.Now()
		resp, rerr := client.Do(req)
		if rerr == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				rerr = fmt.Errorf("it answered %s", resp.Status)
			}
		}
		took := time.Since(start)
		cancel()
		if rerr != nil {
			err = rerr
			continue
		}
		if i > 0 {
			times = append(times, took)
		}
	}
	if len(times) == 0 {
		if err == nil {
			err = errors.New("no answer")
		}
		return 0, 0, err
	}
	var diff time.Duration
	for i := 1; i < len(times); i++ {
		d := times[i] - times[i-1]
		if d < 0 {
			d = -d
		}
		diff += d
	}
	if len(times) > 1 {
		jitter = diff / time.Duration(len(times)-1)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times[len(times)/2], jitter, nil
}

// measure runs one transfer after another on each stream until the time
// or the data is up, and returns the speed in bits per second, not
// counting the first second while the connections get up to speed.
func (t *Test) measure(ctx context.Context, limit uint64, one func(ctx context.Context, count func(int)) error) (bps float64, moved uint64, err error) {
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	var total atomic.Uint64
	t.moved.Store(0)
	start := time.Now()
	t.since.Store(start.UnixNano())
	var warm atomic.Uint64 // what had moved when the warm-up ended
	timer := time.AfterFunc(warmup, func() { warm.Store(total.Load()) })
	defer timer.Stop()
	count := func(n int) {
		if n <= 0 {
			return
		}
		t.moved.Add(uint64(n))
		if total.Add(uint64(n)) >= limit {
			cancel()
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				if e := one(ctx, count); e != nil && ctx.Err() == nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = e
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	moved = total.Load()
	if moved == 0 {
		if firstErr == nil {
			firstErr = errors.New("nothing got through")
		}
		return 0, 0, firstErr
	}
	if elapsed > 2*warmup && warm.Load() > 0 && warm.Load() < moved {
		return float64(moved-warm.Load()) * 8 / (elapsed - warmup).Seconds(), moved, nil
	}
	return float64(moved) * 8 / elapsed.Seconds(), moved, nil
}

// zeros is an upload body of left zero bytes.
type zeros struct {
	left  int
	count func(int)
}

func (z *zeros) Read(p []byte) (int, error) {
	if z.left <= 0 {
		return 0, io.EOF
	}
	n := min(len(p), z.left)
	clear(p[:n])
	z.left -= n
	z.count(n)
	return n, nil
}

// Load reads the remembered results, newest first.
func Load(path string) []Result {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var list []Result
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	return list
}

// Save remembers a result along with the earlier ones.
func Save(path string, r Result) ([]Result, error) {
	list := append([]Result{r}, Load(path)...)
	if len(list) > keep {
		list = list[:keep]
	}
	data, err := json.Marshal(list)
	if err != nil {
		return list, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return list, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return list, err
	}
	return list, os.Rename(tmp, path)
}
