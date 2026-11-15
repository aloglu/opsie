package tui

import (
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/NimbleMarkets/ntcharts/v2/canvas"
	"github.com/NimbleMarkets/ntcharts/v2/canvas/graph"
	"github.com/NimbleMarkets/ntcharts/v2/canvas/runes"
	"github.com/NimbleMarkets/ntcharts/v2/linechart/streamlinechart"
	"github.com/NimbleMarkets/ntcharts/v2/sparkline"
)

// perColumn is how many values a style draws in one column of a chart.
func perColumn(style string) int {
	if style == "dense" {
		return 2
	}
	return 1
}

// chartStyle is how the settings say a reading's chart is drawn.
func (m *Model) chartStyle(reading string) string {
	return m.env.Config.Charts.For(reading)
}

// plot draws the latest values as a chart w wide and h tall, scaled to
// top, in one of config.ChartStyles. Bars are opsie's own; the others are
// drawn by ntcharts and then colored here, a column at a time, so a
// reading that runs high still changes color where it does. A chart one
// row tall is always bars: nothing else reads at that height.
func (t theme) plot(style string, values []float64, w, h int, top float64, colorOf func(v float64) color.Color) []string {
	if style == "bars" || style == "" || h < 2 || w <= 0 {
		return t.chart(values, w, h, top, colorOf)
	}
	if top <= 0 {
		top = 1
	}
	per := perColumn(style)
	if len(values) > w*per {
		values = values[len(values)-w*per:]
	}
	vals := make([]float64, len(values))
	for i, v := range values {
		vals[i] = min(max(v, 0), top)
	}
	// Each column's color comes from the highest value drawn in it.
	// have is false for the columns there's nothing to draw in yet.
	peak, have := make([]float64, w), make([]bool, w)
	first := w*per - len(vals)
	for i, v := range vals {
		col := (first + i) / per
		peak[col], have[col] = max(peak[col], v), true
	}

	// cell is what the chart has at a column and row, or 0 for nothing.
	var cell func(x, y int) rune
	rest := '▁'
	switch style {
	case "braille":
		// Two dots across a column and four up a row. Each value is at
		// the left dot of its column and joined to the next; the top dot
		// of the chart is left empty, so a peak never runs into what's
		// drawn on the line above.
		dotsW, dotsH := 2*w, 4*h
		grid := graph.NewBrailleGrid(w, h, 0, float64(dotsW-1), 0, float64(dotsH-1))
		at := func(i int) canvas.Point {
			x := 2 * (w - len(vals) + i)
			return grid.GridPoint(canvas.Float64Point{X: float64(x), Y: math.Round(vals[i] / top * float64(dotsH-2))})
		}
		for i := range vals {
			from := at(i)
			to := from
			if i+1 < len(vals) {
				to = at(i + 1)
			} else {
				// The latest value fills its column.
				to.X++
			}
			for _, p := range graph.GetLinePoints(from, to) {
				grid.Set(p)
			}
		}
		dots := grid.BraillePatterns()
		cell, rest = func(x, y int) rune { return dots[y][x] }, '⣀'
	case "line":
		ls := runes.ThinLineStyle
		if t.look.tl == lookRound.tl {
			ls = runes.ArcLineStyle
		}
		sl := streamlinechart.New(w, h, streamlinechart.WithXYSteps(0, 0), streamlinechart.WithYRange(0, top), streamlinechart.WithStyles(ls, lipgloss.NewStyle()))
		for _, v := range vals {
			sl.Push(v)
		}
		sl.Draw()
		cell = func(x, y int) rune { return sl.Canvas.Cell(canvas.Point{X: x, Y: y}).Rune }
	default:
		// Two values to a column and two steps to a row; the top step is
		// left empty, and anything above nothing shows as one step.
		full := top * float64(2*h) / float64(2*h-1)
		least := full / float64(2*h) * 0.51
		for i, v := range vals {
			if v > 0 {
				vals[i] = max(v, least)
			}
		}
		sp := sparkline.New(w, h, sparkline.WithNoAutoMaxValue(), sparkline.WithMaxValue(full))
		sp.PushAll(vals)
		sp.DrawQuadrantsColumnsOnly()
		cell = func(x, y int) rune { return sp.Canvas.Cell(canvas.Point{X: x, Y: y}).Rune }
	}

	rows := make([]string, h)
	for y := 0; y < h; y++ {
		var b strings.Builder
		var run []rune
		var runColor color.Color
		flush := func() {
			if len(run) > 0 {
				b.WriteString(lipgloss.NewStyle().Foreground(runColor).Render(string(run)))
				run = run[:0]
			}
		}
		for x := 0; x < w; x++ {
			r := cell(x, y)
			if r == 0 || r == '⠀' {
				r = ' '
			}
			c := colorOf(peak[x])
			if r == ' ' && y == h-1 && (!have[x] || style == "dense") {
				// The same faint floor the bars stand on.
				r, c = rest, t.track
			}
			if !have[x] && r != rest {
				r = ' '
			}
			if !sameColor(c, runColor) {
				flush()
				runColor = c
			}
			run = append(run, r)
		}
		flush()
		rows[y] = b.String()
	}
	return rows
}
