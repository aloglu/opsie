package tui

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aloglu/opsie/internal/backup"
	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/daemon"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/offsite"
	"github.com/aloglu/opsie/internal/priv"
)

type stage int

const (
	stageView stage = iota
	stageConfirm
	stageCountdown
	stageRunning
	stageInput
)

// modal is the detail view of one item. key says what it shows
// ("result:<id>", "ctr:<name>", "proj:<name>", "prune"); its content is
// rebuilt from current data on every frame, so it stays live.
type modal struct {
	key     string
	stage   stage
	focus   int
	fix     health.Fix
	count   int
	countID int
	started time.Time
	err     string
	scroll  int
	// input is the field for a fix that asks for a value.
	input textinput.Model
	// direct modals went straight to their one action, so backing out of
	// it closes them.
	direct bool
	// spots is where each button was drawn, and rows how many rows they
	// took, so up and down can move between rows.
	spots []btnSpot
	rows  int
	// shown is the stage last drawn, to notice arriving at a new one.
	shown stage
	drawn bool
}

// detail is a modal's content, wrapped for a width.
type detail struct {
	title    string
	headline string
	body     []string
	fixes    []health.Fix
}

// detailBuilder lays out a detail's body: paragraphs get a blank line
// before them, rows of facts don't.
type detailBuilder struct {
	t     theme
	inner int
	body  []string
	para  bool
}

func (b *detailBuilder) text(s string, style lipgloss.Style) {
	if len(b.body) > 0 {
		b.body = append(b.body, "")
	}
	for _, line := range wrap(s, b.inner) {
		b.body = append(b.body, style.Render(line))
	}
	b.para = true
}

// kv adds a label and value; long values wrap under themselves.
func (b *detailBuilder) kv(k, v string) {
	if b.para || len(b.body) == 0 {
		if len(b.body) > 0 {
			b.body = append(b.body, "")
		}
		b.para = false
	}
	const keyW = 12
	lines := wrap(v, b.inner-keyW)
	for i, line := range lines {
		label := ""
		if i == 0 {
			label = k
		}
		b.body = append(b.body, b.t.subtle.Render(fit(label, keyW-1))+" "+line)
	}
}

// row adds a line of its own, wrapped with a hanging indent.
func (b *detailBuilder) row(s string) {
	if b.para {
		b.body = append(b.body, "")
		b.para = false
	}
	for i, line := range wrap(s, b.inner-2) {
		if i > 0 {
			line = "  " + line
		}
		b.body = append(b.body, line)
	}
}

func headline(t theme, s health.Status, word, summary string) string {
	out := t.statusIcon(s) + " " + lipgloss.NewStyle().Foreground(t.statusColor(s)).Bold(true).Render(word)
	if summary != "" {
		out += t.subtle.Render(" · " + summary)
	}
	return out
}

func statusWord(s health.Status) string {
	switch s {
	case health.Crit:
		return "problem"
	case health.Warn:
		return "warning"
	case health.Unknown:
		return "unknown"
	case health.Info:
		return "info"
	}
	return "ok"
}

