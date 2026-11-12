package priv

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tunnel is a Cloudflare Tunnel connector (cloudflared) running here,
// either as a container or as a systemd service.
type Tunnel struct {
	Kind    string    `json:"kind"` // "container" or "service"
	Name    string    `json:"name"` // container or unit name
	Running bool      `json:"running"`
	Since   time.Time `json:"since,omitzero"`

	TunnelID string `json:"tunnel_id,omitempty"`
	Version  string `json:"version,omitempty"`

	// Ready is how many connections to Cloudflare are up, from the
	// connector's own status page; HaveReady says whether it answered.
	Ready     int      `json:"ready"`
	HaveReady bool     `json:"have_ready"`
	Locations []string `json:"locations,omitempty"`
	Requests  uint64   `json:"requests"`
	Errors    uint64   `json:"errors"`
	// Statuses counts responses by HTTP status class ("2xx", "4xx", ...).
	Statuses map[string]uint64 `json:"statuses,omitempty"`

	// Routes are the public hostnames and where they lead, from the last
	// configuration Cloudflare sent. Remote is false when the tunnel is
	// configured with a local file, which opsie doesn't read.
	Routes   []Route   `json:"routes,omitempty"`
	RoutesAt time.Time `json:"routes_at,omitzero"`
	Remote   bool      `json:"remote"`

	// Origins are hostnames whose app couldn't be reached in the last day.
	Origins []OriginError `json:"origins,omitempty"`
	// Drops is how often a connection to Cloudflare broke in the last day.
	Drops    int       `json:"drops"`
	LastDrop time.Time `json:"last_drop,omitzero"`
}

// Route is one public hostname.
type Route struct {
	Host    string `json:"host"`
	Path    string `json:"path,omitempty"`
	Service string `json:"service"`
}

// Target is the host part of a route's service, like "blog" for
// http://blog:8080, or "" for services that aren't addresses.
func (r Route) Target() string {
	u, err := url.Parse(r.Service)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}

// OriginError is a hostname whose requests failed because cloudflared
// couldn't reach the app behind it.
type OriginError struct {
	Host   string    `json:"host"`
	Count  int       `json:"count"`  // in the last day
	Recent int       `json:"recent"` // in the last hour
	Last   time.Time `json:"last"`
	Error  string    `json:"error"`
}

// TunnelLog is what a connector's log says about it.
type TunnelLog struct {
	TunnelID, Version string
	// Metrics is the address the status server listens on, as logged.
	Metrics  string
	Routes   []Route
	RoutesAt time.Time
	Remote   bool
	Origins  []OriginError
	Drops    int
	LastDrop time.Time
}

var (
	tunnelTimeRe = regexp.MustCompile(`^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ) (\w{3}) +(.*)$`)
	metricsRe    = regexp.MustCompile(`Starting metrics server on (\S+)/metrics`)
)

// logField reads key=value or key="quoted value" from a cloudflared log
// line.
func logField(line, key string) string {
	i := strings.Index(line, " "+key+"=")
	if i < 0 {
		return ""
	}
	rest := line[i+len(key)+2:]
	if strings.HasPrefix(rest, `"`) {
		q, err := strconv.QuotedPrefix(rest)
		if err != nil {
			return ""
		}
		v, _ := strconv.Unquote(q)
		return v
	}
	v, _, _ := strings.Cut(rest, " ")
	return v
}

// ParseTunnelLog reads cloudflared's log. Errors and drops count from
// since on; hour is where "recent" starts.
func ParseTunnelLog(r io.Reader, since, hour time.Time) TunnelLog {
	var l TunnelLog
	origins := map[string]*OriginError{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		m := tunnelTimeRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		at, _ := time.Parse(time.RFC3339, m[1])
		msg := " " + m[3]
		switch {
		case strings.Contains(msg, " Starting tunnel "):
			l.TunnelID = logField(msg, "tunnelID")
		case strings.HasPrefix(msg, " Version "):
			l.Version, _, _ = strings.Cut(strings.TrimPrefix(msg, " Version "), " ")
		case strings.Contains(msg, "Starting metrics server"):
			if mm := metricsRe.FindStringSubmatch(msg); mm != nil {
				l.Metrics = mm[1]
			}
		case strings.Contains(msg, " Updated to new configuration "):
			if routes, ok := parseIngress(logField(msg, "config")); ok {
				l.Routes, l.RoutesAt, l.Remote = routes, at, true
			}
		case at.Before(since):
		case strings.Contains(msg, " Request failed ") && strings.Contains(msg, "origin service"):
			host := ""
			if u, err := url.Parse(logField(msg, "dest")); err == nil {
				host = u.Hostname()
			}
			if host == "" {
				continue
			}
			o := origins[host]
			if o == nil {
				o = &OriginError{Host: host}
				origins[host] = o
			}
			o.Count++
			if !at.Before(hour) {
				o.Recent++
			}
			o.Last = at
			o.Error = originReason(logField(msg, "error"))
		case strings.Contains(msg, " Connection terminated") || strings.Contains(msg, " Unregistered tunnel connection"):
			l.Drops++
			l.LastDrop = at
		}
	}
	for _, o := range origins {
		l.Origins = append(l.Origins, *o)
	}
	sort.Slice(l.Origins, func(i, j int) bool { return l.Origins[i].Last.After(l.Origins[j].Last) })
	return l
}

