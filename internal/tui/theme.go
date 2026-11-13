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
// A color means one thing: accent is the selection, nav is what you can
// press (tabs, keys, links), and each reading has its chart color. The
// readings that come in twos, in and out, read and written, are two
// shades of one color.
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

	lookRound  = look{"╭", "╮", "╰", "╯", "─", "│", "━", "─", blockBars}
	lookSquare = look{"┌", "┐", "└", "┘", "─", "│", "█", "░", blockBars}
)

// ground is what the colors are drawn on: a dark terminal, a light one, or
// one opsie couldn't ask.
type ground int

const (
	groundUnknown ground = iota
	groundDark
	groundLight
)

// tone is how colors are mixed for a ground: how light and how strong a
// color is before its own ink has a say, how far it may be moved from
// there (lo to hi, where it still reads), what text goes on top of it,
// and how light the greys are. On a dark terminal colors are bright; on
// a light one they're deep. When the ground isn't known they sit in the
// narrow middle that reads on both.
//
// lift is how much of an ink's own lightness the ground has room for,
// and soft is where the quieter of two shades goes: towards the ground.
type tone struct {
	l, c, lo, hi float64
	lift, soft   float64
	ink          string
	grey, border float64
}

var tones = map[ground]tone{
	groundDark:    {l: 0.72, c: 0.19, lo: 0.62, hi: 0.90, lift: 1, soft: -0.13, ink: "#101010", grey: 0.68, border: 0.46},
	groundLight:   {l: 0.50, c: 0.19, lo: 0.36, hi: 0.60, lift: 0.6, soft: 0.12, ink: "#ffffff", grey: 0.50, border: 0.72},
	groundUnknown: {l: 0.60, c: 0.17, lo: 0.52, hi: 0.64, lift: 0.3, soft: -0.08, ink: "#111111", grey: 0.60, border: 0.60},
}

// ink is one color of a palette, before it's mixed for a ground. Most are
// a hue on the OKLCH color wheel, with how light and how strong they are
// next to the others: colors that all have one lightness and one
// strength look made by a machine, so the ones that lead are lifted and
// the ones that only inform are quiet. The rest are the terminal's own:
// its text color, a grey, or one of its sixteen colors.
type ink struct {
	kind inkKind
	// h is the hue, in degrees; up how much lighter than the tone it is,
	// and c how strong, 1 being the tone's.
	h, up, c float64
	// quiet is the second of two shades of one hue.
	quiet bool
	// n is which of the terminal's sixteen colors.
	n int
}

type inkKind int

const (
	inkHue inkKind = iota
	// inkBare is no color at all: the terminal's own text.
	inkBare
	// inkGrey is the theme's grey.
	inkGrey
	// inkANSI is one of the terminal's sixteen colors, whatever its theme
	// makes of it.
	inkANSI
)

func hue(h float64) ink { return ink{h: h, c: 1} }

// lift makes an ink lighter than the rest, as yellows, greens and cyans
// are in the rainbow; without it a yellow is a brown.
func (i ink) lift(by float64) ink { i.up = by; return i }

// strength is how strong an ink is next to the rest.
func (i ink) strength(c float64) ink { i.c = c; return i }

// soft is the quieter shade of an ink, for the second of a pair.
func (i ink) soft() ink { i.quiet = true; i.c *= 0.8; return i }

func ansi16(n int) ink { return ink{kind: inkANSI, n: n} }

var (
	bare = ink{kind: inkBare}
	grey = ink{kind: inkGrey}
)

// palette is a theme's inks; they become colors at the tone of the
// ground.
type palette struct {
	// accent is the selection and, blended into accent2, the wordmark.
	accent, accent2 ink
	// nav is the tabs, key hints and links.
	nav ink
	// The charts and bars. In and out are two shades of one hue, and so
	// are read (disk) and written (wr).
	cpu, mem, disk, wr, rx, tx ink
	ok, info, warn, danger     ink
	// tint is the hue of the greys, or -1 for none.
	tint float64
	look look
}