func (m *Model) detailFor(key string, inner int) detail {
	b := &detailBuilder{t: m.th, inner: inner}
	switch {
	case strings.HasPrefix(key, "result:"):
		return m.resultDetail(strings.TrimPrefix(key, "result:"), b)
	case strings.HasPrefix(key, "ctr:"):
		return m.containerDetail(strings.TrimPrefix(key, "ctr:"), b)
	case strings.HasPrefix(key, "proj:"):
		return m.projectDetail(strings.TrimPrefix(key, "proj:"), b)
	case key == "prune" || strings.HasPrefix(key, "prune:"):
		return m.pruneDetail(b)
	case key == "history:net":
		return m.netHistoryDetail(b)
	case strings.HasPrefix(key, "du:"):
		return m.duDetail(strings.TrimPrefix(key, "du:"), b)
	case key == "clean" || strings.HasPrefix(key, "clean:"):
		return m.cleanDetail(b)
	case strings.HasPrefix(key, "vol:"):
		return m.volumeDetail(strings.TrimPrefix(key, "vol:"), b)
	case key == "speedtest":
		return m.speedDetail(b)
	case key == "do:speedtest":
		return detail{title: "Speed test", fixes: []health.Fix{{Label: "Run a speed test", Special: "speedtest:run"}}}
	case strings.HasPrefix(key, "service:"):
		return m.serviceDetail(strings.TrimPrefix(key, "service:"), b)
	case strings.HasPrefix(key, "agent:"):
		return m.agentDetail(strings.TrimPrefix(key, "agent:"), b)
	case strings.HasPrefix(key, "site:"):
		return m.siteDetail(strings.TrimPrefix(key, "site:"), b)
	case strings.HasPrefix(key, "set:"):
		return m.settingDetail(strings.TrimPrefix(key, "set:"), b)
	case strings.HasPrefix(key, "tunnel:"):
		return m.tunnelDetail(strings.TrimPrefix(key, "tunnel:"), b)
	case strings.HasPrefix(key, "route:"):
		return m.routeDetail(key, b)
	case strings.HasPrefix(key, "app:"):
		return m.appDetail(strings.TrimPrefix(key, "app:"), b)
	case strings.HasPrefix(key, "unit:"):
		return m.unitDetail(strings.TrimPrefix(key, "unit:"), b)
	case strings.HasPrefix(key, "timer:"):
		return m.timerDetail(strings.TrimPrefix(key, "timer:"), b)
	case strings.HasPrefix(key, "backup:"):
		return m.backupDetail(strings.TrimPrefix(key, "backup:"), b)
	case strings.HasPrefix(key, "bfile:"):
		return m.backupFileDetail(strings.TrimPrefix(key, "bfile:"), b)
	case key == "audit":
		return m.auditDetail(b)
	case key == "backupkey":
		return m.backupKeyDetail(b)
	case strings.HasPrefix(key, "cron:"):
		return m.cronDetail(strings.TrimPrefix(key, "cron:"), b)
	case strings.HasPrefix(key, "pid:"):
		return m.procDetail(strings.TrimPrefix(key, "pid:"), b)
	}
	if d, ok := m.securityDetail(key, b); ok {
		return d
	}
	if d, ok := m.updatesDetail(key, b); ok {
		return d
	}
	return detail{title: "Gone", body: []string{m.th.subtle.Render("This item isn't there anymore.")}}
}

func (m *Model) resultDetail(id string, b *detailBuilder) detail {
	for _, r := range m.results {
		if r.ID != id {
			continue
		}
		for _, p := range r.Detail {
			b.text(p, lipgloss.NewStyle())
		}
		if r.ID == "watcher" {
			m.historyLines(b)
		}
		if kind := historyKind("result:" + r.ID); kind != "" {
			m.historyCharts(kind, b)
		}
		return detail{title: r.Title, headline: headline(m.th, r.Status, statusWord(r.Status), r.Summary), body: b.body, fixes: r.Fixes}
	}
	b.text("This is fine now: the last check didn't find it anymore.", m.th.subtle)
	return detail{title: "All clear", headline: headline(m.th, health.OK, "ok", ""), body: b.body}
}

// modalOpened starts anything the new modal needs, like fresh numbers.
func (m *Model) modalOpened() tea.Cmd {
	if m.modal == nil {
		return nil
	}
	if historyKind(m.modal.key) != "" {
		return m.loadHistory()
	}
	switch k := m.modal.key; {
	case strings.HasPrefix(k, "proj:"):
		return tea.Batch(m.pollDocker(), m.preflight(strings.TrimPrefix(k, "proj:")))
	case strings.HasPrefix(k, "ctr:"), k == "prune", strings.HasPrefix(k, "prune:"), strings.HasPrefix(k, "app:"):
		return m.pollDocker()
	case strings.HasPrefix(k, "unit:"), strings.HasPrefix(k, "timer:"), strings.HasPrefix(k, "cron:"):
		return m.pollServices()
	case strings.HasPrefix(k, "du:"):
		return m.loadDu(strings.TrimPrefix(k, "du:"))
	case k == "clean" || strings.HasPrefix(k, "clean:"):
		return m.loadClean()
	case strings.HasPrefix(k, "backup:"), strings.HasPrefix(k, "bfile:"):
		return tea.Batch(m.loadBackups(false), m.pollDocker())
	}
	return nil
}

