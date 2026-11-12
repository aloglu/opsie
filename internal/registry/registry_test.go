package registry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aloglu/opsie/internal/priv"
)

func TestParse(t *testing.T) {
	for in, want := range map[string]Ref{
		"nginx":                          {"registry-1.docker.io", "library/nginx", "latest"},
		"couchdb:3.5.3":                  {"registry-1.docker.io", "library/couchdb", "3.5.3"},
		"cloudflare/cloudflared:latest":  {"registry-1.docker.io", "cloudflare/cloudflared", "latest"},
		"docker.io/me/app":               {"registry-1.docker.io", "me/app", "latest"},
		"ghcr.io/someone/wiki:1.0.0-b.8": {"ghcr.io", "someone/wiki", "1.0.0-b.8"},
		"registry.example.com:5000/a/b":  {"registry.example.com:5000", "a/b", "latest"},
	} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
	for _, local := range []string{"shop:local", "local/game-server:initial", "localhost:5000/x", "nginx@sha256:abc"} {
		if _, err := Parse(local); !errors.Is(err, ErrLocal) {
			t.Errorf("%s should be local: %v", local, err)
		}
	}
}

func TestChecksFollowTheTokenDance(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") != "repository:me/app:pull" {
				t.Errorf("scope %q", r.URL.Query().Get("scope"))
			}
			w.Write([]byte(`{"token":"t0k"}`))
		case r.Header.Get("Authorization") != "Bearer t0k":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="test",scope="repository:me/app:pull"`)
			w.WriteHeader(401)
		case r.Method != http.MethodHead || !strings.Contains(r.Header.Get("Accept"), "image.index"):
			t.Errorf("%s %s", r.Method, r.Header.Get("Accept"))
		case strings.Contains(r.URL.Path, "/missing/"):
			w.WriteHeader(404)
		default:
			w.Header().Set("Docker-Content-Digest", "sha256:new")
		}
	}))
	defer srv.Close()
	old := client
	client = srv.Client()
	defer func() { client = old }()
	host := strings.TrimPrefix(srv.URL, "https://")
	oldLocal := localHost
	localHost = func(string) bool { return false }
	defer func() { localHost = oldLocal }()

	got := Checks(context.Background(), []priv.ImageInfo{
		{Container: "a", Ref: host + "/me/app:latest", Digests: []string{host + "/me/app@sha256:old"}},
		{Container: "b", Ref: host + "/me/app:2", Digests: []string{host + "/me/app@sha256:new"}},
		{Container: "c", Ref: host + "/missing/app:1", Digests: []string{"x@sha256:1"}},
		{Container: "d", Ref: host + "/me/app:3"}, // no digests: built here
	})
	by := map[string]Check{}
	for _, c := range got {
		by[c.Container] = c
	}
	if len(got) != 2 || !by["a"].Newer || by["b"].Newer || by["a"].Remote != "sha256:new" {
		t.Errorf("%+v", got)
	}
}
