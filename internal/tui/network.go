package tui

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/speedtest"
)

// The Network tab: traffic, a speed test and the interfaces.

type iface struct {
	name   string
	up     bool
	addrs  []string
	rx, tx uint64
	// virtual interfaces belong to containers and bridges.
	virtual bool
}

type netState struct {
	ifaces []iface
	// results are the speed tests so far, newest first.
	results []speedtest.Result
	loaded  bool
	// test is the one running now.
	test    *speedtest.Test
	started time.Time
	err     string
}

type (
	netMsg struct {
		ifaces  []iface
		results []speedtest.Result
	}
	speedMsg struct {
		results []speedtest.Result
		err     error
	}
)

func (m *Model) speedFile() string { return filepath.Join(m.env.Paths.State, "speedtest.json") }

// networkPoll reads the interfaces and the remembered speed tests.
func (m *Model) networkPoll() tea.Cmd {
	path := m.speedFile()
	return func() tea.Msg {
		return netMsg{ifaces: readIfaces(), results: speedtest.Load(path)}
	}
}

func (m *Model) netUpdated(msg netMsg) {
	m.net.ifaces, m.net.loaded = msg.ifaces, true
	if m.net.test == nil {
		m.net.results = msg.results
	}
}

func readIfaces() []iface {
	list, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []iface
	for _, in := range list {
		if in.Flags&net.FlagLoopback != 0 {
			continue
		}
		f := iface{name: in.Name, up: in.Flags&net.FlagUp != 0 && in.Flags&net.FlagRunning != 0}
		for _, prefix := range []string{"veth", "br-", "docker", "virbr", "cni", "flannel", "cali"} {
			if strings.HasPrefix(in.Name, prefix) {
				f.virtual = true
			}
		}
		addrs, _ := in.Addrs()
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil || ip.IsLinkLocalUnicast() {
				continue
			}
			f.addrs = append(f.addrs, ip.String())
		}
		stat := func(name string) uint64 {
			data, _ := os.ReadFile(filepath.Join("/sys/class/net", in.Name, "statistics", name))
			n, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
			return n
		}
		f.rx, f.tx = stat("rx_bytes"), stat("tx_bytes")
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return !out[i].virtual && out[j].virtual })
	return out
}

// runSpeedTest starts a test; the page shows its progress as it goes.
func (m *Model) runSpeedTest() tea.Cmd {
	if m.net.test != nil {
		return nil
	}
	test := &speedtest.Test{}
	m.net.test, m.net.started, m.net.err = test, time.Now(), ""
	m.modal = nil
	path := m.speedFile()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		res, err := test.Run(ctx)
		if err != nil {
			return speedMsg{results: speedtest.Load(path), err: err}
		}
		list, _ := speedtest.Save(path, res)
		return speedMsg{results: list}
	}
}

func (m *Model) speedDone(msg speedMsg) tea.Cmd {
	m.net.test, m.net.results = nil, msg.results
	if msg.err != nil {
		m.net.err = msg.err.Error()
		return m.setFlash("Speed test: "+msg.err.Error(), flashError, 6*time.Second)
	}
	if len(msg.results) > 0 {
		r := msg.results[0]
		return m.setFlash(fmt.Sprintf("↓ %s  ↑ %s  %s", bitRate(r.Down), bitRate(r.Up), human.Seconds(r.Ping)), flashOK, 6*time.Second)
	}
	return nil
}

// bitRate writes a speed the way connections are sold: in bits.
func bitRate(bps float64) string {
	switch {
	case bps >= 1e9:
		return fmt.Sprintf("%.2f Gbps", bps/1e9)
	case bps >= 1e8:
		return fmt.Sprintf("%.0f Mbps", bps/1e6)
	case bps >= 1e6:
		return fmt.Sprintf("%.1f Mbps", bps/1e6)
	}
	return fmt.Sprintf("%.0f kbps", bps/1e3)
}

func ms(d time.Duration) string {
	if d < 10*time.Millisecond {
		return fmt.Sprintf("%.1f ms", float64(d)/float64(time.Millisecond))
	}
	return fmt.Sprintf("%.0f ms", float64(d)/float64(time.Millisecond))
}

func (m *Model) networkSections(inner int) []section {
	out := []section{m.trafficSection(inner), m.speedSection(inner), m.ifaceSection(inner)}
	// How the server is reached from outside belongs with the network,
	// whether or not the tunnel happens to run in a container.
	out = append(out, m.tunnelSections(inner)...)
	if extra := m.extraSites(); len(extra) > 0 {
		out = append(out, m.sitesSection(extra, inner))
	}
	return out
}

func (m *Model) trafficSection(inner int) section {
	// The charts get more room here than on the Overview.
	grow := m.grow
	m.grow = 4
	card := m.netCard(inner)
	m.grow = grow
	return section{title: "Traffic", badge: card.badge, entries: []entry{{id: "history:net", lines: card.body, block: true}}}
}

