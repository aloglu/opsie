package priv

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const tunnelLogSample = `2026-11-04T11:53:01Z INF Starting tunnel tunnelID=a3039a3f-329c-452f-a3ef-48afa025b95b
2026-11-04T11:53:01Z INF Version 2026.10.7 (Checksum c4f9)
2026-11-04T11:53:01Z INF Starting metrics server on [::]:20241/metrics
2026-11-07T04:57:11Z INF Updated to new configuration config="{\"ingress\":[{\"hostname\":\"pens.example.com\", \"service\":\"http://blog:8080\"}, {\"service\":\"http_status:404\"}], \"warp-routing\":{\"enabled\":false}}" version=2
2026-11-08T13:30:16Z INF Updated to new configuration config="{\"ingress\":[{\"hostname\":\"pens.example.com\", \"service\":\"http://blog:8080\"}, {\"hostname\":\"wiki.example.com\", \"path\":\"/app\", \"service\":\"http://wiki:3000\"}, {\"service\":\"http_status:404\"}], \"warp-routing\":{\"enabled\":false}}" version=6
2026-11-09T17:00:37Z ERR  error="Unable to reach the origin service. The service may be down or it may not be responding to traffic from cloudflared: dial tcp: lookup wiki on 127.0.0.11:53: server misbehaving" connIndex=0 event=1 ingressRule=3 originService=http://wiki:3000
2026-11-09T17:00:37Z ERR Request failed error="Unable to reach the origin service. The service may be down or it may not be responding to traffic from cloudflared: dial tcp: lookup wiki on 127.0.0.11:53: server misbehaving" connIndex=0 dest=https://wiki.example.com/ event=0 ip=198.41.192.37 type=http
2026-11-10T10:30:00Z ERR Request failed error="Unable to reach the origin service. The service may be down or it may not be responding to traffic from cloudflared: dial tcp 172.18.0.5:3000: connect: connection refused" connIndex=0 dest=https://wiki.example.com/login event=0 ip=198.41.192.37 type=http
2026-11-10T10:48:29Z ERR Connection terminated error="accept stream listener error: timeout: no recent network activity" connIndex=2
2026-11-10T10:48:30Z WRN Connection terminated error="datagram manager error" connIndex=1
2026-11-10T10:48:32Z INF Registered tunnel connection connIndex=1 location=fra14 protocol=quic
not a cloudflared line
`

func TestParseTunnelLog(t *testing.T) {
	since := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	hour := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	l := ParseTunnelLog(strings.NewReader(tunnelLogSample), since, hour)
	if l.TunnelID != "a3039a3f-329c-452f-a3ef-48afa025b95b" || l.Version != "2026.10.7" || l.Metrics != "[::]:20241" {
		t.Errorf("header %+v", l)
	}
	if !l.Remote || len(l.Routes) != 2 || l.Routes[1] != (Route{Host: "wiki.example.com", Path: "/app", Service: "http://wiki:3000"}) {
		t.Errorf("routes %+v", l.Routes)
	}
	if l.Routes[0].Target() != "blog" || (Route{Service: "http_status:404"}).Target() != "" {
		t.Error("targets")
	}
	if len(l.Origins) != 1 {
		t.Fatalf("origins %+v", l.Origins)
	}
	o := l.Origins[0]
	if o.Host != "wiki.example.com" || o.Count != 2 || o.Recent != 1 || o.Error != "nothing is listening on the app's port" {
		t.Errorf("origin %+v", o)
	}
	if l.Drops != 2 || !l.LastDrop.Equal(time.Date(2026, 11, 10, 10, 48, 30, 0, time.UTC)) {
		t.Errorf("drops %d %v", l.Drops, l.LastDrop)
	}
	// Errors before the window don't count, but the routes still do.
	late := ParseTunnelLog(strings.NewReader(tunnelLogSample), time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), hour)
	if len(late.Origins) != 0 || late.Drops != 0 || len(late.Routes) != 2 {
		t.Errorf("late window %+v", late)
	}
}

func TestOriginReason(t *testing.T) {
	if got := originReason("x from cloudflared: dial tcp: lookup wiki on 127.0.0.11:53: no such host"); !strings.Contains(got, "not running") {
		t.Error(got)
	}
	if got := originReason("something new"); got != "something new" {
		t.Error(got)
	}
}

