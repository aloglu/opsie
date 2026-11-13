package tui

import (
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"

	"github.com/aloglu/opsie/internal/app"
	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/health"
)

// askGround asks the terminal whether its background is dark or light. It
// only asks a real terminal, since the question is typed into it.
func askGround() ground {
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		return groundUnknown
	}
	if lipgloss.HasDarkBackground(os.Stdin, os.Stdout) {
		return groundDark
	}
	return groundLight
}

// PrintThemes shows a sample of each theme, for picking one: a card in
// its shapes, with a bar and a chart in each reading's color.
func PrintThemes(env *app.Env) error {
	g := askGround()
	var b strings.Builder
	b.WriteString("\n")
	sample := []float64{12, 30, 22, 48, 70, 55, 90, 64, 38}
	for _, name := range config.Themes {
		cfg := env.Config
		cfg.Theme = name
		t := themeOn(cfg, g)
		badge := ""
		if name == env.Config.Theme {
			badge = t.okText.Render("in use")
		}
		paint := func(c colorT, s string) string { return lipgloss.NewStyle().Foreground(c).Render(s) }
		charts := ""
		for _, c := range []colorT{t.cpu, t.mem, t.disk, t.wr, t.rx, t.tx} {
			charts += t.chart(sample, 6, 1, 100, func(float64) colorT { return c })[0] + " "
		}
		body := []string{
			t.tab.Render("1 Tab") + "   " + t.dim.Render("2 ") + t.subtle.Render("Tab") + "   " + t.key.Render("⏎") + " " + t.subtle.Render("open") + "   " + t.navText.Render("Link ›"),
			"A selected row",
			t.statusIcon(health.OK) + " " + t.statusIcon(health.Info) + " " + t.statusIcon(health.Warn) + " " + t.statusIcon(health.Crit) + "  " + t.gauge(62, 8, t.disk) + "  " + paint(t.cpu, "cpu") + " " + paint(t.mem, "mem") + " " + paint(t.disk, "disk") + " " + paint(t.wr, "write") + " " + paint(t.rx, "in") + " " + paint(t.tx, "out"),
			charts,
			t.buttonFocus.Render("Restart") + " " + t.button.Render("Close") + " " + t.buttonDanger.Render("Remove"),
		}
		card := t.card(name, badge, body, 50, 0, 1, 2, name == env.Config.Theme)
		for _, line := range card.lines {
			b.WriteString("  " + line + "\n")
		}
	}
	b.WriteString("\n  " + "Pick one in " + tildePath(env.Paths.ConfigFile()) + ", or in the Settings tab:\n\n")
	b.WriteString("      " + themeOn(env.Config, g).key.Render(`theme = "flexoki"`) + "\n\n")
	_, err := lipgloss.Fprint(env.Out, b.String())
	return err
}
