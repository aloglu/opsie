package tui

import (
	"fmt"
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/health"
)

// theme holds every color and style, and the shapes things are drawn
// with. Plain text has no color of its own, so it's the terminal's, and
// opsie never paints the background.
//
// Every part of the screen has a color of its own, so a color always
// means one thing: accent is the selection, nav is what you can press
// (tabs, keys, links), and each reading has its chart color.
type theme struct {
	name string
	look look

	text, muted, faint, border color.Color
	accent, accent2, nav       color.Color
	cpu, mem, disk, rx, tx     color.Color
	// wr is what's written to the disks; what's read is in disk's color.
	wr                            color.Color
	ok, info, warn, danger, track color.Color

	bold, subtle, dim, accentText lipgloss.Style
	// navText is a link or an action; tab is the tab you're on.
	navText, tab                lipgloss.Style
	okText, infoText, warnText  lipgloss.Style
	dangerText, borderText, key lipgloss.Style
	button, buttonFocus         lipgloss.Style
	buttonDanger                lipgloss.Style
	// selected is the band across the selected row; backdrop is the page
	// behind a popup.
	selected, backdrop lipgloss.Style
}

// look is the shapes a theme draws with: its borders, bars and charts.
type look struct {
	// The corners and sides of a card.
	tl, tr, bl, br, h, v string
	// fill and rest are the two halves of a bar.
	fill, rest string
	// bars are a chart column's heights, from empty to full.
	bars []rune
}

var (
	blockBars = []rune(" ▁▂▃▄▅▆▇█")
	dotBars   = []rune(" ⣀⣤⣶⣿")

	lookRound  = look{"╭", "╮", "╰", "╯", "─", "│", "━", "─", blockBars}
	lookSquare = look{"┌", "┐", "└", "┘", "─", "│", "█", "░", blockBars}
	lookHeavy  = look{"┏", "┓", "┗", "┛", "━", "┃", "█", "▁", blockBars}
	lookDashed = look{"╭", "╮", "╰", "╯", "┄", "┆", "━", "┄", dotBars}
	lookSoft   = look{"╭", "╮", "╰", "╯", "─", "│", "▄", "▁", blockBars}
)

// ground is what the colors are drawn on: a dark terminal, a light one, or
// one opsie couldn't ask.
type ground int

const (
	groundUnknown ground = iota
	groundDark
	groundLight
)

// tone is how colors are mixed for a ground: how light, how strong, what
// text goes on top of them, and how light the greys are. On a dark
// terminal colors are bright and full; on a light one they're deep. When
// the ground isn't known they sit in the middle, where they read on both.
//
// Colors are as strong as a screen can show at their lightness, up to c:
// lighter than this and they wash out into pastels, since a screen can
// only make a color lighter by adding white. sun is how much lighter the
// yellows and greens are than the blues and reds, as they are in the
// rainbow; without it a yellow is a brown.
type tone struct {
	l, c, sun    float64
	ink          string
	grey, border float64
}

var tones = map[ground]tone{
	groundDark:    {l: 0.70, c: 0.20, sun: 0.07, ink: "#101010", grey: 0.68, border: 0.46},
	groundLight:   {l: 0.53, c: 0.19, sun: 0, ink: "#ffffff", grey: 0.50, border: 0.72},
	groundUnknown: {l: 0.61, c: 0.17, sun: 0, ink: "#111111", grey: 0.60, border: 0.60},
}

// palette is a theme's colors, each a hue on the OKLCH color wheel; they
// become colors at the tone of the ground. plain is no hue: the
// terminal's own text color.
type palette struct {
	// accent is the selection and, blended into accent2, the wordmark.
	accent, accent2 float64
	// nav is the tabs, key hints and links.
	nav float64
	// The charts and bars, one hue per reading.
	cpu, mem, disk, rx, tx float64
	// wr is what's written to the disks; what's read is in disk's hue.
	wr                     float64
	ok, info, warn, danger float64
	// tint is the hue of the greys, and vivid how strong the colors are
	// next to the other themes'.
	tint, vivid float64
	look        look
}

const plain = -1

// roles are the hues that each mean something different, and so have to
// look different within a theme. accent2 isn't one: it only ever shows as
// the far end of the wordmark.
func (p palette) roles() map[string]float64 {
	return map[string]float64{
		"accent": p.accent, "nav": p.nav,
		"cpu": p.cpu, "mem": p.mem, "disk": p.disk, "rx": p.rx, "tx": p.tx,
		"wr": p.wr,
		"ok": p.ok, "info": p.info, "warn": p.warn, "danger": p.danger,
	}
}

