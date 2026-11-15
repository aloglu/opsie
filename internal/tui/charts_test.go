package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aloglu/opsie/internal/config"
)

func TestChartStyles(t *testing.T) {
	th := themeNamed("opsie", groundDark)
	one := func(float64) colorT { return th.cpu }
	var busy []float64
	for i := 0; i < 200; i++ {
		busy = append(busy, float64(i%50)*2)
	}
	busy[len(busy)-3] = 100
	for _, style := range config.ChartStyles {
		for _, size := range [][2]int{{40, 4}, {12, 3}, {7, 2}, {30, 1}, {1, 3}} {
			w, h := size[0], size[1]
			for name, values := range map[string][]float64{"full": busy, "short": {0, 0.2, 40, 100}, "none": nil} {
				rows := th.plot(style, values, w, h, 100, one)
				if len(rows) != h {
					t.Fatalf("%s %dx%d %s: %d rows", style, w, h, name, len(rows))
				}
				for _, row := range rows {
					if got := lipgloss.Width(row); got != w {
						t.Errorf("%s %dx%d %s: a row is %d wide: %q", style, w, h, name, got, ansi.Strip(row))
					}
				}
				// A peak never fills the top of its cell, where it would
				// touch what's drawn above the chart.
				if strings.ContainsAny(ansi.Strip(rows[0]), "█▀▛▜▌▐▙▟⣿⡏⢹⠉⠁⠈") && h > 1 {
					t.Errorf("%s %dx%d %s: the top row is filled to its top: %q", style, w, h, name, ansi.Strip(rows[0]))
				}
				// Something that isn't nothing shows.
				if name == "short" && w >= 4 && strings.TrimSpace(strings.Trim(ansi.Strip(strings.Join(rows, "")), "▁⣀")) == "" {
					t.Errorf("%s %dx%d: nothing drawn", style, w, h)
				}
			}
		}
	}
	// A reading that runs high changes color where it does, in every style.
	hot := func(v float64) colorT {
		if v >= 80 {
			return th.danger
		}
		return th.cpu
	}
	for _, style := range config.ChartStyles {
		rows := strings.Join(th.plot(style, []float64{10, 10, 10, 10, 95, 95, 10, 10}, 8, 4, 100, hot), "\n")
		if !strings.Contains(rows, lipgloss.NewStyle().Foreground(th.danger).Render("x")[:12]) {
			t.Errorf("%s: no column took the color of a high reading", style)
		}
	}
}

func TestChartStyleSettings(t *testing.T) {
	c := config.Default().Charts
	if got := c.For("cpu"); got != "bars" {
		t.Errorf("default is %s", got)
	}
	c.Style, c.Network = "braille", "line"
	if c.For("cpu") != "braille" || c.For("network") != "line" || c.For("disk") != "braille" {
		t.Errorf("styles: %+v", c)
	}
	c.Style, c.Network = "nope", "nope"
	if c.For("network") != "bars" {
		t.Errorf("a style opsie doesn't have should be bars, got %s", c.For("network"))
	}
}