// originReason shortens cloudflared's error to the part that says what
// went wrong.
func originReason(e string) string {
	_, after, found := strings.Cut(e, "from cloudflared: ")
	if found {
		e = after
	}
	switch {
	case strings.Contains(e, "no such host"), strings.Contains(e, "server misbehaving"):
		return "the app's name doesn't resolve, so it's probably not running"
	case strings.Contains(e, "connection refused"):
		return "nothing is listening on the app's port"
	case strings.Contains(e, "i/o timeout"), strings.Contains(e, "deadline exceeded"):
		return "the app didn't answer in time"
	}
	return e
}

func parseIngress(config string) ([]Route, bool) {
	var c struct {
		Ingress []struct {
			Hostname string `json:"hostname"`
			Path     string `json:"path"`
			Service  string `json:"service"`
		} `json:"ingress"`
	}
	if json.Unmarshal([]byte(config), &c) != nil {
		return nil, false
	}
	routes := []Route{}
	for _, r := range c.Ingress {
		if r.Hostname != "" {
			routes = append(routes, Route{Host: r.Hostname, Path: r.Path, Service: r.Service})
		}
	}
	return routes, true
}

// TunnelMetrics is what the connector's /metrics page says.
type TunnelMetrics struct {
	Connections int
	Requests    uint64
	Errors      uint64
	Statuses    map[string]uint64
	Locations   []string
}

var locationRe = regexp.MustCompile(`edge_location="([^"]+)"`)
var statusRe = regexp.MustCompile(`status_code="(\d)\d\d"`)

// ParseTunnelMetrics reads Prometheus text from cloudflared.
func ParseTunnelMetrics(text string) TunnelMetrics {
	m := TunnelMetrics{Statuses: map[string]uint64{}}
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		if i < 0 {
			continue
		}
		name, value := line[:i], line[i+1:]
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			continue
		}
		switch {
		case name == "cloudflared_tunnel_ha_connections":
			m.Connections = int(v)
		case name == "cloudflared_tunnel_request_errors":
			m.Errors = uint64(v)
		case strings.HasPrefix(name, "cloudflared_tunnel_response_by_code{"):
			if s := statusRe.FindStringSubmatch(name); s != nil {
				m.Statuses[s[1]+"xx"] += uint64(v)
				m.Requests += uint64(v)
			}
		case strings.HasPrefix(name, "cloudflared_tunnel_server_locations{") && v == 1:
			if l := locationRe.FindStringSubmatch(name); l != nil && !seen[l[1]] {
				seen[l[1]] = true
				m.Locations = append(m.Locations, l[1])
			}
		}
	}
	sort.Strings(m.Locations)
	return m
}

// --- reading it ---

// tunnelCache keeps what the full log said, which only changes when
// Cloudflare sends a new configuration, so the whole log isn't read on
// every check.
var tunnelCache = struct {
	sync.Mutex
	key string
	at  time.Time
	log TunnelLog
}{}

const tunnelFullEvery = 10 * time.Minute

func opTunnel(ctx context.Context, _ map[string]string) (any, error) {
	out := []Tunnel{}
	if _, err := lookPath("docker"); err == nil {
		list, err := output(ctx, "docker", "ps", "-a", "--no-trunc", "--format", "{{.Names}}|{{.Image}}|{{.ID}}")
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(list), "\n") {
				f := strings.Split(line, "|")
				if len(f) == 3 && strings.Contains(f[1], "cloudflared") {
					out = append(out, containerTunnel(ctx, f[0], f[2]))
				}
			}
		}
	}
	units, err := output(ctx, "systemctl", "list-units", "--all", "--plain", "--no-legend", "--type=service", "cloudflared*")
	if err == nil {
		for _, line := range strings.Split(units, "\n") {
			if f := strings.Fields(line); len(f) > 0 && ValidUnit(f[0]) && strings.HasPrefix(f[0], "cloudflared") {
				out = append(out, unitTunnel(ctx, f[0]))
			}
		}
	}
	return out, nil
}