// A theme's accent gives it its color (teal, orange, green, blue, pink or
// none); the other hues are spread around the wheel from there, as far
// from each other as they'll go, and the chart hues keep well clear of the
// warning ones they turn into. Its look gives it its shape.
var palettes = map[string]palette{
	// teal to violet, cool slate greys, rounded
	"opsie": {accent: 190, accent2: 295, nav: 295, cpu: 330, mem: 268, disk: 120, rx: 50, tx: 0, wr: 312,
		ok: 150, info: 240, warn: 82, danger: 25, tint: 240, vivid: 1, look: lookRound},
	// Steph Ango's Flexoki (stephango.com/flexoki): its eight inks on warm
	// paper greys, a little quieter than the rest, square
	"flexoki": {accent: 50, accent2: 350, nav: 350, cpu: 190, mem: 295, disk: 222, rx: 322, tx: 152, wr: 274,
		ok: 122, info: 255, warn: 88, danger: 25, tint: 80, vivid: 0.9, look: lookSquare},
	// green to olive, lichen greys, soft bars
	"moss": {accent: 138, accent2: 105, nav: 325, cpu: 290, mem: 350, disk: 225, rx: 55, tx: 108, wr: 158,
		ok: 185, info: 255, warn: 80, danger: 28, tint: 125, vivid: 0.95, look: lookSoft},
	// blue to cyan, blue-grey, heavy lines
	"ocean": {accent: 250, accent2: 205, nav: 205, cpu: 330, mem: 125, disk: 278, rx: 50, tx: 355, wr: 144,
		ok: 160, info: 300, warn: 82, danger: 22, tint: 250, vivid: 1.05, look: lookHeavy},
	// pink to violet, mauve greys, dashed lines and dotted charts, the
	// strongest colors
	"dusk": {accent: 345, accent2: 290, nav: 290, cpu: 130, mem: 318, disk: 215, rx: 45, tx: 105, wr: 2,
		ok: 170, info: 250, warn: 76, danger: 20, tint: 320, vivid: 1.15, look: lookDashed},
	// no color but the warnings: everything else is the terminal's own text
	"mono": {accent: plain, accent2: plain, nav: plain, cpu: plain, mem: plain, disk: plain, rx: plain, tx: plain, wr: plain,
		ok: 150, info: 245, warn: 80, danger: 25, tint: plain, vivid: 0.45, look: lookSquare},
}

func newTheme() theme { return themeNamed("opsie", groundUnknown) }

// themeNamed builds one of config.Themes for a ground; unknown names get
// the default.
func themeNamed(name string, g ground) theme {
	p, ok := palettes[name]
	if !ok {
		name, p = "opsie", palettes["opsie"]
	}
	tn := tones[g]
	col := func(h float64) color.Color {
		if h == plain {
			return lipgloss.NoColor{}
		}
		sun := tn.sun * max(math.Cos((h-105)*math.Pi/180), 0)
		return lipgloss.Color(oklch(tn.l+sun, tn.c*p.vivid, h))
	}
	grey := func(l float64) color.Color {
		if p.tint == plain {
			return lipgloss.Color(oklch(l, 0, 0))
		}
		return lipgloss.Color(oklch(l, 0.022, p.tint))
	}
	t := theme{
		name:    name,
		look:    p.look,
		text:    lipgloss.NoColor{},
		muted:   grey(tn.grey),
		faint:   grey(tn.grey),
		border:  grey(tn.border),
		track:   grey(tn.border),
		accent:  col(p.accent),
		accent2: col(p.accent2),
		nav:     col(p.nav),
		cpu:     col(p.cpu),
		mem:     col(p.mem),
		disk:    col(p.disk),
		rx:      col(p.rx),
		tx:      col(p.tx),
		wr:      col(p.wr),
		ok:      col(p.ok),
		info:    col(p.info),
		warn:    col(p.warn),
		danger:  col(p.danger),
	}
	if p.accent == plain {
		// Out and written are the readings that can't be told from their
		// pair by place, so they're grey next to the other's plain text.
		t.accent2, t.tx, t.wr = t.muted, t.muted, t.muted
	}
	fg := func(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }
	t.bold = lipgloss.NewStyle().Bold(true)
	t.subtle = fg(t.muted)
	t.dim = fg(t.faint)
	t.accentText = fg(t.accent)
	t.okText = fg(t.ok)
	t.infoText = fg(t.info)
	t.warnText = fg(t.warn)
	t.dangerText = fg(t.danger)
	t.borderText = fg(t.border)
	t.navText = fg(t.nav)
	t.tab = fg(t.nav).Bold(true).Underline(true)
	t.key = fg(t.nav).Bold(true)
	t.backdrop = fg(t.border).Faint(true)
	on := lipgloss.Color(tn.ink)
	t.button = lipgloss.NewStyle().Foreground(on).Background(t.muted).Padding(0, 1)
	t.buttonDanger = lipgloss.NewStyle().Foreground(on).Background(t.danger).Bold(true).Padding(0, 1)
	if p.accent == plain {
		// The terminal's own colors, swapped.
		t.buttonFocus = lipgloss.NewStyle().Reverse(true).Bold(true).Padding(0, 1)
		t.selected = lipgloss.NewStyle().Reverse(true)
	} else {
		t.buttonFocus = lipgloss.NewStyle().Foreground(on).Background(t.accent).Bold(true).Padding(0, 1)
		t.selected = lipgloss.NewStyle().Foreground(on).Background(t.accent)
	}
	return t
}

