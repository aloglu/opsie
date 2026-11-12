package speedtest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestRunAgainstALocalServer(t *testing.T) {
	var uploaded int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cdn-cgi/trace":
			io.WriteString(w, "fl=1\ncolo=TST\nloc=XX\n")
		case "/__down":
			n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
			buf := make([]byte, 32<<10)
			for n > 0 {
				k := min(n, len(buf))
				if _, err := w.Write(buf[:k]); err != nil {
					return
				}
				n -= k
			}
		case "/__up":
			n, _ := io.Copy(io.Discard, r.Body)
			uploaded += n
		}
	}))
	defer srv.Close()

	test := &Test{URL: srv.URL}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := test.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Server != "TST" || res.Ping <= 0 || res.Down <= 0 || res.Up <= 0 {
		t.Fatalf("result %+v", res)
	}
	// Both directions stop at their caps on a line this fast.
	if res.Moved < downCap || res.Moved > downCap+upCap+64<<20 {
		t.Errorf("moved %d MB, want about the caps", res.Moved>>20)
	}
	if phase, _ := test.Progress(); phase != "" {
		t.Errorf("phase %q after the test", phase)
	}

	path := filepath.Join(t.TempDir(), "state", "speedtest.json")
	for i := 0; i < keep+3; i++ {
		res.Ping = time.Duration(i)
		if _, err := Save(path, res); err != nil {
			t.Fatal(err)
		}
	}
	if list := Load(path); len(list) != keep || list[0].Ping != time.Duration(keep+2) {
		t.Fatalf("kept %d, newest %v", len(list), list[0].Ping)
	}
}
