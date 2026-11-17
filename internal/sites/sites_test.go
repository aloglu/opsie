package sites

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aloglu/opsie/internal/priv"
)

func TestTargets(t *testing.T) {
	tunnels := []priv.Tunnel{
		{Running: true, Routes: []priv.Route{{Host: "a.example.com"}, {Host: "b.example.com", Path: "/app"}, {Host: "*.example.com"}}},
		{Running: false, Routes: []priv.Route{{Host: "stopped.example.com"}}},
	}
	got := Targets(tunnels, []string{"https://a.example.com", " https://other.example.org/x ", "ftp://nope", "not a url"})
	var urls []string
	for _, g := range got {
		urls = append(urls, g.URL)
	}
	if strings.Join(urls, " ") != "https://a.example.com https://b.example.com/app https://other.example.org/x" || !got[0].Route || got[2].Route {
		t.Errorf("%+v", got)
	}
}

func TestCheckClassifies(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "opsie-site-check" {
			t.Error("user agent", r.Header.Get("User-Agent"))
		}
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
	mux.HandleFunc("/broken", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) })
	mux.HandleFunc("/tunnel", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(530) })
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	Forget()
	targets := []Target{{URL: srv.URL + "/ok"}, {URL: srv.URL + "/login"}, {URL: srv.URL + "/broken"}, {URL: srv.URL + "/tunnel"}, {URL: srv.URL + "/loop"}}
	res := Check(context.Background(), targets, Options{Every: 2 * time.Minute, Slow: 5 * time.Second})
	by := map[string]Result{}
	for _, r := range res {
		by[r.URL[len(srv.URL):]] = r
	}
	if !by["/ok"].Up() || !by["/login"].Up() || by["/ok"].Problem() != "" {
		t.Errorf("ok/login %+v %+v", by["/ok"], by["/login"])
	}
	if by["/broken"].Up() || !strings.Contains(by["/broken"].Problem(), "didn't answer") {
		t.Errorf("broken %+v", by["/broken"])
	}
	if !strings.Contains(by["/tunnel"].Problem(), "530") {
		t.Errorf("tunnel %q", by["/tunnel"].Problem())
	}
	if by["/loop"].Up() || by["/loop"].Problem() != "it redirects in a loop" {
		t.Errorf("loop %q", by["/loop"].Problem())
	}
	// Results are reused for a while.
	srv.Close()
	again := Check(context.Background(), targets[:1], Options{Every: 2 * time.Minute, Slow: 5 * time.Second})
	if !again[0].Up() || time.Since(again[0].At) > time.Minute {
		t.Errorf("not reused: %+v", again[0])
	}
	Forget()
	if again := Check(context.Background(), targets[:1], Options{Every: 2 * time.Minute, Slow: 5 * time.Second}); again[0].Up() {
		t.Error("still up after Forget with the server gone")
	}
}

func TestBadRunIsTimed(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(502)
		}
	}))
	defer srv.Close()
	Forget()
	defer Forget()
	targets := []Target{{URL: srv.URL}}
	opt := Options{Every: 2 * time.Minute, Slow: 5 * time.Second}
	// age pretends the cached visit, and the run it's part of, happened
	// d ago.
	age := func(r Result, d time.Duration) {
		r.At = r.At.Add(-d)
		if r.Bad() {
			r.BadSince = r.BadSince.Add(-d)
		}
		cache.Lock()
		cache.results[srv.URL] = r
		cache.Unlock()
	}
	r := Check(context.Background(), targets, opt)[0]
	if r.Up() || !r.Bad() || r.BadFor() != 0 {
		t.Fatalf("first bad visit: %+v", r)
	}
	first := r.BadSince
	// A bad visit is reused only until Recheck, not Every.
	age(r, Recheck/2)
	if again := Check(context.Background(), targets, opt)[0]; !again.At.Equal(r.At.Add(-Recheck / 2)) {
		t.Fatalf("revisited before Recheck: %+v", again)
	}
	// The run is timed from its first visit, however far apart the visits
	// are, as with a long check interval.
	age(r, 5*time.Minute)
	r = Check(context.Background(), targets, opt)[0]
	if !r.BadSince.Equal(first.Add(-5*time.Minute)) || r.BadFor() < 5*time.Minute {
		t.Fatalf("second bad visit: %+v", r)
	}
	fail.Store(false)
	age(r, Recheck+time.Second)
	if r = Check(context.Background(), targets, opt)[0]; !r.Up() || r.Bad() || r.BadFor() != 0 {
		t.Fatalf("recovered: %+v", r)
	}
	// Slow answers count as bad too.
	Forget()
	if r = Check(context.Background(), targets, Options{Every: 2 * time.Minute, Slow: time.Nanosecond})[0]; !r.Bad() {
		t.Fatalf("slow: %+v", r)
	}
}

func TestVisitEvery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	Forget()
	defer Forget()
	targets := []Target{{URL: srv.URL}}
	opt := Options{Every: 10 * time.Minute, Slow: 5 * time.Second}
	first := Check(context.Background(), targets, opt)[0]
	cache.Lock()
	first.At = time.Now().Add(-5 * time.Minute)
	cache.results[srv.URL] = first
	cache.Unlock()
	if r := Check(context.Background(), targets, opt)[0]; !r.At.Equal(first.At) {
		t.Errorf("revisited before Every: %+v", r)
	}
	opt.Every = 2 * time.Minute
	if r := Check(context.Background(), targets, opt)[0]; r.At.Equal(first.At) {
		t.Errorf("not revisited after Every: %+v", r)
	}
}
