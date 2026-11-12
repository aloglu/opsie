package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/agents"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
)

// The Agents tab: how much the AI coding agents on this server have been
// used, and how much of their plans' limits is left.

// agentsFresh is how long a reading of the logs is reused, and
// limitsFresh how often Anthropic is asked.
const (
	agentsFresh = time.Minute
	limitsFresh = 5 * time.Minute
)

type agentsState struct {
	list    []agents.Agent
	at      time.Time
	loading bool
	// claude is what Anthropic last said about the plan's limits.
	claude claudeLimits
	// services are the pay-as-you-go services with a key, as last asked.
	services   []agents.Service
	servicesAt time.Time
}

type claudeLimits struct {
	limits []agents.Window
	plan   string
	err    string
	at     time.Time
}

type agentsMsg struct {
	list     []agents.Agent
	claude   *claudeLimits
	services []agents.Service
	asked    bool
}

func (m *Model) agentsPoll(again bool) tea.Cmd {
	a := &m.agents
	if a.loading || (!again && !a.at.IsZero() && time.Since(a.at) < agentsFresh) {
		return nil
	}
	a.loading = true
	ask := m.env.Config.Agents.ClaudeLimits && (again || time.Since(a.claude.at) > limitsFresh)
	keys := agents.Keys{OpenRouter: m.env.Config.Agents.OpenRouterKey, DeepSeek: m.env.Config.Agents.DeepSeekKey}
	askServices := again || time.Since(a.servicesAt) > limitsFresh
	return func() tea.Msg {
		home, err := os.UserHomeDir()
		if err != nil {
			return agentsMsg{}
		}
		msg := agentsMsg{list: agents.Read(home, time.Now())}
		if ask {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			c := &claudeLimits{at: time.Now()}
			var err error
			if c.limits, c.plan, err = agents.ClaudeLimits(ctx, filepath.Join(home, ".claude")); err != nil {
				c.err = err.Error()
			}
			msg.claude = c
		}
		if askServices {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			msg.services, msg.asked = agents.Services(ctx, keys), true
		}
		return msg
	}
}

func (m *Model) agentsUpdated(msg agentsMsg) {
	a := &m.agents
	a.list, a.at, a.loading = msg.list, time.Now(), false
	if msg.claude != nil {
		a.claude = *msg.claude
	}
	if msg.asked {
		a.services, a.servicesAt = msg.services, time.Now()
	}
}

// agent is one agent with whatever else is known about its limits.
func (m *Model) agent(id string) (agents.Agent, bool) {
	for _, a := range m.agents.list {
		if a.ID != id {
			continue
		}
		if id == "claude" {
			c := m.agents.claude
			switch {
			case !m.env.Config.Agents.ClaudeLimits:
				a.LimitsNote = "Its plan limits aren't written to disk. opsie can ask Anthropic for them."
			case c.err != "":
				a.LimitsNote = "Plan limits: " + c.err + "."
			case len(c.limits) > 0:
				a.Limits, a.LimitsAt, a.LimitsNote = c.limits, c.at, ""
			}
			if c.plan != "" {
				a.Plan = c.plan
			}
		}
		return a, true
	}
	return agents.Agent{}, false
}