type button struct {
	label  string
	danger bool
	press  func(m *Model) tea.Cmd
}

func (m *Model) buttons() []button {
	d := m.modal
	if d == nil {
		return nil
	}
	closeModal := func(m *Model) tea.Cmd { m.modal = nil; return nil }
	back := func(m *Model) tea.Cmd { m.modal.stage, m.modal.focus = stageView, 0; return nil }
	switch d.stage {
	case stageView:
		var out []button
		for _, f := range m.detailFor(d.key, 40).fixes {
			out = append(out, button{label: f.Label, danger: f.Danger, press: func(m *Model) tea.Cmd { return m.chooseFix(f) }})
		}
		return append(out, button{label: "Close", press: closeModal})
	case stageConfirm:
		return []button{
			{label: d.fix.Label, danger: true, press: func(m *Model) tea.Cmd { return m.confirmFix() }},
			{label: "Cancel", press: back},
		}
	case stageCountdown:
		return []button{{label: "Cancel", press: back}}
	case stageInput:
		return []button{
			{label: d.fix.Label, press: func(m *Model) tea.Cmd { return m.submitInput() }},
			{label: "Cancel", press: func(m *Model) tea.Cmd { m.leaveInput(); return nil }},
		}
	}
	return nil
}

// safeButton is the first button that doesn't need a confirmation.
func safeButton(btns []button) int {
	for i, b := range btns {
		if !b.danger {
			return i
		}
	}
	return len(btns) - 1
}

func (m *Model) chooseFix(f health.Fix) tea.Cmd {
	if f.Input != nil {
		in := textinput.New()
		in.Placeholder = f.Input.Placeholder
		in.Prompt = "› "
		in.CharLimit = 16 << 10
		in.SetValue(f.Input.Value)
		if f.Input.Secret {
			in.EchoMode = textinput.EchoPassword
		}
		m.modal.stage, m.modal.fix, m.modal.focus, m.modal.err, m.modal.input = stageInput, f, 0, "", in
		return m.modal.input.Focus()
	}
	if f.Danger {
		// Cancel is focused, so a stray Enter doesn't do anything drastic.
		m.modal.stage, m.modal.fix, m.modal.focus = stageConfirm, f, 1
		return nil
	}
	m.modal.fix = f
	return m.runFix(f)
}

// submitInput checks the typed value and runs the fix with it.
func (m *Model) submitInput() tea.Cmd {
	d := m.modal
	value := strings.TrimSpace(d.input.Value())
	if _, problem := checkInput(d.fix.Input.Kind, value); problem != "" {
		d.err = problem
		return nil
	}
	f := d.fix
	args := map[string]string{}
	for k, v := range f.Args {
		args[k] = v
	}
	args[f.Input.Arg] = value
	if !f.Input.Secret {
		f.Confirm = strings.ReplaceAll(f.Confirm, "{"+f.Input.Arg+"}", value)
		f.Done = strings.ReplaceAll(f.Done, "{"+f.Input.Arg+"}", value)
	}
	f.Args, f.Input = args, nil
	d.err = ""
	return m.chooseFix(f)
}