// oklch is the sRGB color, as #rrggbb, of a lightness (0–1), a chroma
// (strength, about 0–0.3) and a hue in degrees. Colors too strong for a
// screen are weakened until they fit, keeping their lightness and hue.
func oklch(l, c, h float64) string {
	rad := h * math.Pi / 180
	for ; ; c -= 0.004 {
		if c < 0 {
			c = 0
		}
		a, b := c*math.Cos(rad), c*math.Sin(rad)
		l_ := l + 0.3963377774*a + 0.2158037573*b
		m_ := l - 0.1055613458*a - 0.0638541728*b
		s_ := l - 0.0894841775*a - 1.2914855480*b
		l3, m3, s3 := l_*l_*l_, m_*m_*m_, s_*s_*s_
		rgb := [3]float64{
			4.0767416621*l3 - 3.3077115913*m3 + 0.2309699292*s3,
			-1.2684380046*l3 + 2.6097574011*m3 - 0.3413193965*s3,
			-0.0041960863*l3 - 0.7034186147*m3 + 1.7076147010*s3,
		}
		fits := true
		for _, v := range rgb {
			if v < -0.0005 || v > 1.0005 {
				fits = false
			}
		}
		if !fits && c > 0 {
			continue
		}
		var out [3]int
		for i, v := range rgb {
			v = min(max(v, 0), 1)
			if v <= 0.0031308 {
				v *= 12.92
			} else {
				v = 1.055*math.Pow(v, 1/2.4) - 0.055
			}
			out[i] = int(math.Round(v * 255))
		}
		return fmt.Sprintf("#%02x%02x%02x", out[0], out[1], out[2])
	}
}

// statusIcon is the mark in front of a checklist line. Each status has its
// own shape as well as color, so it reads without color too.
func (t theme) statusIcon(s health.Status) string {
	switch s {
	case health.Crit:
		return t.dangerText.Bold(true).Render("✗")
	case health.Warn:
		return t.warnText.Bold(true).Render("▲")
	case health.Unknown:
		return t.dim.Render("?")
	case health.Info:
		return t.infoText.Render("●")
	}
	return t.okText.Render("✓")
}

func (t theme) statusColor(s health.Status) color.Color {
	switch s {
	case health.Crit:
		return t.danger
	case health.Warn:
		return t.warn
	case health.Info:
		return t.info
	case health.Unknown:
		return t.faint
	}
	return t.ok
}

// level is the color of a percentage: the reading's own, base, until it
// passes warn or crit.
func (t theme) level(base color.Color, pct, warn, crit float64) color.Color {
	switch {
	case pct >= crit:
		return t.danger
	case pct >= warn:
		return t.warn
	}
	return base
}

// gradient colors each character of s along accent → accent2.
func (t theme) gradient(s string, bold bool) string {
	if _, none := t.accent.(lipgloss.NoColor); none {
		if bold {
			return t.bold.Render(s)
		}
		return s
	}
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		frac := 0.0
		if len(runes) > 1 {
			frac = float64(i) / float64(len(runes)-1)
		}
		style := lipgloss.NewStyle().Foreground(blend(t.accent, t.accent2, frac))
		if bold {
			style = style.Bold(true)
		}
		if r == ' ' {
			b.WriteRune(r)
			continue
		}
		b.WriteString(style.Render(string(r)))
	}
	return b.String()
}

func blend(a, b color.Color, frac float64) color.Color {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	mix := func(x, y uint32) uint8 {
		return uint8((float64(x>>8)*(1-frac) + float64(y>>8)*frac) + 0.5)
	}
	return color.RGBA{R: mix(ar, br), G: mix(ag, bg), B: mix(ab, bb), A: 255}
}

// wordmark is the small name in the header.
func (t theme) wordmark() string { return t.gradient("opsie", true) }

var bigWordmark = []string{
	"█▀█ █▀█ █▀▀ █ █▀▀",
	"█▄█ █▀▀ ▄▄█ █ ██▄",
}

// bigWordmark is the two-line logo shown when installing and updating.
func (t theme) bigWordmark() []string {
	out := make([]string, len(bigWordmark))
	for i, line := range bigWordmark {
		out[i] = t.gradient(line, false)
	}
	return out
}
