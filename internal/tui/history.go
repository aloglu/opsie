package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/history"
	"github.com/aloglu/opsie/internal/human"
)

type histState struct {
	samples []history.Sample
	at      time.Time
	loading bool
	err     string
}

type histMsg struct {
	samples []history.Sample
	err     error
}

// loadHistory reads what the background checks recorded, at most once a
// minute.
func (m *Model) loadHistory() tea.Cmd {
	if m.hist.loading || (!m.hist.at.IsZero() && time.Since(m.hist.at) < time.Minute) {
		return nil
	}
	m.hist.loading = true
	dir := m.env.Paths.Service
	return func() tea.Msg {
		s, err := history.Read(dir, time.Now().Add(-7*24*time.Hour))
		return histMsg{samples: s, err: err}
	}
}

func (m *Model) histUpdated(msg histMsg) {
	m.hist = histState{samples: msg.samples, at: time.Now()}
	if msg.err != nil {
		m.hist.err = msg.err.Error()
	}
}

// historyKind says which chart a modal shows, or "".
func historyKind(key string) string {
	switch {
	case key == "result:load":
		return "cpu"
	case key == "result:memory":
		return "mem"
	case key == "result:swap":
		return "swap"
	case strings.HasPrefix(key, "result:disk:"):
		return "disk:" + strings.TrimPrefix(key, "result:disk:")
	case strings.HasPrefix(key, "vol:"):
		return "disk:" + strings.TrimPrefix(key, "vol:")
	case key == "history:net":
		return "net"
	case key == "history:io":
		return "io"
	}
	return ""
}

type chartLine struct {
	label string
	pick  func(history.Sample) (float64, bool)
	color func(v float64) colorT
	// rate values are bytes per second; the others are percentages.
	rate bool
}

func (m *Model) chartLines(kind string) []chartLine {
	t := m.th
	th := m.env.Config.Thresholds
	pct := func(base colorT, warn, crit float64) func(float64) colorT {
		return func(v float64) colorT { return t.level(base, v, warn, crit) }
	}
	switch {
	case kind == "cpu":
		return []chartLine{{label: "CPU", pick: func(s history.Sample) (float64, bool) { return s.CPU, true }, color: pct(t.cpu, 80, 95)}}
	case kind == "mem":
		return []chartLine{{label: "Memory", pick: func(s history.Sample) (float64, bool) { return s.Mem, true }, color: pct(t.mem, th.MemoryWarn, th.MemoryCrit)}}
	case kind == "swap":
		return []chartLine{{label: "Swap", pick: func(s history.Sample) (float64, bool) { return s.Swap, true }, color: pct(t.mem, th.SwapWarn, 101)}}
	case strings.HasPrefix(kind, "disk:"):
		mount := strings.TrimPrefix(kind, "disk:")
		return []chartLine{{label: "Disk " + mount, pick: func(s history.Sample) (float64, bool) {
			v, ok := s.Disks[mount]
			return v, ok
		}, color: pct(t.disk, th.DiskWarn, th.DiskCrit)}}
	case kind == "net":
		in, out := t.rx, t.tx
		return []chartLine{
			{label: "↓ In", pick: func(s history.Sample) (float64, bool) { return s.Rx, true }, color: func(float64) colorT { return in }, rate: true},
			{label: "↑ Out", pick: func(s history.Sample) (float64, bool) { return s.Tx, true }, color: func(float64) colorT { return out }, rate: true},
		}
	case kind == "io":
		// Samples from before this was recorded have nothing to show.
		side := func(i int) func(history.Sample) (float64, bool) {
			return func(s history.Sample) (float64, bool) {
				if len(s.IO) != 2 {
					return 0, false
				}
				return s.IO[i], true
			}
		}
		rd, wr := t.disk, t.wr
		return []chartLine{
			{label: "Read", pick: side(0), color: func(float64) colorT { return rd }, rate: true},
			{label: "Written", pick: side(1), color: func(float64) colorT { return wr }, rate: true},
		}
	}
	return nil
}