func (m *Model) speedSection(inner int) section {
	t := m.th
	s := section{title: "Speed test"}
	paint := func(c colorT, text string) string { return lipgloss.NewStyle().Foreground(c).Bold(true).Render(text) }
	if test := m.net.test; test != nil {
		phase, bps := test.Progress()
		s.badge = t.infoText.Render("running")
		line := "Starting…"
		switch phase {
		case "ping":
			line = "Timing the round trip…"
		case "download":
			line = paint(t.rx, "↓") + " Downloading  " + t.bold.Render(bitRate(bps))
		case "upload":
			line = paint(t.tx, "↑") + " Uploading  " + t.bold.Render(bitRate(bps))
		}
		// How far along, of the twenty seconds or so it takes.
		pct := min(m.now.Sub(m.net.started).Seconds()/20*100, 100)
		s.intro = []string{fit(line, inner), t.gauge(pct, inner, t.info)}
		return s
	}
	if m.net.err != "" {
		for _, line := range wrap("The last one didn't finish: "+m.net.err, inner) {
			s.intro = append(s.intro, t.warnText.Render(line))
		}
	}
	if len(m.net.results) > 0 {
		r := m.net.results[0]
		s.badge = t.subtle.Render(human.Duration(m.now.Sub(r.At)) + " ago")
		lines := m.itemRow(paint(t.rx, "↓")+" Download", t.bold.Render(bitRate(r.Down)), inner)
		lines = append(lines, m.itemRow(paint(t.tx, "↑")+" Upload", t.bold.Render(bitRate(r.Up)), inner)...)
		about := ms(r.Ping) + t.dim.Render(" · varies by "+ms(r.Jitter))
		lines = append(lines, m.itemRow(t.subtle.Render("  Round trip"), about, inner)...)
		// Running another is in its popup.
		s.entries = append(s.entries, entry{id: "speedtest", lines: lines, block: true})
		return s
	}
	if m.net.loaded {
		s.intro = append(s.intro, wrap("How fast this server's connection is, measured against Cloudflare.", inner)...)
	}
	s.entries = append(s.entries, entry{id: "do:speedtest", lines: m.action("Run a speed test", inner)})
	return s
}

func (m *Model) ifaceSection(inner int) section {
	t := m.th
	s := section{title: "Interfaces"}
	if !m.net.loaded {
		s.intro = []string{t.dim.Render("Reading…")}
		return s
	}
	virtual := 0
	for _, f := range m.net.ifaces {
		if f.virtual {
			virtual++
			continue
		}
		state := t.okText.Render("up")
		if !f.up {
			state = t.subtle.Render("down")
		}
		s.intro = append(s.intro, m.itemRow(t.bold.Render(f.name), state, inner)...)
		for _, a := range f.addrs {
			s.intro = append(s.intro, "  "+fit(a, inner-2))
		}
		s.intro = append(s.intro, t.dim.Render(fit(fmt.Sprintf("  since boot ↓ %s  ↑ %s", human.Bytes(f.rx), human.Bytes(f.tx)), inner)))
	}
	if virtual > 0 {
		s.intro = append(s.intro, t.dim.Render(fit("and "+human.Plural(virtual, "more for containers", "more for containers"), inner)))
	}
	if len(s.intro) == 0 {
		s.intro = []string{t.dim.Render("None found")}
	}
	return s
}

func (m *Model) speedDetail(b *detailBuilder) detail {
	t := m.th
	plain := lipgloss.NewStyle()
	b.text("Measures this server's connection against the nearest Cloudflare data center: the round trip, then eight seconds of downloading and eight of uploading, on four connections each.", plain)
	b.text("A test moves real data: up to 500 MB down and 250 MB up on a fast line, less on a slow one. If your provider counts traffic, it counts this.", t.subtle)
	head := ""
	for i, r := range m.net.results {
		if i == 0 {
			head = headline(t, health.Info, "↓ "+bitRate(r.Down)+"  ↑ "+bitRate(r.Up), ms(r.Ping))
			b.para = true
		}
		if i == 8 {
			break
		}
		via := ""
		if r.Server != "" {
			via = t.dim.Render(" · " + r.Server)
		}
		b.row(t.subtle.Render(r.At.Local().Format("2 Jan 15:04")) + fmt.Sprintf("  ↓ %s  ↑ %s  %s", bitRate(r.Down), bitRate(r.Up), ms(r.Ping)) + via)
	}
	if len(m.net.results) > 0 {
		b.text("The letters are the airport nearest the data center that answered. This is the speed between here and there: what the server can do, not what a visitor at home gets.", t.dim)
	}
	return detail{title: "Speed test", headline: head, body: b.body, fixes: []health.Fix{{Label: "Run a speed test", Special: "speedtest:run"}}}
}