// tokens writes a count of tokens short: 950, 12K, 3.4M.
func tokens(n uint64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1e7:
		return fmt.Sprintf("%.0fM", float64(n)/1e6)
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1e4:
		return fmt.Sprintf("%.0fK", float64(n)/1e3)
	case n >= 1e3:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// resetsIn says when a limit starts over.
func (m *Model) resetsIn(w agents.Window) string {
	if w.Resets.IsZero() {
		return ""
	}
	left := w.Resets.Sub(m.now)
	switch {
	case left <= 0:
		return "starts over now"
	case left < 24*time.Hour:
		return "starts over in " + human.Duration(left)
	}
	return "starts over " + w.Resets.Local().Format("Mon 15:04")
}

// limitColor is green until a limit is mostly used.
func (t theme) limitColor(pct float64) colorT {
	switch {
	case pct >= 90:
		return t.danger
	case pct >= 70:
		return t.warn
	}
	return t.ok
}

func (m *Model) agentsSections(inner int) []section {
	t := m.th
	if m.agents.at.IsZero() {
		return []section{{title: "Agents", intro: []string{t.dim.Render("Reading their logs…")}}}
	}
	var out []section
	if len(m.agents.list) == 0 {
		out = append(out, section{title: "Agents", intro: wrap("No AI coding agents found in your home folder. opsie looks for Claude Code (~/.claude) and Codex (~/.codex), and reads the usage they record there.", inner)})
	}
	for _, a := range m.agents.list {
		a, _ = m.agent(a.ID)
		out = append(out, m.agentSection(a, inner))
	}
	// Then the services paid by use, and the ones that could be added.
	cfg := m.env.Config.Agents
	have := map[string]bool{"openrouter": cfg.OpenRouterKey != "", "deepseek": cfg.DeepSeekKey != ""}
	shown := map[string]bool{}
	for _, s := range m.agents.services {
		if have[s.ID] {
			out = append(out, m.serviceSection(s, inner))
			shown[s.ID] = true
		}
	}
	more := section{title: "Services"}
	for _, k := range agents.Known {
		switch {
		case shown[k.ID]:
		case have[k.ID]:
			more.intro = append(more.intro, t.dim.Render(fit("Asking "+k.Name+"…", inner)))
		default:
			more.entries = append(more.entries, entry{id: "set:agents." + k.ID + "_key", lines: m.action("Add "+k.Name, inner)})
		}
	}
	if len(more.entries) > 0 && len(shown) == 0 && len(more.intro) == 0 {
		more.intro = wrap("Services paid by use can show what a key has spent, once opsie has the key.", inner)
	}
	if len(more.entries)+len(more.intro) > 0 {
		out = append(out, more)
	}
	return out
}

// money writes an amount of a currency; small ones keep their cents'
// cents, since a day of a cheap model is a fraction of one.
func money(currency string, v float64) string {
	sign := map[string]string{"USD": "$", "": "$", "CNY": "¥", "EUR": "€"}[currency]
	if sign == "" {
		sign = currency + " "
	}
	if v > 0 && v < 0.1 {
		return fmt.Sprintf("%s%.4f", sign, v)
	}
	return fmt.Sprintf("%s%.2f", sign, v)
}

func (m *Model) serviceSection(s agents.Service, inner int) section {
	t := m.th
	sec := section{title: s.Name}
	if s.Label != "" {
		sec.badge = t.subtle.Render(s.Label)
	}
	if s.Err != "" {
		for _, line := range wrap(s.Name+" couldn't be asked: "+s.Err+".", inner) {
			sec.intro = append(sec.intro, t.warnText.Render(line))
		}
		sec.entries = []entry{{id: "set:agents." + s.ID + "_key", lines: m.action("Change the key", inner)}}
		return sec
	}
	var lines []string
	// What's left comes first, as a bar when there's a whole to be part of.
	left := func(label string, used, whole float64, note string) {
		pct := 0.0
		if whole > 0 {
			pct = min(max(used/whole*100, 0), 100)
		}
		c := t.limitColor(pct)
		const labelW = 8
		text := lipgloss.NewStyle().Foreground(c).Bold(true).Render(money("", whole-used)) + t.subtle.Render(" left of "+money("", whole))
		line := t.subtle.Render(fit(label, labelW)) + " "
		if gw := inner - labelW - 1 - 1 - lipgloss.Width(text); gw >= 4 {
			line += t.gauge(pct, gw, c) + " "
		}
		lines = append(lines, line+text)
		if note != "" {
			lines = append(lines, t.dim.Render(fit(strings.Repeat(" ", labelW+1)+note, inner)))
		}
	}
	if s.HaveCredits {
		left("Credits", s.Used, s.Bought, "")
	}
	if s.HaveLimit {
		note := "the key's own limit"
		if s.Reset != "" {
			note += ", starts over " + s.Reset
		}
		left("Limit", s.Limit-s.Remaining, s.Limit, note)
	}
	for _, b := range s.Balances {
		right := t.bold.Render(money(b.Currency, b.Total))
		if b.Granted > 0 {
			right = t.dim.Render(money(b.Currency, b.Granted)+" of it granted · ") + right
		}
		lines = append(lines, m.itemRow(t.subtle.Render("Balance"), right, inner)...)
	}
	if s.Blocked {
		lines = append(lines, t.dangerText.Render(fit("✗ Nothing left: requests are refused", inner)))
	}
	if s.HaveSpend {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		spent := func(label string, v float64) {
			right := t.dim.Render("nothing")
			if v > 0 {
				right = t.bold.Render(money("", v))
			}
			lines = append(lines, m.itemRow(t.subtle.Render(label), right, inner)...)
		}
		spent("Today", s.Today)
		spent("This week", s.Week)
		spent("This month", s.Month)
	}
	if s.FreeLimit > 0 && s.FreeUsed > 0 {
		lines = append(lines, m.itemRow(t.subtle.Render("Free models"), fmt.Sprintf("%d of %d requests today", s.FreeUsed, s.FreeLimit), inner)...)
	}
	sec.entries = []entry{{id: "service:" + s.ID, lines: lines, block: true}}
	return sec
}

func (m *Model) serviceDetail(id string, b *detailBuilder) detail {
	t := m.th
	for _, s := range m.agents.services {
		if s.ID != id {
			continue
		}
		host := ""
		for _, k := range agents.Known {
			if k.ID == id {
				host = k.Host
			}
		}
		word := "in use"
		st := health.Info
		if s.Blocked {
			st, word = health.Crit, "nothing left"
		}
		if s.Label != "" {
			b.kv("Key", s.Label)
		}
		if s.HaveCredits {
			b.share("Credits", pctOf(s.Used, s.Bought), t.limitColor(pctOf(s.Used, s.Bought)), money("", s.Bought-s.Used)+t.dim.Render(" left of "+money("", s.Bought)+" bought"))
		}
		if s.HaveLimit {
			used := s.Limit - s.Remaining
			text := money("", s.Remaining) + t.dim.Render(" left of "+money("", s.Limit))
			if s.Reset != "" {
				text += t.dim.Render(" · starts over " + s.Reset)
			}
			b.share("Key limit", pctOf(used, s.Limit), t.limitColor(pctOf(used, s.Limit)), text)
		} else if s.HaveSpend {
			b.kv("Key limit", t.dim.Render("none: the key can spend all of the account's credits"))
		}
		for _, bal := range s.Balances {
			v := money(bal.Currency, bal.Total)
			if bal.Granted > 0 {
				v += t.dim.Render(" · " + money(bal.Currency, bal.Granted) + " of it granted rather than paid for")
			}
			b.kv("Balance", v)
		}
		if s.HaveSpend {
			b.para = true
			b.row(t.bold.Render("Spent with this key"))
			// Each period as its share of the month's spending.
			for _, p := range []struct {
				label string
				v     float64
			}{{"Today", s.Today}, {"This week", s.Week}, {"This month", s.Month}} {
				b.share(p.label, pctOf(p.v, s.Month), t.nav, money("", p.v))
			}
			b.kv("Ever", money("", s.AllTime))
			b.text("Days, weeks (from Monday) and months are counted in UTC, as "+s.Name+" counts them.", t.dim)
		}
		if s.FreeLimit > 0 {
			b.share("Free models", pctOf(float64(s.FreeUsed), float64(s.FreeLimit)), t.limitColor(pctOf(float64(s.FreeUsed), float64(s.FreeLimit))), fmt.Sprintf("%d of %d requests today", s.FreeUsed, s.FreeLimit))
		}
		if id == "openrouter" && !s.HaveCredits {
			b.text("The account's credits only show with a management key; an ordinary key can see its own spending and limit.", t.subtle)
		}
		b.text("Asked of "+host+" "+human.Duration(m.now.Sub(m.agents.servicesAt))+" ago, with the key in Settings. The key is sent nowhere else.", t.dim)
		return detail{title: s.Name, headline: headline(t, st, word, s.Label), body: b.body, fixes: []health.Fix{
			{Label: "Ask again", Special: "agents:refresh"},
			{Label: "Change the key", Special: "open:settings:set:agents." + id + "_key"},
		}}
	}
	b.text("This service has no key anymore.", t.subtle)
	return detail{title: "Service", body: b.body}
}

func pctOf(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return min(max(part/whole*100, 0), 100)
}

func (m *Model) agentSection(a agents.Agent, inner int) section {
	t := m.th
	s := section{title: a.Name}
	if a.Plan != "" {
		s.badge = t.subtle.Render(a.Plan + " plan")
	}
	var lines []string
	// The limits first: they're what runs out.
	nameW := 0
	for _, w := range a.Limits {
		nameW = max(nameW, lipgloss.Width(w.Name))
	}
	nameW = min(nameW, max(inner/3, 7))
	for _, w := range a.Limits {
		c := t.limitColor(w.Used)
		pct := lipgloss.NewStyle().Foreground(c).Bold(true).Render(fmt.Sprintf("%3.0f%%", w.Used))
		line := t.subtle.Render(fit(w.Name, nameW)) + " "
		if gw := inner - nameW - 1 - 1 - 4; gw >= 4 {
			line += t.gauge(w.Used, gw, c) + " "
		}
		lines = append(lines, line+pct)
		if r := m.resetsIn(w); r != "" {
			lines = append(lines, t.dim.Render(fit(strings.Repeat(" ", nameW+1)+r, inner)))
		}
	}
	if len(a.Limits) > 0 && m.now.Sub(a.LimitsAt) > 10*time.Minute {
		lines = append(lines, t.dim.Render(fit("as of "+human.Duration(m.now.Sub(a.LimitsAt))+" ago, when it was last used", inner)))
	}
	if a.LimitsNote != "" {
		for _, line := range wrap(a.LimitsNote, inner) {
			lines = append(lines, t.dim.Render(line))
		}
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	count := func(tok agents.Tokens, sessions int) string {
		if tok.Total() == 0 {
			return t.dim.Render("not used")
		}
		return t.bold.Render(tokens(tok.Total())) + t.subtle.Render(" tokens · "+human.Plural(sessions, "session", "sessions"))
	}
	lines = append(lines, m.itemRow(t.subtle.Render("Today"), count(a.Today, a.SessionsToday), inner)...)
	lines = append(lines, m.itemRow(t.subtle.Render("7 days"), count(a.Week, a.SessionsWeek), inner)...)
	if a.Week.Total() > 0 {
		lines = append(lines, m.dailyChart(a, inner, 2)...)
	}
	if a.Model != "" {
		last := a.Model
		if !a.LastActive.IsZero() {
			last += t.dim.Render(" · " + human.Duration(m.now.Sub(a.LastActive)) + " ago")
		}
		lines = append(lines, m.itemRow(t.subtle.Render("Last"), last, inner)...)
	}
	s.entries = []entry{{id: "agent:" + a.ID, lines: lines, block: true}}
	return s
}

// dailyChart draws the last days' tokens as one wide bar a day, with the
// days named underneath.
func (m *Model) dailyChart(a agents.Agent, inner, h int) []string {
	t := m.th
	n := len(a.Daily)
	colW := max((inner-(n-1))/n, 1)
	var top float64
	for _, v := range a.Daily {
		top = max(top, float64(v))
	}
	rows := make([]string, h)
	labels := ""
	today := m.now
	for i, v := range a.Daily {
		col := t.chart(repeat(float64(v), colW), colW, h, top, func(float64) colorT { return t.nav })
		gap := ""
		if i > 0 {
			gap = " "
		}
		for r := range rows {
			rows[r] += gap + col[r]
		}
		day := today.AddDate(0, 0, i-(n-1)).Format("Mon")
		labels += gap + fit(day[:min(len(day), colW)], colW)
	}
	return append(rows, t.dim.Render(labels))
}

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func (m *Model) agentDetail(id string, b *detailBuilder) detail {
	t := m.th
	a, ok := m.agent(id)
	if !ok {
		b.text("This agent's folder isn't there anymore.", t.subtle)
		return detail{title: "Agent", body: b.body}
	}
	var fixes []health.Fix
	for _, w := range a.Limits {
		v := fmt.Sprintf("%.0f%% used", w.Used)
		if r := m.resetsIn(w); r != "" {
			v += t.dim.Render(" · " + r)
		}
		b.kv(w.Name, lipgloss.NewStyle().Foreground(t.limitColor(w.Used)).Render(v))
	}
	if len(a.Limits) > 0 {
		from := "what " + a.Name + " wrote down when it was last used, " + human.Duration(m.now.Sub(a.LimitsAt)) + " ago"
		if id == "claude" {
			from = "what Anthropic said " + human.Duration(m.now.Sub(a.LimitsAt)) + " ago"
		}
		b.text("These are the plan's limits, the ones its own usage screen shows: "+from+".", t.subtle)
	}
	if a.LimitsNote != "" {
		b.text(a.LimitsNote, t.subtle)
	}
	if id == "claude" && !m.env.Config.Agents.ClaudeLimits {
		b.text("To show them, opsie would read the login Claude Code keeps in ~/.claude and ask api.anthropic.com with it, every five minutes while this tab is open. The login is only read, never changed or sent anywhere else. It's off until you turn it on.", lipgloss.NewStyle())
		fixes = append(fixes, health.Fix{Label: "Show plan limits", Special: "open:settings:set:agents.claude_limits"})
	}

	b.para = true
	b.row(t.bold.Render("Tokens"))
	split := func(label string, tok agents.Tokens, sessions int) {
		if tok.Total() == 0 {
			b.kv(label, t.dim.Render("not used"))
			return
		}
		b.kv(label, tokens(tok.Total())+t.dim.Render(" in "+human.Plural(sessions, "session", "sessions")))
		b.kv("", t.subtle.Render(fmt.Sprintf("%s sent · %s written · %s read from cache · %s cached", tokens(tok.Input), tokens(tok.Output), tokens(tok.CacheRead), tokens(tok.CacheWrite))))
	}
	split("Today", a.Today, a.SessionsToday)
	split("7 days", a.Week, a.SessionsWeek)
	if a.Week.Total() > 0 {
		b.body = append(b.body, "")
		b.body = append(b.body, m.dailyChart(a, b.inner, 3)...)
		b.para = true
	}
	if len(a.Models) > 0 {
		names := make([]string, 0, len(a.Models))
		for name := range a.Models {
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool { return a.Models[names[i]] > a.Models[names[j]] })
		b.row(t.bold.Render("By model, 7 days"))
		for _, name := range names {
			b.kv("", fmt.Sprintf("%6s  %s", tokens(a.Models[name]), name))
		}
	}
	if a.Version != "" {
		b.kv("Version", a.Version)
	}
	if !a.LastActive.IsZero() {
		b.kv("Last used", human.Duration(m.now.Sub(a.LastActive))+" ago"+t.dim.Render(" · "+a.LastActive.Local().Format("2 Jan 15:04")))
	}
	where := map[string]string{"claude": "~/.claude/projects", "codex": "~/.codex/sessions"}[id]
	b.text("Counted from the session logs in "+where+", for your user on this server. Tokens read from cache are most of the count and the cheapest part of it.", t.dim)

	word := "not used today"
	if a.Today.Total() > 0 {
		word = tokens(a.Today.Total()) + " tokens today"
	}
	fixes = append(fixes, health.Fix{Label: "Read again", Special: "agents:refresh"})
	return detail{title: a.Name, headline: headline(t, health.Info, word, a.Plan), body: b.body, fixes: fixes}
}
