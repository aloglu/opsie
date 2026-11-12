package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"github.com/aloglu/opsie/internal/app"
	"github.com/aloglu/opsie/internal/daemon"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/metrics"
	"github.com/aloglu/opsie/internal/priv"
)

// motdIssues is how many problems the login summary lists by name.
const motdIssues = 3

// PrintMotd prints the short summary for logging in: how the server is,
// and what needs attention if anything does. It reads what the background
// service found, so it's quick; without the service it only looks at
// load, memory and disk.
func PrintMotd(env *app.Env) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	snap := metrics.NewCollector().Collect(ctx)
	st, err := daemon.ReadStatus(env.Paths.Service)
	width := 80
	if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
		width = w
	}
	text := motdText(themeFor(env.Config), env, snap, st, err == nil, priv.ServiceInstalled(), time.Now(), width)
	_, err = lipgloss.Fprint(env.Out, text)
	return err
}

func motdText(t theme, env *app.Env, snap metrics.Snapshot, st daemon.Status, haveStatus, service bool, now time.Time, width int) string {
	width = max(width, 20)
	fresh := haveStatus && st.Fresh(now)

	var issues []daemon.Issue
	if fresh {
		issues = st.Issues
	} else {
		// Only what can be read quickly, without admin access.
		for _, r := range health.Evaluate(health.Readings{Snap: snap, HaveSnap: true, Thresholds: env.Config.Thresholds}) {
			if r.Status >= health.Warn {
				issues = append(issues, daemon.Issue{Status: r.Status, Title: r.Title, Summary: r.Summary})
			}
		}
	}
	worst := health.OK
	for _, i := range issues {
		worst = max(worst, i.Status)
	}
	state := t.okText.Render("✓ all good")
	need := human.Plural(len(issues), "thing needs", "things need") + " attention"
	if width < 50 {
		need = human.Plural(len(issues), "issue", "issues")
	}
	switch {
	case worst == health.Crit:
		state = t.statusIcon(health.Crit) + " " + t.dangerText.Bold(true).Render(need)
	case worst == health.Warn:
		state = t.statusIcon(health.Warn) + " " + t.warnText.Bold(true).Render(need)
	case !fresh:
		state = t.okText.Render("✓ looks fine")
	}

	// The first line has what fits, most important first, shown in a
	// fixed order.
	type part struct {
		order int
		text  string
	}
	parts := []part{{0, t.wordmark()}, {2, state}}
	if snap.Host.Hostname != "" {
		parts = append(parts, part{1, t.bold.Render(snap.Host.Hostname)})
	}
	if d := rootDisk(snap); d != nil {
		parts = append(parts, part{6, t.subtle.Render(fmt.Sprintf("disk %.0f%%", d.Percent))})
	}
	if snap.Mem.Total > 0 {
		parts = append(parts, part{5, t.subtle.Render(fmt.Sprintf("mem %.0f%%", snap.Mem.Percent))})
	}
	if snap.Host.Uptime > 0 {
		parts = append(parts, part{3, t.subtle.Render("up " + human.Duration(snap.Host.Uptime))})
	}
	parts = append(parts, part{4, t.subtle.Render(fmt.Sprintf("load %.2f", snap.Load[0]))})
	sep := t.dim.Render(" · ")
	for len(parts) > 1 {
		total := 0
		for i, p := range parts {
			total += lipgloss.Width(p.text)
			if i > 0 {
				total += 3
			}
		}
		if total <= width {
			break
		}
		parts = parts[:len(parts)-1]
	}
	ordered := make([]string, 7)
	for _, p := range parts {
		ordered[p.order] = p.text
	}
	var shown []string
	for _, s := range ordered {
		if s != "" {
			shown = append(shown, s)
		}
	}
	lines := []string{strings.Join(shown, sep)}

	for i, is := range issues {
		if i == motdIssues {
			lines = append(lines, t.dim.Render(fmt.Sprintf("… and %d more", len(issues)-motdIssues)))
			break
		}
		line := t.statusIcon(is.Status) + " " + is.Title
		if is.Summary != "" {
			line += t.subtle.Render(" · " + is.Summary)
		}
		lines = append(lines, line)
	}
	switch {
	case !service:
		lines = append(lines, t.dim.Render("Background checks are off; turn them on in opsie for the full picture."))
	case haveStatus && !fresh:
		lines = append(lines, t.warnText.Render("The background checks last ran "+human.Duration(now.Sub(st.Checked))+" ago; they may have stopped."))
	case len(issues) > 0:
		lines = append(lines, t.dim.Render("Type ")+t.key.Render("opsie")+t.dim.Render(" to see more."))
	}
	var out []string
	for i, l := range lines {
		if i == 0 {
			out = append(out, ansi.Truncate(l, width, "…"))
			continue
		}
		for j, w := range wrap(l, width-4) {
			indent := "  "
			if j > 0 {
				indent = "    "
			}
			out = append(out, indent+w)
		}
	}
	return strings.Join(out, "\n") + "\n"
}

func rootDisk(s metrics.Snapshot) *metrics.Disk {
	for i := range s.Disks {
		if s.Disks[i].Mount == "/" {
			return &s.Disks[i]
		}
	}
	if len(s.Disks) > 0 {
		return &s.Disks[0]
	}
	return nil
}
