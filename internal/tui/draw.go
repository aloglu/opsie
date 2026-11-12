package tui

import (
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type colorT = color.Color

// fit truncates or pads s to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// spread puts left and right at either end of w cells. When they don't
// both fit, right is dropped and ok is false.
func spread(left, right string, w int) (string, bool) {
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	if right == "" {
		return fit(left, w), true
	}
	if lw+1+rw > w {
		return fit(left, w), false
	}
	return left + strings.Repeat(" ", w-lw-rw) + right, true
}

// wrap word-wraps s to w cells.
func wrap(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	return strings.Split(ansi.Wrap(s, w, ""), "\n")
}

// block is a rendered rectangle of lines with the clickable regions inside
// it, in coordinates relative to its top-left corner.
type block struct {
	lines []string
	w     int
	hits  []hit
	// focus is the row range [top, bottom) to keep on screen when
	// scrolling; top is -1 when there's nothing to follow.
	focus [2]int
}

func newBlock(w int) block { return block{w: w, focus: [2]int{-1, -1}} }

func (b block) height() int { return len(b.lines) }

// vstack puts blocks below each other.
func vstack(w int, blocks ...block) block {
	out := newBlock(w)
	for _, b := range blocks {
		dy := len(out.lines)
		for _, line := range b.lines {
			out.lines = append(out.lines, fit(line, w))
		}
		for _, h := range b.hits {
			h.y += dy
			out.hits = append(out.hits, h)
		}
		if b.focus[0] >= 0 && out.focus[0] < 0 {
			out.focus = [2]int{b.focus[0] + dy, b.focus[1] + dy}
		}
	}
	return out
}

// hstack puts blocks side by side with gap columns between them, padding
// the shorter ones at the bottom.
func hstack(gap int, blocks ...block) block {
	total, height := 0, 0
	for i, b := range blocks {
		if i > 0 {
			total += gap
		}
		total += b.w
		height = max(height, b.height())
	}
	out := newBlock(total)
	out.lines = make([]string, height)
	x := 0
	for i, b := range blocks {
		if i > 0 {
			x += gap
		}
		for row := 0; row < height; row++ {
			if i > 0 {
				out.lines[row] += strings.Repeat(" ", gap)
			}
			line := ""
			if row < len(b.lines) {
				line = b.lines[row]
			}
			out.lines[row] += fit(line, b.w)
		}
		for _, h := range b.hits {
			h.x += x
			out.hits = append(out.hits, h)
		}
		if b.focus[0] >= 0 && out.focus[0] < 0 {
			out.focus = b.focus
		}
		x += b.w
	}
	return out
}

// split divides w into n columns with gap cells between, as evenly as
// possible.
func split(w, n, gap int) []int {
	avail := w - gap*(n-1)
	widths := make([]int, n)
	for i := range widths {
		widths[i] = avail / n
		if i < avail%n {
			widths[i]++
		}
	}
	return widths
}

// card draws a box w cells wide with title and badge in its top border.
// body lines are fitted to the inner width; rows selected up to selEnd get
// the selection band, from one border to the other, unless selected is
// -1. Rows are padded to minBody. A focused card is the selection itself:
// its border and title take the selection's color, and nothing inside it
// is covered.
func (t theme) card(title, badge string, body []string, w, minBody, selected, selEnd int, focused bool) block {
	return t.cardBand(title, badge, body, w, minBody, selected, selEnd, 0, focused)
}

// cardBand is card with a band that stops band cells into the row, when
// band isn't 0, so a bar drawn after it keeps its colors.
func (t theme) cardBand(title, badge string, body []string, w, minBody, selected, selEnd, band int, focused bool) block {
	inner := w - 4
	b := newBlock(w)
	if inner < 1 {
		return b
	}
	edge, name := t.borderText, t.bold
	if focused {
		edge, name = t.accentText, t.accentText.Bold(true)
	}

	// ╭─ Title ──────── badge ─╮
	titleText := " " + name.Render(title) + " "
	badgeText := ""
	if badge != "" {
		badgeText = " " + badge + " "
	}
	fill := w - 4 - lipgloss.Width(titleText) - lipgloss.Width(badgeText)
	if fill < 1 {
		badgeText = ""
		fill = w - 4 - lipgloss.Width(titleText)
	}
	if fill < 1 {
		titleText = fit(titleText, w-5)
		fill = 1
	}
	lk := t.look
	top := edge.Render(lk.tl+lk.h) + titleText + edge.Render(strings.Repeat(lk.h, fill)) + badgeText + edge.Render(lk.h+lk.tr)
	b.lines = append(b.lines, top)

	for len(body) < minBody {
		body = append(body, "")
	}
	for i, line := range body {
		line = fit(line, inner)
		row := " " + line + " "
		if i >= selected && i < max(selEnd, selected+1) && selected >= 0 {
			if band > 0 && band < inner {
				row = t.highlight(" "+ansi.Cut(line, 0, band)) + ansi.Cut(line, band, inner) + " "
			} else {
				row = t.highlight(row)
			}
		}
		b.lines = append(b.lines, edge.Render(lk.v)+row+edge.Render(lk.v))
	}
	b.lines = append(b.lines, edge.Render(lk.bl+strings.Repeat(lk.h, w-2)+lk.br))
	return b
}

var _ = blockBars

// chart draws the last w values as vertical bars h rows tall, scaled to
// top. colorOf picks each bar's color from its value. Columns without data
// show a faint baseline.
func (t theme) chart(values []float64, w, h int, top float64, colorOf func(v float64) color.Color) []string {
	if w <= 0 || h <= 0 {
		return nil
	}
	if len(values) > w {
		values = values[len(values)-w:]
	}
	offset := w - len(values)
	if top <= 0 {
		top = 1
	}
	bars := t.look.bars
	steps := len(bars) - 1
	floor := bars[1]
	// The tallest bar stops short of the top of its cell, so a peak never
	// runs into whatever is drawn on the line above.
	full := h*steps - max(steps/4, 1)
	rows := make([]string, h)
	for row := 0; row < h; row++ {
		level := h - 1 - row // 0 is the bottom row
		var b strings.Builder
		var run []rune
		var runColor color.Color
		flush := func() {
			if len(run) > 0 {
				b.WriteString(lipgloss.NewStyle().Foreground(runColor).Render(string(run)))
				run = run[:0]
			}
		}
		for col := 0; col < w; col++ {
			var r rune
			var c color.Color
			if col < offset {
				r, c = ' ', t.faint
				if level == 0 {
					r = floor
					c = t.track
				}
			} else {
				v := values[col-offset]
				parts := int(math.Round(math.Min(v/top, 1) * float64(full)))
				if v > 0 && parts == 0 {
					parts = 1
				}
				cell := min(max(parts-level*steps, 0), steps)
				r, c = bars[cell], colorOf(v)
				if cell == 0 && level == 0 {
					r, c = floor, t.track
				}
			}
			if !sameColor(c, runColor) {
				flush()
				runColor = c
			}
			run = append(run, r)
		}
		flush()
		rows[row] = b.String()
	}
	return rows
}

func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == b
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

// gauge draws a horizontal bar w cells wide filled to pct.
func (t theme) gauge(pct float64, w int, c color.Color) string {
	if w <= 0 {
		return ""
	}
	filled := int(math.Round(math.Min(math.Max(pct, 0), 100) / 100 * float64(w)))
	if pct > 0 && filled == 0 {
		filled = 1
	}
	return lipgloss.NewStyle().Foreground(c).Render(strings.Repeat(t.look.fill, filled)) +
		lipgloss.NewStyle().Foreground(t.track).Render(strings.Repeat(t.look.rest, w-filled))
}

// pill renders a button.
func (t theme) pill(label string, focused, danger bool) string {
	switch {
	case focused && danger:
		return t.buttonDanger.Render(label)
	case focused:
		return t.buttonFocus.Render(label)
	}
	return t.button.Render(label)
}

// highlight marks the selected row with a band of the accent color. The
// row's own colors are dropped, since they're as bright as the band; the
// status marks keep their shapes.
func (t theme) highlight(row string) string {
	return t.selected.Render(ansi.Strip(row))
}