// historyCharts draws the last day and week of a reading.
func (m *Model) historyCharts(kind string, b *detailBuilder) {
	t := m.th
	lines := m.chartLines(kind)
	if len(lines) == 0 {
		return
	}
	h := m.hist
	switch {
	case h.at.IsZero() || h.loading:
		b.text("Reading the history…", t.dim)
		return
	case len(h.samples) == 0:
		note := "No history yet. The background checks record one reading a minute while they're on, and keep eight days."
		if m.env.Dev {
			note = "No history yet. In a development build it's recorded while `make daemon` runs."
		}
		b.text(note, t.subtle)
		return
	}
	now := m.now
	w := b.inner
	for _, span := range []struct {
		name string
		d    time.Duration
		axis string
	}{{"Last 24 hours", 24 * time.Hour, "24h ago"}, {"Last 7 days", 7 * 24 * time.Hour, "7 days ago"}} {
		from := now.Add(-span.d)
		b.para = true
		b.row(t.bold.Render(span.name))
		scale := ""
		for _, l := range lines {
			low, avg, high, n := history.Stats(h.samples, from, l.pick)
			if n == 0 {
				b.row(t.dim.Render(l.label + ": nothing recorded"))
				continue
			}
			var stats string
			base, top := 0.0, 100.0
			if l.rate {
				top = max(high, 1024)
				stats = fmt.Sprintf("%s · avg %s · high %s", l.label, human.Rate(avg), human.Rate(high))
			} else {
				base, top = zoom(low, high)
				stats = fmt.Sprintf("low %.0f%% · avg %.0f%% · high %.0f%%", low, avg, high)
				if base > 0 || top < 100 {
					scale = fmt.Sprintf(" · %.0f–%.0f%%", base, top)
				}
			}
			b.row(t.subtle.Render(fit(stats, w)))
			buckets := history.Buckets(h.samples, from, now, w, l.pick)
			values := make([]float64, len(buckets))
			for i, bk := range buckets {
				if bk.N > 0 {
					values[i] = max(bk.Avg-base, 0.01)
				}
			}
			color := func(v float64) colorT { return l.color(v + base) }
			for _, row := range t.chart(values, w, 3, top-base, color) {
				b.body = append(b.body, row)
			}
		}
		left := span.axis + scale
		pad := max(w-lipgloss.Width(left)-3, 1)
		b.body = append(b.body, t.dim.Render(left+strings.Repeat(" ", pad)+"now"))
	}
}

func (m *Model) netHistoryDetail(b *detailBuilder) detail {
	t := m.th
	n := m.snap.Net
	if n.Ready {
		b.kv("Now", "↓ "+human.Rate(n.RxRate)+"  ↑ "+human.Rate(n.TxRate))
		b.kv("Since boot", "↓ "+human.Bytes(n.RxTotal)+"  ↑ "+human.Bytes(n.TxTotal))
	}
	m.historyCharts("net", b)
	return detail{title: "Network", headline: headline(t, health.Info, "traffic", strings.Join(n.Interfaces, " ")), body: b.body}
}

func (m *Model) ioHistoryDetail(b *detailBuilder) detail {
	t := m.th
	io := m.snap.IO
	if io.Ready {
		b.kv("Now", "read "+human.Rate(io.ReadRate)+" · written "+human.Rate(io.WriteRate))
		b.kv("Busy", fmt.Sprintf("%.0f%% of the time", io.Busy))
	}
	b.kv("Since boot", "read "+human.Bytes(io.ReadTotal)+" · written "+human.Bytes(io.WriteTotal))
	b.text("Busy is how much of the time the busiest disk had work waiting. Near 100% for long, the disk is what everything else is waiting for.", t.dim)
	m.historyCharts("io", b)
	return detail{title: "Disk activity", headline: headline(t, health.Info, "activity", strings.Join(io.Devices, " ")), body: b.body}
}

// zoom picks the range a percentage chart shows: all of 0–100 when the
// values move a lot, or a closer band around them when they don't, so a
// disk creeping from 70% to 80% visibly climbs.
func zoom(low, high float64) (base, top float64) {
	if high-low >= 40 {
		return 0, 100
	}
	base = math.Max(0, math.Floor((low-5)/10)*10)
	top = math.Min(100, math.Ceil((high+5)/10)*10)
	if top-base < 20 {
		top = math.Min(100, base+20)
		base = top - 20
	}
	return base, top
}
