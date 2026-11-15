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
	lookDouble = look{"╔", "╗", "╚", "╝", "═", "║", "▰", "▱", blockBars}
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
// the ones that only inform are quiet. Some are a color scheme's own
// color, given exactly. The rest are the terminal's own text color, or a
// grey.
type ink struct {
	kind inkKind
	// h is the hue, in degrees; up how much lighter than the tone it is,
	// and c how strong, 1 being the tone's.
	h, up, c float64
	// quiet is the second of two shades of one hue.
	quiet bool
	// hex is a scheme's color as it's written, and l and chroma what it
	// is in OKLCH, along with h.
	hex       string
	l, chroma float64
}

type inkKind int

const (
	inkHue inkKind = iota
	// inkBare is no color at all: the terminal's own text.
	inkBare
	// inkGrey is the theme's grey.
	inkGrey
	// inkExact is a color scheme's own color. It's shown as it is on a
	// dark terminal, which is what the schemes are made for, and moved
	// to where it reads on any other.
	inkExact
)

func hue(h float64) ink { return ink{h: h, c: 1} }

// lift makes an ink lighter than the rest, as yellows, greens and cyans
// are in the rainbow; without it a yellow is a brown.
func (i ink) lift(by float64) ink { i.up = by; return i }

// strength is how strong an ink is next to the rest.
func (i ink) strength(c float64) ink { i.c = c; return i }

// soft is the quieter shade of an ink, for the second of a pair.
func (i ink) soft() ink { i.quiet = true; i.c *= 0.8; return i }