func TestParseTunnelMetrics(t *testing.T) {
	m := ParseTunnelMetrics(`# HELP x
cloudflared_tunnel_ha_connections 4
cloudflared_tunnel_request_errors 93
cloudflared_tunnel_response_by_code{status_code="200"} 13373
cloudflared_tunnel_response_by_code{status_code="304"} 5064
cloudflared_tunnel_response_by_code{status_code="502"} 7
cloudflared_tunnel_server_locations{connection_id="0",edge_location="fra08"} 1
cloudflared_tunnel_server_locations{connection_id="0",edge_location="fra10"} 0
cloudflared_tunnel_server_locations{connection_id="1",edge_location="ams01"} 1
`)
	if m.Connections != 4 || m.Errors != 93 || m.Requests != 18444 || m.Statuses["5xx"] != 7 || m.Statuses["3xx"] != 5064 {
		t.Errorf("%+v", m)
	}
	if strings.Join(m.Locations, ",") != "ams01,fra08" {
		t.Errorf("locations %v", m.Locations)
	}
}

func TestMetricsAddrs(t *testing.T) {
	got := metricsAddrs("[::]:20241", []string{"172.18.0.2"}, false)
	if strings.Join(got, ",") != "172.18.0.2:20241" {
		t.Error(got)
	}
	if got := metricsAddrs("", nil, true); len(got) != 5 || got[0] != "127.0.0.1:20241" {
		t.Error(got)
	}
}

func TestAppDirStaysInHome(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	home, _ := filepath.EvalSymlinks(u.HomeDir)
	dir, err := os.MkdirTemp(home, ".opsie-test-")
	if err != nil {
		t.Skip("can't write in home:", err)
	}
	defer os.RemoveAll(dir)
	if _, err := appDir(dir, u.Username); err == nil || !strings.Contains(err.Error(), "no compose file") {
		t.Errorf("no compose: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o644)
	if got, err := appDir(dir, u.Username); err != nil || got != dir {
		t.Errorf("good dir: %q %v", got, err)
	}
	for _, bad := range []string{"", "relative/path", dir + "/../" + filepath.Base(dir), "/etc", "/tmp"} {
		if _, err := appDir(bad, u.Username); err == nil {
			t.Errorf("%q was allowed", bad)
		}
	}
	// A link from home to outside it doesn't count as inside.
	link := filepath.Join(dir, "out")
	if os.Symlink("/etc", link) == nil {
		if _, err := appDir(link, u.Username); err == nil {
			t.Error("symlink out of home was allowed")
		}
	}
}

func TestParseDu(t *testing.T) {
	out := "11796480000\t/var/lib\n4096\t/var/mail\n79118336\t/var/log\n12136005632\t/var\n"
	u := ParseDu(out, "/var")
	if u.Size != 12136005632 || len(u.Children) != 3 || u.Children[0].Path != "/var/lib" || u.Children[2].Path != "/var/mail" || u.Files != 12136005632-11796480000-79118336-4096 {
		t.Errorf("%+v", u)
	}
	var many strings.Builder
	for i := 0; i < 20; i++ {
		many.WriteString("100\t/x/d" + string(rune('a'+i)) + "\n")
	}
	many.WriteString("5000\t/x\n")
	if u := ParseDu(many.String(), "/x"); len(u.Children) != maxChildren || u.More != 8 || u.MoreSize != 800 {
		t.Errorf("many %+v", u)
	}
}

func TestParseAutoremove(t *testing.T) {
	out := "Reading package lists...\nThe following packages will be REMOVED:\n  libfoo1* linux-image-6.1.0-old*\nRemv libfoo1 [1.0]\nRemv linux-image-6.1.0-old [6.1.0]\nAfter this operation, 412 MB disk space will be freed.\n"
	pkgs, size := ParseAutoremove(out)
	if strings.Join(pkgs, " ") != "libfoo1 linux-image-6.1.0-old" || size != 412e6 {
		t.Errorf("%v %d", pkgs, size)
	}
	if pkgs, size := ParseAutoremove("0 upgraded, 0 newly installed, 0 to remove"); pkgs != nil || size != 0 {
		t.Error(pkgs, size)
	}
}