// checkInput describes a value as it's typed, or says what's wrong with it.
func checkInput(kind, value string) (note, problem string) {
	if value == "" {
		return "", "Type something first."
	}
	switch kind {
	case "sshdest":
		if !offsite.ValidSSH(value) {
			return "", "Write it as user@host:/folder, like backup@nas.example:/srv/backups/harbor."
		}
		return "", ""
	case "weekly":
		if _, _, _, ok := daemon.ParseWeekly(value); !ok {
			return "", "Write a day and a time, like sun 18:00 or mon 09:30."
		}
		return "", ""
	case "list":
		return human.Plural(len(splitList(value)), "item", "items"), ""
	case "agepub":
		if _, err := backup.ParseRecipient(value); err != nil {
			return "", "That isn't an age public key. It's one word starting with age1; the secret key (AGE-SECRET-KEY-…) stays private."
		}
		return "age public key", ""
	case "agekey":
		ids, err := backup.ParseIdentities(value)
		if err != nil {
			return "", "That isn't an age secret key. It starts with AGE-SECRET-KEY-1."
		}
		return human.Plural(len(ids), "secret key", "secret keys"), ""
	case "key":
		k, ok := priv.ParseKey(value)
		if !ok {
			return "", "That isn't an SSH public key. It's one line starting with ssh-ed25519, ssh-rsa or ecdsa-sha2-, usually from a .pub file."
		}
		if strings.HasPrefix(value, "-----") || strings.Contains(value, "PRIVATE KEY") {
			return "", "That's a private key. Paste the public one (the .pub file) instead."
		}
		note = k.Type + " " + k.Fingerprint
		if k.Comment != "" {
			note += " · " + k.Comment
		}
		return note, ""
	case "ip":
		if net.ParseIP(value) == nil {
			if _, _, err := net.ParseCIDR(value); err != nil {
				return "", "That isn't an IP address or a range like 203.0.113.0/24."
			}
		}
	case "port":
		if !portInputRe.MatchString(strings.ToLower(value)) {
			return "", "Use a port number like 443, or add tcp or udp: 443/tcp, 6000:6010/udp."
		}
	case "url":
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return "", "Use a full address starting with https://"
		}
		if u.Scheme == "http" {
			return "Not encrypted: anyone along the way can read what's sent.", ""
		}
	case "clock":
		if !priv.ValidClock(value) {
			return "", "Use a time of day like 04:00 or 23:30."
		}
	case "urls":
		for _, one := range splitList(value) {
			if _, problem := checkInput("url", one); problem != "" {
				return "", one + ": " + problem
			}
		}
	case "hours":
		if !config.ValidHours(value) {
			return "", "Use a range like 23:00-08:00."
		}
	case "chat":
		if !chatRe.MatchString(value) {
			return "", "Use the chat's number, like 123456789 or -1001234567890, or @channelname."
		}
	}
	if lo, hi, ok := numberRange(kind); ok {
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return "", "Use a number."
		}
		if n < lo || n > hi {
			return "", fmt.Sprintf("Use a number from %s to %s.", trimFloat(lo), trimFloat(hi))
		}
	}
	return "", ""
}

var chatRe = regexp.MustCompile(`^(-?\d{1,20}|@[A-Za-z0-9_]{4,64})$`)

// numberRange reads "number:MIN:MAX".
func numberRange(kind string) (lo, hi float64, ok bool) {
	parts := strings.Split(kind, ":")
	if len(parts) != 3 || parts[0] != "number" {
		return 0, 0, false
	}
	lo, err1 := strconv.ParseFloat(parts[1], 64)
	hi, err2 := strconv.ParseFloat(parts[2], 64)
	return lo, hi, err1 == nil && err2 == nil
}

func trimFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

var portInputRe = regexp.MustCompile(`^\d{1,5}(:\d{1,5})?(/(tcp|udp)| (tcp|udp))?$`)

func (m *Model) confirmFix() tea.Cmd {
	d := m.modal
	if d.fix.Countdown > 0 {
		d.stage, d.count, d.focus = stageCountdown, d.fix.Countdown, 0
		d.countID++
		return countdown(d.countID)
	}
	return m.runFix(d.fix)
}

func countdown(id int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return countdownMsg(id) })
}

func (m *Model) countdownTick(id int) tea.Cmd {
	d := m.modal
	if d == nil || d.stage != stageCountdown || id != d.countID {
		return nil
	}
	d.count--
	if d.count <= 0 {
		return m.runFix(d.fix)
	}
	return countdown(id)
}

