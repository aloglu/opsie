package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestPendingIsNewestFirstAndSkipsDrafts(t *testing.T) {
	releases := []Release{
		{Tag: "v0.2.0"}, {Tag: "v0.4.0"}, {Tag: "v0.3.0", Draft: true},
		{Tag: "v0.5.0-rc1", Prerelease: true}, {Tag: "v0.1.0"}, {Tag: "v0.3.1"},
	}
	var got []string
	for _, r := range Pending("v0.2.0", releases) {
		got = append(got, r.Tag)
	}
	if want := []string{"v0.4.0", "v0.3.1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if len(Pending("dev", releases)) != 0 {
		t.Fatal("development builds have nothing pending")
	}
}

func TestHighlights(t *testing.T) {
	notes := "## What's new\n\n- **Docker** tab with `logs`\n* Faster startup\n-not a bullet\n- \n- Third\n- Fourth"
	got := Highlights(notes, 3)
	want := []string{"Docker tab with logs", "Faster startup", "Third"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func archive(t *testing.T, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct{ name, body string }{{"README.md", "readme"}, {"opsie", content}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(f.body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func serve(t *testing.T, data []byte, sum string) {
	t.Helper()
	name := AssetName(runtime.GOARCH)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/download/v1.0.0/checksums.txt":
			w.Write([]byte(sum + "  " + name + "\n"))
		case "/download/v1.0.0/" + name:
			w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := DownloadBase
	DownloadBase = srv.URL
	t.Cleanup(func() { DownloadBase = old })
}

func TestInstallReleaseReplacesBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("releases are Linux-only")
	}
	data := archive(t, "new binary")
	sum := sha256.Sum256(data)
	serve(t, data, hex.EncodeToString(sum[:]))
	target := filepath.Join(t.TempDir(), "opsie")
	os.WriteFile(target, []byte("old"), 0o755)
	if err := InstallRelease(context.Background(), "v1.0.0", target); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new binary" {
		t.Fatalf("target holds %q", got)
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", info.Mode())
	}
}

func TestInstallReleaseRejectsBadChecksum(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("releases are Linux-only")
	}
	serve(t, archive(t, "evil"), "0000000000000000000000000000000000000000000000000000000000000000")
	target := filepath.Join(t.TempDir(), "opsie")
	os.WriteFile(target, []byte("old"), 0o755)
	if err := InstallRelease(context.Background(), "v1.0.0", target); err == nil {
		t.Fatal("a checksum mismatch must fail")
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatal("the old binary must be left alone")
	}
}

func TestDailyRemembersForADay(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"tag_name":"v0.3.0"}`))
	}))
	defer srv.Close()
	old := APIBase
	APIBase = srv.URL
	defer func() { APIBase = old }()

	path := filepath.Join(t.TempDir(), "meta.json")
	for i := 0; i < 3; i++ {
		latest, newer := Daily(context.Background(), path, "v0.2.0")
		if latest != "v0.3.0" || !newer {
			t.Fatalf("got %s %v", latest, newer)
		}
	}
	if calls != 1 {
		t.Fatalf("asked GitHub %d times, want once", calls)
	}
	if _, newer := Cached(path, "v0.3.0"); newer {
		t.Fatal("v0.3.0 isn't newer than itself")
	}
}
