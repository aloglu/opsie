package sites

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
	res := Check(context.Background(), targets)
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
	again := Check(context.Background(), targets[:1])
	if !again[0].Up() || time.Since(again[0].At) > time.Minute {
		t.Errorf("not reused: %+v", again[0])
	}
	Forget()
	if again := Check(context.Background(), targets[:1]); again[0].Up() {
		t.Error("still up after Forget with the server gone")
	}
}