func (m *Model) leaveInput() {
	if m.modal.direct {
		m.modal = nil
		return
	}
	m.modal.stage, m.modal.focus, m.modal.err = stageView, 0, ""
}

// inputKey sends typing to the field; only a few keys mean something else.
func (m *Model) inputKey(msg tea.KeyPressMsg) tea.Cmd {
	d := m.modal
	switch msg.String() {
	case "enter":
		if d.focus == 1 {
			m.leaveInput()
			return nil
		}
		return m.submitInput()
	case "esc":
		m.leaveInput()
		return nil
	case "tab", "down":
		d.focus = (d.focus + 1) % 2
		return nil
	case "shift+tab", "up":
		d.focus = (d.focus + 1) % 2
		return nil
	}
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	d.err = ""
	return cmd
}

func (m *Model) modalKey(key string) tea.Cmd {
	d := m.modal
	btns := m.buttons()
	switch key {
	case "esc", "q", "backspace":
		switch d.stage {
		case stageInput:
			m.leaveInput()
		case stageConfirm, stageCountdown:
			d.stage, d.focus = stageView, 0
		case stageView:
			m.modal = nil
		}
	case "tab":
		if len(btns) > 0 {
			d.focus = (d.focus + 1) % len(btns)
		}
	case "shift+tab":
		if len(btns) > 0 {
			d.focus = (d.focus + len(btns) - 1) % len(btns)
		}
	// The arrows that run along the buttons choose one; the others scroll
	// the text. Which is which follows how the buttons were laid out.
	case "left", "h":
		if d.focus > 0 {
			d.focus--
		}
	case "right", "l":
		if d.focus < len(btns)-1 {
			d.focus++
		}
	// With more than one row of buttons, up and down move between rows;
	// past the top or bottom row, and with one row, they scroll the text.
	case "up", "k":
		if i, ok := d.rowStep(-1); ok {
			d.focus = i
		} else {
			d.scroll--
		}
	case "down", "j":
		if i, ok := d.rowStep(1); ok {
			d.focus = i
		} else {
			d.scroll++
		}
	case "pgup", "b":
		d.scroll -= 5
	case "pgdown", "f":
		d.scroll += 5
	case "enter", "space":
		if d.focus < len(btns) {
			return btns[d.focus].press(m)
		}
	default:
		// 1–9 press a button directly, except while confirming: a stray
		// digit mustn't go ahead without the Cancel in between.
		if d.stage == stageConfirm || d.stage == stageCountdown {
			return nil
		}
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			if i := int(key[0] - '1'); i < len(btns) {
				d.focus = i
				return btns[i].press(m)
			}
		}
	}
	return nil
}

// btnGap is the space between buttons in a row.
const btnGap = 2

// btnSpot is where a button sits among the popup's buttons.
type btnSpot struct{ x, y, w int }