func containerTunnel(ctx context.Context, name, id string) Tunnel {
	t := Tunnel{Kind: "container", Name: name}
	var info []struct {
		State struct {
			Running   bool
			StartedAt time.Time
		}
		HostConfig      struct{ NetworkMode string }
		NetworkSettings struct {
			Networks map[string]struct{ IPAddress string }
		}
	}
	if raw, err := outputBytes(ctx, "docker", "inspect", "--type", "container", id); err == nil {
		_ = json.Unmarshal(raw, &info)
	}
	var ips []string
	host := false
	if len(info) == 1 {
		t.Running, t.Since = info[0].State.Running, info[0].State.StartedAt
		host = info[0].HostConfig.NetworkMode == "host"
		for _, n := range info[0].NetworkSettings.Networks {
			if n.IPAddress != "" {
				ips = append(ips, n.IPAddress)
			}
		}
		sort.Strings(ips)
	}
	logs := func(args ...string) io.Reader {
		path, err := lookPath("docker")
		if err != nil {
			return strings.NewReader("")
		}
		raw, _ := execCombined(ctx, path, append([]string{"logs"}, append(args, "--", id)...)...)
		return strings.NewReader(raw)
	}
	l := tunnelLog(id+t.Since.String(),
		func() io.Reader { return logs("--tail", "20000") },
		func() io.Reader { return logs("--since", "24h") })
	fillTunnel(&t, l)
	if t.Running {
		addrs := metricsAddrs(l.Metrics, ips, host)
		readStatus(ctx, &t, addrs)
	}
	return t
}

func unitTunnel(ctx context.Context, unit string) Tunnel {
	t := Tunnel{Kind: "service", Name: unit}
	show, _ := output(ctx, "systemctl", "show", unit, "--timestamp=unix", "-p", "ActiveState", "-p", "ActiveEnterTimestamp")
	for _, line := range strings.Split(show, "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "ActiveState":
			t.Running = v == "active"
		case "ActiveEnterTimestamp":
			if n, err := strconv.ParseInt(strings.TrimPrefix(v, "@"), 10, 64); err == nil && n > 0 {
				t.Since = time.Unix(n, 0)
			}
		}
	}
	journal := func(args ...string) io.Reader {
		raw, _ := output(ctx, "journalctl", append([]string{"--unit", unit, "--output", "cat", "--no-pager", "--quiet"}, args...)...)
		return strings.NewReader(raw)
	}
	l := tunnelLog(unit+t.Since.String(),
		func() io.Reader { return journal("--lines", "20000") },
		func() io.Reader { return journal("--since", "-24h") })
	fillTunnel(&t, l)
	if t.Running {
		readStatus(ctx, &t, metricsAddrs(l.Metrics, nil, true))
	}
	return t
}

func execCombined(ctx context.Context, path string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// tunnelLog combines the full log (read now and then) with the last day's
// (read every time).
func tunnelLog(key string, full, day func() io.Reader) TunnelLog {
	now := time.Now()
	since, hour := now.Add(-24*time.Hour), now.Add(-time.Hour)
	tunnelCache.Lock()
	cached := tunnelCache.key == key && time.Since(tunnelCache.at) < tunnelFullEvery
	base := tunnelCache.log
	tunnelCache.Unlock()
	if !cached {
		base = ParseTunnelLog(full(), since, hour)
		tunnelCache.Lock()
		tunnelCache.key, tunnelCache.at, tunnelCache.log = key, now, base
		tunnelCache.Unlock()
		return base
	}
	recent := ParseTunnelLog(day(), since, hour)
	base.Origins, base.Drops, base.LastDrop = recent.Origins, recent.Drops, recent.LastDrop
	if recent.Remote {
		base.Routes, base.RoutesAt, base.Remote = recent.Routes, recent.RoutesAt, true
	}
	if recent.Metrics != "" {
		base.Metrics = recent.Metrics
	}
	return base
}

func fillTunnel(t *Tunnel, l TunnelLog) {
	t.TunnelID, t.Version = l.TunnelID, l.Version
	t.Routes, t.RoutesAt, t.Remote = l.Routes, l.RoutesAt, l.Remote
	t.Origins, t.Drops, t.LastDrop = l.Origins, l.Drops, l.LastDrop
}

// metricsAddrs is where to look for the connector's status server. A
// container's server is reached on the container's own address.
func metricsAddrs(logged string, ips []string, local bool) []string {
	port := ""
	if logged != "" {
		if _, p, err := net.SplitHostPort(logged); err == nil {
			port = p
		}
	}
	ports := []string{port}
	if port == "" {
		ports = []string{"20241", "20242", "20243", "20244", "20245"}
	}
	hosts := ips
	if local {
		hosts = append([]string{"127.0.0.1"}, hosts...)
	}
	var out []string
	for _, h := range hosts {
		for _, p := range ports {
			out = append(out, net.JoinHostPort(h, p))
		}
	}
	return out
}

func readStatus(ctx context.Context, t *Tunnel, addrs []string) {
	client := &http.Client{Timeout: 2 * time.Second}
	get := func(u string) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return string(body), err
	}
	for _, a := range addrs {
		body, err := get("http://" + a + "/ready")
		if err != nil {
			continue
		}
		var r struct {
			ReadyConnections int `json:"readyConnections"`
		}
		if json.Unmarshal([]byte(body), &r) != nil {
			continue
		}
		t.Ready, t.HaveReady = r.ReadyConnections, true
		if text, err := get("http://" + a + "/metrics"); err == nil {
			m := ParseTunnelMetrics(text)
			t.Locations, t.Requests, t.Errors = m.Locations, m.Requests, m.Errors
			if len(m.Statuses) > 0 {
				t.Statuses = m.Statuses
			}
		}
		return
	}
}