// exact is a color scheme's own color, written as #rrggbb.
func exact(hex string) ink {
	l, c, h := toOKLCH(hex)
	return ink{kind: inkExact, hex: hex, l: l, chroma: c, h: h, c: 1}
}

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
	// tint is the hue of the greys, or -1 for none, and tintC how strong
	// it is when that's more than a trace.
	tint, tintC float64
	look        look
	// scheme palettes are someone else's colors, and there are fewer of
	// them than opsie has parts, so some parts share one.
	scheme bool
}

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

	// The color schemes below are other people's, each with the shapes
	// its look asks for. Their colors are as the schemes have them, in
	// opsie's parts: the selection never shares a color with a chart, and
	// a reading whose color changes when it runs high never shares one
	// with a warning. The network, whose color never changes, does where
	// a scheme runs out of hues.
	"catppuccin": {
		accent: exact("#cba6f7"), accent2: exact("#f5c2e7"), nav: exact("#89dceb"),
		cpu: exact("#89b4fa"), mem: exact("#fab387"),
		disk: exact("#94e2d5"), wr: exact("#94e2d5").soft(),
		rx: exact("#f5c2e7"), tx: exact("#f5c2e7").soft(),
		ok: exact("#a6e3a1"), info: exact("#9399b2"), warn: exact("#f9e2af"), danger: exact("#f38ba8"),
		tint: 267, look: lookRound, scheme: true,
	},
	"cyberpunk": {
		accent: exact("#00ffff"), accent2: exact("#ff00ff"), nav: exact("#ff00ff"),
		cpu: exact("#ffff00"), mem: exact("#ff00ff"),
		disk: exact("#00ff88"), wr: exact("#00ff88").soft(),
		rx: exact("#ffaa00"), tx: exact("#ffaa00").soft(),
		ok: exact("#00ff88"), info: grey, warn: exact("#ffaa00"), danger: exact("#ff0044"),
		tint: 285, tintC: 0.05, look: lookDouble, scheme: true,
	},
	"dracula": {
		accent: exact("#bd93f9"), accent2: exact("#ff79c6"), nav: exact("#ff79c6"),
		cpu: exact("#8be9fd"), mem: exact("#ffb86c"),
		disk: exact("#50fa7b"), wr: exact("#50fa7b").soft(),
		rx: exact("#f1fa8c"), tx: exact("#f1fa8c").soft(),
		ok: exact("#50fa7b"), info: exact("#6272a4"), warn: exact("#f1fa8c"), danger: exact("#ff5555"),
		tint: 265, tintC: 0.04, look: lookRound, scheme: true,
	},
	"gruvbox": {
		accent: exact("#fe8019"), accent2: exact("#fabd2f"), nav: exact("#d3869b"),
		cpu: exact("#83a598"), mem: exact("#8ec07c"),
		disk: exact("#b8bb26"), wr: exact("#b8bb26").soft(),
		rx: exact("#fabd2f"), tx: exact("#fabd2f").soft(),
		ok: exact("#b8bb26"), info: exact("#928374"), warn: exact("#fabd2f"), danger: exact("#fb4934"),
		tint: 70, tintC: 0.03, look: lookSquare, scheme: true,
	},
	"nord": {
		accent: exact("#88c0d0"), accent2: exact("#5e81ac"), nav: exact("#b48ead"),
		cpu: exact("#81a1c1"), mem: exact("#d08770"),
		disk: exact("#a3be8c"), wr: exact("#a3be8c").soft(),
		rx: exact("#ebcb8b"), tx: exact("#ebcb8b").soft(),
		ok: exact("#a3be8c"), info: exact("#5e81ac"), warn: exact("#ebcb8b"), danger: exact("#bf616a"),
		tint: 255, tintC: 0.03, look: lookSquare, scheme: true,
	},
	"ocean": {
		accent: exact("#55efc4"), accent2: exact("#48dbfb"), nav: exact("#48dbfb"),
		cpu: exact("#0abde3"), mem: exact("#c8d6e5"),
		disk: exact("#00d2d3"), wr: exact("#00d2d3").soft(),
		rx: exact("#feca57"), tx: exact("#feca57").soft(),
		ok: exact("#00d2d3"), info: exact("#8395a7"), warn: exact("#feca57"), danger: exact("#ff6b6b"),
		tint: 240, tintC: 0.04, look: lookRound, scheme: true,
	},
	// An amber screen, so the selection is what it was on one: the text
	// and the background swapped.
	"retro": {
		accent: bare, accent2: bare, nav: exact("#ffcc00"),
		cpu: exact("#ffb000"), mem: exact("#cc8800"),
		disk: exact("#33ff00"), wr: exact("#33ff00").soft(),
		rx: exact("#ff6600"), tx: exact("#ff6600").soft(),
		ok: exact("#33ff00"), info: grey, warn: exact("#ff6600"), danger: exact("#ff0033"),
		tint: 85, tintC: 0.08, look: lookDouble, scheme: true,
	},
	"rose-pine": {
		accent: exact("#ebbcba"), accent2: exact("#c4a7e7"), nav: exact("#c4a7e7"),
		cpu: exact("#9ccfd8"), mem: exact("#c4a7e7"),
		disk: exact("#31748f"), wr: exact("#31748f").soft(),
		rx: exact("#f6c177"), tx: exact("#f6c177").soft(),
		ok: exact("#31748f"), info: exact("#908caa"), warn: exact("#f6c177"), danger: exact("#eb6f92"),
		tint: 290, tintC: 0.03, look: lookRound, scheme: true,
	},
	"tokyo-night": {
		accent: exact("#7aa2f7"), accent2: exact("#bb9af7"), nav: exact("#bb9af7"),
		cpu: exact("#7dcfff"), mem: exact("#ff9e64"),
		disk: exact("#73daca"), wr: exact("#73daca").soft(),
		rx: exact("#e0af68"), tx: exact("#e0af68").soft(),
		ok: exact("#9ece6a"), info: exact("#737aa2"), warn: exact("#e0af68"), danger: exact("#f7768e"),
		tint: 265, tintC: 0.04, look: lookRound, scheme: true,
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
		return lipgloss.Color(oklch(l, max(p.tintC, 0.02), p.tint))
	}
	muted := shade(tn.grey)
	col := func(i ink) color.Color {
		switch i.kind {
		case inkBare:
			return lipgloss.NoColor{}
		case inkGrey:
			return muted
		case inkExact:
			return lipgloss.Color(i.on(g, tn))
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
	t.buttonDanger = lipgloss.NewStyle().Foreground(on).Background(t.danger).Bold(true).Padding(0, 1)
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

// on is a scheme's color on a ground. On a dark one it's the color as
// the scheme has it. The schemes are all made for dark backgrounds, so on
// any other the color keeps its hue and is moved to where the ground's
// tone says colors read, the lighter ones staying the lighter ones.
func (i ink) on(g ground, tn tone) string {
	l, c := i.l, i.chroma
	if g != groundDark {
		const from, to = 0.50, 0.95
		l = tn.lo + (min(max(l, from), to)-from)/(to-from)*(tn.hi-tn.lo)
		if c > 0.04 {
			// A pastel that's made deep goes muddy without more color.
			c = max(c, 0.11)
		}
	} else if !i.quiet {
		return i.hex
	}
	if i.quiet {
		// The quieter shade goes towards the ground, unless the color is
		// already as near to it as reads.
		lo, hi := tn.lo, tn.hi
		if g == groundDark {
			lo, hi = 0.50, 0.95
		}
		was := l
		if l+tn.soft < lo || l+tn.soft > hi {
			l -= tn.soft
		} else {
			l += tn.soft
		}
		l = min(max(l, lo), hi)
		if math.Abs(l-was) < 0.08 {
			// No room to be lighter or darker, so it's weaker instead.
			c *= 0.5
		}
	}
	return oklch(l, c*i.c, i.h)
}

// toOKLCH is the lightness, chroma and hue of an sRGB color written as
// #rrggbb.
func toOKLCH(hex string) (l, c, h float64) {
	var r8, g8, b8 int
	fmt.Sscanf(hex, "#%02x%02x%02x", &r8, &g8, &b8)
	lin := func(v int) float64 {
		x := float64(v) / 255
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	r, g, b := lin(r8), lin(g8), lin(b8)
	l_ := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m_ := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s_ := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	l = 0.2104542553*l_ + 0.7936177850*m_ - 0.0040720468*s_
	a := 1.9779984951*l_ - 2.4285922050*m_ + 0.4505937099*s_
	bb := 0.0259040371*l_ + 0.7827717662*m_ - 0.8086757660*s_
	h = math.Atan2(bb, a) * 180 / math.Pi
	if h < 0 {
		h += 360
	}
	return l, math.Hypot(a, bb), h
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