// rowStep is the button on the row above (dir -1) or below (dir 1) the
// focused one, the one nearest it across.
func (d *modal) rowStep(dir int) (int, bool) {
	if d.rows < 2 || d.focus >= len(d.spots) {
		return 0, false
	}
	cur := d.spots[d.focus]
	mid := cur.x + cur.w/2
	best, bestDist := -1, 0
	for i, sp := range d.spots {
		if sp.y != cur.y+dir {
			continue
		}
		dist := abs(sp.x + sp.w/2 - mid)
		if best < 0 || dist < bestDist {
			best, bestDist = i, dist
		}
	}
	return best, best >= 0
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// modalBody draws the detail popup over bg, the page behind it, which is
// dimmed so the popup stands out. Tapping outside the popup closes it.
func (m *Model) modalBody(bg []string, w, h, top int) []string {
	t := m.th
	d := m.modal
	// A column of the page shows either side, so it reads as a popup even
	// on a phone.
	cw := min(w-2, 78)
	x0 := (w - cw) / 2
	inner := cw - 4
	content := m.detailFor(d.key, inner)

	var text []string
	if content.headline != "" {
		text = append(text, content.headline, "")
	}
	text = append(text, content.body...)
	if d.err != "" {
		text = append(text, "")
		for _, line := range wrap("✗ "+d.err, inner) {
			text = append(text, t.dangerText.Render(line))
		}
	}
	switch d.stage {
	case stageConfirm:
		text = append(text, "")
		for _, line := range wrap(d.fix.Confirm, inner) {
			text = append(text, t.warnText.Bold(true).Render(line))
		}
	case stageCountdown:
		text = append(text, "", t.warnText.Bold(true).Render(fmt.Sprintf("%s in %d…", d.fix.Label, d.count)))
	case stageInput:
		in := d.fix.Input
		text = append(text, "")
		for _, line := range wrap(in.Prompt, inner) {
			text = append(text, t.bold.Render(line))
		}
		d.input.SetWidth(max(inner-3, 4))
		text = append(text, d.input.View())
		if note, _ := checkInput(in.Kind, strings.TrimSpace(d.input.Value())); note != "" {
			for _, line := range wrap(note, inner) {
				text = append(text, t.okText.Render(line))
			}
		}
	case stageRunning:
		working := "Working…"
		if since := time.Since(d.started); since >= 3*time.Second {
			working = "Working… " + human.Duration(since)
		}
		text = append(text, "", t.subtle.Render(d.fix.Label+": "+working))
	}

	// Buttons go in a row when they fit, otherwise one per line.
	btns := m.buttons()
	if d.stage == stageView && (!d.drawn || d.shown != stageView) {
		// Arriving at the actions, start on a harmless one, so a quick
		// Enter can't head for anything drastic.
		d.focus = safeButton(btns)
	}
	d.shown, d.drawn = d.stage, true
	d.focus = min(d.focus, max(len(btns)-1, 0))
	// A few buttons sit in a row; more than fit in one become a menu.
	// head is the menu's rule, above its first row of buttons.
	btnLines, spots := m.buttonRow(btns, inner)
	menuTop, head := 0, 0
	if btnLines == nil {
		btnLines, spots, menuTop = m.buttonMenu(btns, inner, max((h-2)/2, 3))
		head = 1
	}
	d.spots, d.rows = spots, 0
	for _, sp := range spots {
		d.rows = max(d.rows, sp.y+1)
	}

	// Text scrolls; the buttons stay put at the bottom.
	room := h - 2 - len(btnLines)
	if len(btnLines) > 0 {
		room--
	}
	room = max(room, 1)
	maxScroll := max(len(text)-room, 0)
	d.scroll = min(max(d.scroll, 0), maxScroll)
	visible := text[d.scroll:min(d.scroll+room, len(text))]
	if d.scroll < maxScroll {
		visible = append(visible[:len(visible)-1:len(visible)-1], t.dim.Render("↓ more"))
	}
	body := append([]string{}, visible...)
	if len(btnLines) > 0 {
		body = append(body, "")
	}
	btnTop := len(body)
	body = append(body, btnLines...)

	card := t.card(content.title, "", body, cw, 0, -1, -1, true)
	// Centered, a little above the middle where the eye goes first.
	y0 := max((h-len(card.lines))*2/5, 0)
	out := make([]string, h)
	for i := range out {
		plain := ""
		if i < len(bg) {
			plain = ansi.Strip(bg[i])
		}
		plain = fit(plain, w)
		if i < y0 || i >= y0+len(card.lines) {
			out[i] = t.backdrop.Render(plain)
			continue
		}
		left := t.backdrop.Render(ansi.Cut(plain, 0, x0))
		right := t.backdrop.Render(ansi.Cut(plain, x0+cw, w))
		out[i] = fit(left+card.lines[i-y0]+right, w)
	}
	// Taps outside the popup close it; the popup's own hits come after,
	// so they win where they overlap.
	m.hits = append(m.hits, hit{x: 0, y: top, w: w, h: h, click: func(m *Model) tea.Cmd { return m.modalKey("esc") }},
		hit{x: x0, y: top + y0, w: cw, h: len(card.lines), click: func(*Model) tea.Cmd { return nil }})
	for i, sp := range spots {
		// Menu rows scrolled out of sight can't be tapped.
		row := sp.y - menuTop + head
		if row < head || row >= len(btnLines) {
			continue
		}
		if y := y0 + 1 + btnTop + row; y < h {
			m.hits = append(m.hits, hit{x: x0 + 2 + sp.x, y: top + y, w: sp.w, h: 1, click: btns[i].press})
		}
	}
	return out
}

// buttonRow lays the buttons out side by side, or returns nil when they
// don't all fit in one row.
func (m *Model) buttonRow(btns []button, inner int) ([]string, []btnSpot) {
	t := m.th
	if len(btns) == 0 {
		return nil, nil
	}
	line, x := "", 0
	var spots []btnSpot
	for i, b := range btns {
		label := b.label
		if len(btns) > 1 {
			label = fmt.Sprintf("%d %s", i+1, b.label)
		}
		p := t.pill(label, i == m.modal.focus, b.danger)
		pw := lipgloss.Width(p)
		if i > 0 {
			line += strings.Repeat(" ", btnGap)
			x += btnGap
		}
		spots = append(spots, btnSpot{x: x, y: 0, w: pw})
		line += p
		x += pw
	}
	if x > inner || len(btns) > 9 {
		return nil, nil
	}
	return []string{line}, spots
}

// buttonMenu lays the buttons out as a menu under a rule: one per line,
// or in two or three even columns when the popup is wide enough for
// their labels. The chosen one gets the selection band across its cell.
// At most maxRows of it show at once, the ones around the chosen button;
// top is the first row showing.
func (m *Model) buttonMenu(btns []button, inner, maxRows int) (lines []string, spots []btnSpot, top int) {
	t := m.th
	d := m.modal
	const numW, colGap = 2, 2
	longest := 0
	for _, b := range btns {
		longest = max(longest, lipgloss.Width(b.label))
	}
	cols := 1
	for c := 3; c > 1; c-- {
		// Columns are for menus long enough to need them.
		if (inner-colGap*(c-1))/c >= longest+numW+2 && len(btns) > c+1 {
			cols = c
			break
		}
	}
	cellW := (inner - colGap*(cols-1)) / cols
	rows := (len(btns) + cols - 1) / cols
	for i := range btns {
		spots = append(spots, btnSpot{x: (i % cols) * (cellW + colGap), y: i / cols, w: cellW})
	}
	if rows > maxRows {
		top = min(max(d.focus/cols-maxRows/2, 0), rows-maxRows)
	}
	shown := min(rows, maxRows)

	rule := strings.Repeat("─", inner)
	if note := map[[2]bool]string{{true, false}: " ↑ more ", {false, true}: " ↓ more ", {true, true}: " ↑↓ more "}[[2]bool{top > 0, top+shown < rows}]; note != "" {
		rule = strings.Repeat("─", max(inner-lipgloss.Width(note)-2, 0)) + note + "──"
	}
	lines = append(lines, t.borderText.Render(fit(rule, inner)))
	for r := top; r < top+shown; r++ {
		line := ""
		for c := 0; c < cols; c++ {
			i := r*cols + c
			if c > 0 {
				line += strings.Repeat(" ", colGap)
			}
			if i >= len(btns) {
				line += strings.Repeat(" ", cellW)
				continue
			}
			// The first nine have a key.
			num := "  "
			if i < 9 {
				num = fmt.Sprintf("%d ", i+1)
			}
			b := btns[i]
			label := ansi.Truncate(b.label, max(cellW-numW-2, 1), "…")
			cell := fit(" "+num+label, cellW)
			switch {
			case i == d.focus && b.danger:
				line += t.buttonDanger.Padding(0).Render(cell)
			case i == d.focus:
				line += t.selected.Bold(true).Render(cell)
			case b.danger:
				line += " " + t.dim.Render(num) + t.dangerText.Render(fit(label, cellW-numW-1))
			default:
				line += " " + t.dim.Render(num) + fit(label, cellW-numW-1)
			}
		}
		lines = append(lines, line)
	}
	return lines, spots, top
}