// The terminal's sixteen colors, by their usual names.
const (
	ansiRed = 1 + iota
	ansiGreen
	ansiYellow
	ansiBlue
	ansiMagenta
	ansiCyan
	ansiBright = 8
)

var palettes = map[string]palette{
	// A city at night: blue, violet and cyan lead, pink, teal and orange
	// are the lights. The statuses are the usual four, with info a quiet
	// steel so it doesn't pass for the selection. Rounded.
	"opsie": {
		accent: hue(262), accent2: hue(325),
		nav: hue(300).lift(0.03).strength(0.9),
		cpu: hue(216).lift(0.08), mem: hue(350).lift(0.02),
		disk: hue(172).lift(0.06), wr: hue(172).lift(0.06).soft(),
		rx: hue(55).lift(0.06), tx: hue(55).lift(0.06).soft(),
		ok: hue(142).lift(0.08), info: hue(240).strength(0.45),
		warn: hue(92).lift(0.12), danger: hue(22),
		tint: 265, look: lookRound,
	},
	// The terminal's own sixteen colors, so opsie looks like the rest of
	// what's in it. There are only six hues among them, so the readings
	// borrow from the statuses, and the selection is the terminal's
	// colors swapped. Square.
	"terminal": {
		accent: bare, accent2: bare,
		nav: ansi16(ansiBright + ansiMagenta),
		cpu: ansi16(ansiCyan), mem: ansi16(ansiMagenta),
		disk: ansi16(ansiBright + ansiGreen), wr: ansi16(ansiGreen),
		rx: ansi16(ansiBright + ansiBlue), tx: ansi16(ansiBlue),
		ok: ansi16(ansiGreen), info: ansi16(ansiBlue),
		warn: ansi16(ansiYellow), danger: ansi16(ansiRed),
		tint: -1, look: lookSquare,
	},
	// No color but the statuses, and those quiet: everything else is the
	// terminal's own text. Out and written are the readings that can't be
	// told from their pair by place, so they're grey next to the other's
	// plain text.
	"mono": {
		accent: bare, accent2: grey, nav: bare,
		cpu: bare, mem: bare, disk: bare, wr: grey, rx: bare, tx: grey,
		ok: hue(150).strength(0.5), info: hue(245).strength(0.5),
		warn: hue(85).lift(0.06).strength(0.5), danger: hue(25).strength(0.5),
		tint: -1, look: lookSquare,
	},
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
	shade := func(l float64) color.Color {
		if p.tint < 0 {
			return lipgloss.Color(oklch(l, 0, 0))
		}
		return lipgloss.Color(oklch(l, 0.02, p.tint))
	}
	muted := shade(tn.grey)
	col := func(i ink) color.Color {
		switch i.kind {
		case inkBare:
			return lipgloss.NoColor{}
		case inkGrey:
			return muted
		case inkANSI:
			return lipgloss.ANSIColor(i.n)
		}
		l := tn.l + i.up*tn.lift
		if i.quiet {
			l += tn.soft
		}
		return lipgloss.Color(oklch(min(max(l, tn.lo), tn.hi), tn.c*i.c, i.h))
	}
	t := theme{
		name:    name,
		look:    p.look,
		text:    lipgloss.NoColor{},
		muted:   muted,
		faint:   muted,
		border:  shade(tn.border),
		track:   shade(tn.border),
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
	if p.danger.kind == inkANSI {
		// What reads on one of the terminal's colors is up to its theme;
		// its own background always does.
		t.buttonDanger = fg(t.danger).Reverse(true).Bold(true).Padding(0, 1)
	} else {
		t.buttonDanger = lipgloss.NewStyle().Foreground(on).Background(t.danger).Bold(true).Padding(0, 1)
	}
	if p.accent.kind == inkBare {
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
