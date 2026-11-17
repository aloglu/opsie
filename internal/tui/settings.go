package tui

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
)

// setting is one line of config.toml that the Settings tab can change.
type setting struct {
	key, label, help string
	// kind is "choice" (pick from options) or an input kind for checkInput.
	kind    string
	options []string
	// integer settings are written without a decimal point.
	integer bool
	// boolean choices are "on" and "off", written as true and false.
	boolean bool
	secret  bool
	// optional settings can be cleared; the others go back to a default.
	optional    bool
	placeholder string
	// hidden settings are changed from another screen.
	hidden bool
}

type settingGroup struct {
	title    string
	settings []setting
}

var settingGroups = []settingGroup{
	{"Look", []setting{
		{key: "theme", label: "Theme", kind: "choice", options: config.Themes,
			help: "opsie's look. Each theme has its own colors and its own shapes: opsie's own, blue and violet on rounded cards (opsie), no color but the statuses (mono), or one of nine well-known color schemes. The schemes are made for dark terminals; on a light one their colors are deepened so they still read. `opsie themes` shows them all. Text and the background are always your terminal's own."},
		{key: "background", label: "Background", kind: "choice", options: config.Backgrounds,
			help: "Colors are mixed for the terminal's background: light and clear on a dark one, deep on a light one. auto asks the terminal which it has; if yours doesn't answer and the colors look washed out, say dark or light here."},
	}},
	{"Charts", []setting{
		{key: "charts.style", label: "Chart style", kind: "choice", options: config.ChartStyles,
			help: "How the charts are drawn: solid bars (bars), a fine dotted line (braille), a drawn line (line), or bars at twice the detail, two readings to a column (dense). A chart one row tall is always bars."},
		{key: "charts.cpu", label: "CPU chart", kind: "choice", options: config.ChartChoices,
			help: "The CPU chart's own style, or the same as the rest."},
		{key: "charts.memory", label: "Memory chart", kind: "choice", options: config.ChartChoices,
			help: "The memory and swap charts' own style, or the same as the rest."},
		{key: "charts.network", label: "Network chart", kind: "choice", options: config.ChartChoices,
			help: "The network charts' own style, or the same as the rest."},
		{key: "charts.disk", label: "Disk chart", kind: "choice", options: config.ChartChoices,
			help: "The style of the charts of what the disks read and write and how full they are, or the same as the rest."},
	}},
	{"Alerts", []setting{
		{key: "notify.ntfy_url", label: "ntfy topic", kind: "url", optional: true, placeholder: "https://ntfy.sh/a-long-secret-name",
			help: "Alerts go to this ntfy topic. Anyone who knows the address can read it, so make the name long and random. Subscribe to the same topic in the ntfy app."},
		{key: "notify.ntfy_token", label: "ntfy token", kind: "text", secret: true, optional: true, placeholder: "tk_…",
			help: "Only needed for an ntfy server that requires logging in."},
		{key: "notify.telegram_token", label: "Telegram bot", kind: "text", secret: true, optional: true, placeholder: "123456:ABC-DEF…",
			help: "A bot token from @BotFather. The bot posts alerts in the chat below; send it a message first so it's allowed to."},
		{key: "notify.telegram_chat", label: "Telegram chat", kind: "chat", optional: true, placeholder: "123456789",
			help: "The chat the bot posts in: your own chat's number, a group's (starting with -100), or @channel."},
		{key: "notify.webhook_url", label: "Webhook", kind: "url", optional: true, placeholder: "https://example.com/hook",
			help: "Gets a JSON POST for every alert, with title, text, lines and level."},
		{key: "notify.level", label: "Send", kind: "choice", options: []string{"warn", "crit"},
			help: "warn sends warnings and problems; crit sends problems only."},
		{key: "notify.logins", label: "SSH logins", kind: "choice", options: []string{"new", "all", "off"},
			help: "new alerts when someone logs in from an address the background checks haven't seen in the last three months; all alerts on every login (quiet hours hold those back); off sends none."},
		{key: "notify.weekly", label: "Weekly summary", kind: "weekly", optional: true, placeholder: "sun 18:00",
			help: "Sends a summary of the week at this time, like \"sun 18:00\": uptime, problems that came and went, how busy the server was, disk use and backups. Clear it to stop."},
		{key: "notify.quiet_hours", label: "Quiet hours", kind: "hours", optional: true, placeholder: "23:00-08:00",
			help: "During these hours only problems are sent; warnings wait until they're over. Times are the server's."},
		{key: "notify.repeat_hours", label: "Remind after", kind: "number:0:720",
			help: "Hours before a problem that's still there is sent again. 0 never reminds."},
	}},
	{"Checks", []setting{
		{key: "check_seconds", label: "Check every", kind: "number:5:3600", integer: true,
			help: "Seconds between health checks, here and in the background checks."},
		{key: "refresh_seconds", label: "Live stats every", kind: "number:0.5:60",
			help: "Seconds between updates of CPU, memory and network on the dashboard."},
		{key: "thresholds.disk_warn", label: "Disk warning", kind: "number:1:100", help: "Percent of a disk used before it's a warning."},
		{key: "thresholds.disk_crit", label: "Disk problem", kind: "number:1:100", help: "Percent of a disk used before it's a problem."},
		{key: "thresholds.memory_warn", label: "Memory warning", kind: "number:1:100", help: "Percent of memory in use before it's a warning."},
		{key: "thresholds.memory_crit", label: "Memory problem", kind: "number:1:100", help: "Percent of memory in use before it's a problem."},
		{key: "thresholds.swap_warn", label: "Swap warning", kind: "number:1:100", help: "Percent of swap in use before it's a warning."},
		{key: "thresholds.load_warn", label: "Load warning", kind: "number:0.1:100", help: "Load per CPU core before it's a warning. 1 means every core is busy."},
		{key: "thresholds.load_crit", label: "Load problem", kind: "number:0.1:100", help: "Load per CPU core before it's a problem."},
	}},
	{"Sites", []setting{
		{key: "sites.check", label: "Check sites", kind: "choice", options: []string{"on", "off"}, boolean: true,
			help: "Visits every tunnel hostname and the extra sites, like a visitor would, and warns when one doesn't answer or is slow."},
		{key: "sites.extra", label: "Extra sites", kind: "urls", optional: true, placeholder: "https://example.com, https://status.example.com",
			help: "More addresses to check, separated by commas: sites that aren't behind this server's tunnel, or a page deeper in one that is."},
		{key: "sites.visit_minutes", label: "Visit every", kind: "number:1:60", integer: true,
			help: "Minutes between visits to each site while it's fine. A site that's failing or slow is visited every minute instead, until it answers again."},
		{key: "sites.slow_seconds", label: "Slow after", kind: "number:0.5:60",
			help: "Seconds a site may take to answer before it's a warning."},
		{key: "sites.alert_after_minutes", label: "Alert after", kind: "number:0:30", integer: true,
			help: "Minutes a site must stay down or slow before it counts as a problem and sends an alert. A bad visit is often a blip, like a container restarting, so a failing site is visited again every minute until it answers. Higher means fewer alerts about blips, but you hear about real outages later. 0 alerts on the first bad visit."},
	}},
	{"Images", []setting{
		{key: "images.check", label: "Look for new images", kind: "choice", options: []string{"on", "off"}, boolean: true,
			help: "Every six hours, asks Docker Hub, GitHub and the other registries your images come from whether their tags point at a newer image. It only reads, never downloads, and skips images built here."},
	}},
	{"Agents", []setting{
		{key: "agents.claude_limits", label: "Claude plan limits", kind: "choice", options: []string{"on", "off"}, boolean: true,
			help: "Claude Code doesn't write its plan's limits to disk, so the Agents tab can only show them by asking Anthropic. When this is on, opsie reads the login Claude Code keeps in ~/.claude and asks api.anthropic.com with it, at most every five minutes while the Agents tab is open. The login is only read: never changed, stored or sent anywhere else. Codex's limits need nothing; it writes them down itself."},
		{key: "agents.openrouter_key", label: "OpenRouter keys", kind: "list", secret: true, optional: true, placeholder: "sk-or-v1-…",
			help: "One or more OpenRouter API keys. For each, the Agents tab shows what the key has spent today, this week and this month, its spending limit if it has one, and its free-model requests. opsie asks openrouter.ai, at most every five minutes; the keys are sent nowhere else. A management key also shows the account's credits. A key can be given a name from its card on the Agents tab, or written here as name=key."},
		{key: "agents.deepseek_key", label: "DeepSeek key", kind: "text", secret: true, optional: true, placeholder: "sk-…",
			help: "A DeepSeek API key. With it the Agents tab shows the account's balance. opsie asks api.deepseek.com, at most every five minutes; the key is sent nowhere else."},
		{key: "agents.check", label: "Watch spending", kind: "choice", options: []string{"on", "off"}, boolean: true,
			help: "Keeps asking the services with a key what's left, so the checklist warns when a key's limit, the account's credits or its balance run low, and says when a key is refused or has nothing left. The background checks ask every fifteen minutes and send an alert. When this is off, the services are only asked while the Agents tab is open."},
		{key: "agents.low_percent", label: "Spending low at", kind: "number:0:100",
			help: "Percent of a key's spending limit, or of the account's credits, that can be left before it's a warning. 0 only warns once nothing is left."},
		{key: "agents.low_balance", label: "Balance low at", kind: "number:0:100000",
			help: "For an account that only shows a balance, like DeepSeek: the balance, in the account's currency, under which it's a warning. 0 only warns once requests are refused."},
	}},
	{"Apps", []setting{
		{key: "apps_dir", label: "Apps folder", kind: "text", placeholder: "~/apps",
			help: "Where your apps' folders are, each with a compose file. The Docker tab lists the ones that aren't running. ~ is your home."},
	}},
	{"Docker", []setting{
		{key: "docker.public_ports", label: "Public on purpose", kind: "list", optional: true, placeholder: "game-server:27015/udp",
			help: "Container ports that are meant to be reachable from the internet, so opsie doesn't warn about them. Each is container:port/protocol, separated by commas."},
	}},
	{"Backups", []setting{
		{key: "backup.dir", label: "Backup folder", kind: "text", optional: true, placeholder: "~/opsie-backups",
			help: "Where app backups are saved: a folder in your home, or under /mnt, /media, /srv or /var/backups, like another disk. Backups belong to you, so you can copy them off the server. ~ is your home."},
		{key: "backup.keep", label: "Keep", kind: "number:1:100", integer: true,
			help: "How many backups of each app are kept. Making one more deletes the oldest."},
		{key: "backup.nightly", label: "Scheduled apps", kind: "list", optional: true, placeholder: "sync, wiki",
			help: "Apps the background checks back up on their own, by project name, separated by commas. Each app's Backups screen can switch it on too. With none listed, nothing is backed up unless you do it yourself with Back up now."},
		{key: "backup.every_days", label: "Days between", kind: "number:1:30", integer: true,
			help: "Days between scheduled backups: 1 is every night, 7 once a week. Fewer backups means less space taken, here and off the server."},
		{key: "backup.at", label: "Scheduled at", kind: "clock",
			help: "When the scheduled backups run, in the server's time. If the server is off then, they still run if it's back within two hours."},
		{key: "backup.stop", label: "Stop apps while copying", kind: "choice", options: []string{"on", "off"}, boolean: true,
			help: "Scheduled backups stop each app for the moment it's copied, so databases are saved whole. Turn it off only for apps that can't have any downtime."},
		{key: "backup.encrypt_to", label: "Encryption key", kind: "agepub", optional: true, hidden: true},
	}},
	{"Off-server copies", []setting{
		{key: "backup.keep_remote", label: "Keep off the server", kind: "number:0:1000", integer: true,
			help: "How many copies of each app's backups stay off the server. After a new copy arrives, opsie deletes the older ones there, over SSH or from the bucket, so the space they take stops growing. 0 keeps every copy and deletes nothing, which is how it has always been. Only files opsie put there are ever deleted."},
		{key: "backup.ssh", label: "Copy over SSH to", kind: "sshdest", optional: true, placeholder: "backup@nas.example:/srv/backups/harbor",
			help: "Each backup is also copied here with scp, using your SSH key: another server, or a computer at home. Connect to it once yourself first so its host key is known; opsie won't trust a host it hasn't seen. Give each server a folder of its own. Old copies there stay unless Keep off the server is set."},
		{key: "backup.ssh_port", label: "SSH port", kind: "number:1:65535", integer: true, optional: true, placeholder: "22",
			help: "Only if the other machine's SSH isn't on port 22."},
		{key: "backup.s3.endpoint", label: "S3 or R2 endpoint", kind: "url", optional: true, placeholder: "https://s3.eu-central-003.backblazeb2.com",
			help: "Each backup is also uploaded to this S3-compatible storage: Backblaze B2, Cloudflare R2 (https://ACCOUNT.r2.cloudflarestorage.com), AWS, Wasabi or MinIO. Old copies stay there unless Keep off the server is set, or the bucket has an expiry rule."},
		{key: "backup.s3.region", label: "S3 region", kind: "text", optional: true, placeholder: "us-east-1",
			help: "The bucket's region, like eu-central-003 for B2, or auto for R2."},
		{key: "backup.s3.bucket", label: "S3 bucket", kind: "text", optional: true, placeholder: "my-backups"},
		{key: "backup.s3.prefix", label: "S3 folder", kind: "text", optional: true, placeholder: "harbor",
			help: "A folder in the bucket for this server's backups. Give each server its own, so one never clears another's copies."},
		{key: "backup.s3.key_id", label: "S3 key ID", kind: "text", optional: true,
			help: "An access key that can write to the bucket. Writing alone is safest, since a stolen key then can't read or delete your backups. Keep off the server also needs it to list and delete, and fetching backups onto a new server needs it to list and read."},
		{key: "backup.s3.secret", label: "S3 secret", kind: "text", secret: true, optional: true},
	}},
}

func settingFor(key string) (setting, bool) {
	for _, g := range settingGroups {
		for _, s := range g.settings {
			if s.key == key {
				return s, true
			}
		}
	}
	return setting{}, false
}

// value parses what was picked or typed into what config.toml holds.
func (s setting) value(text string) (any, error) {
	switch {
	case s.boolean:
		return text == "on", nil
	case s.kind == "urls", s.kind == "list":
		return splitList(text), nil
	case strings.HasPrefix(s.kind, "number"):
		n, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, err
		}
		if s.integer {
			return int64(n), nil
		}
		return n, nil
	}
	return text, nil
}

func formatValue(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case float64:
		return trimFloat(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case string:
		return v
	case bool:
		if v {
			return "on"
		}
		return "off"
	case []any:
		var parts []string
		for _, x := range v {
			parts = append(parts, fmt.Sprint(x))
		}
		return strings.Join(parts, ", ")
	}
	return fmt.Sprint(v)
}

// maskSecrets masks a secret setting's value: one secret, or a list of
// them.
func maskSecrets(v any) string {
	list, ok := v.([]any)
	if !ok {
		return maskSecret(formatValue(v))
	}
	var parts []string
	for _, x := range list {
		parts = append(parts, maskKey(fmt.Sprint(x)))
	}
	if len(parts) > 2 {
		return strconv.Itoa(len(parts)) + " keys"
	}
	return strings.Join(parts, ", ")
}

// maskKey masks a key that may have a name: the name if it has one, or
// else its last characters.
func maskKey(entry string) string {
	k := config.ParseKey(entry)
	if k.Name != "" {
		return k.Name
	}
	return maskSecret(k.Secret)
}

func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "••••"
	}
	return "••••" + s[len(s)-4:]
}

// settingsState is what the tab shows: the effective values and which of
// them the file sets.
func (m *Model) settingValues() (effective, inFile map[string]any) {
	return m.env.Config.Values(), config.FileValues(m.env.Paths.ConfigFile())
}

func (m *Model) settingsSections(inner int) []section {
	t := m.th
	eff, file := m.settingValues()
	var out []section
	for _, g := range settingGroups {
		sec := section{title: g.title}
		for _, s := range g.settings {
			if s.hidden {
				continue
			}
			v := formatValue(eff[s.key])
			_, set := file[s.key]
			right := t.subtle.Render(v)
			switch {
			case v == "" && s.key == "backup.dir":
				right = t.dim.Render(tildePath(m.backupDir()) + " · default")
			case v == "":
				right = t.dim.Render("not set")
			case s.secret:
				right = t.subtle.Render(maskSecrets(eff[s.key]))
			case !set:
				right = t.dim.Render(v + " · default")
			}
			sec.entries = append(sec.entries, entry{id: "set:" + s.key, lines: m.itemRow(s.label, right, inner)})
		}
		if g.title == "Alerts" && m.env.Config.Notify.Targets() {
			sec.entries = append(sec.entries, entry{id: "set:test", lines: m.action("Send a test alert", inner)})
			sec.entries = append(sec.entries, entry{id: "set:weekly-now", lines: m.action("Send this week's summary", inner)})
		}
		if g.title == "Backups" {
			sec.entries = append(sec.entries, entry{id: "set:encryption", lines: m.itemRow("Encryption", t.subtle.Render(m.encryptionWord()), inner)})
			sec.entries = append(sec.entries, entry{id: "set:packall", lines: m.action("Back up everything", inner)})
			sec.entries = append(sec.entries, entry{id: "set:restoreall", lines: m.action("Restore from backups", inner)})
		}
		if g.title == "Alerts" && len(m.env.Config.Problems()) > 0 {
			sec.outro = wrap(t.warnText.Render(strings.Join(m.env.Config.Problems(), "; ")+"."), inner)
		}
		out = append(out, sec)
	}
	out = append(out, section{title: "opsie", entries: []entry{
		{id: "set:version", lines: m.itemRow("Version", m.versionWord(), inner)},
	}})
	bg := section{title: "Background"}
	if m.haveWatcher {
		word := "off"
		for _, r := range m.results {
			if r.ID == "watcher" {
				word = r.Summary
			}
		}
		bg.entries = append(bg.entries, entry{id: "result:watcher", lines: m.itemRow("Background checks", t.subtle.Render(word), inner)})
		if !m.env.Dev {
			word := "off"
			if m.watcher.Motd {
				word = "on"
			}
			bg.entries = append(bg.entries, entry{id: "set:motd", lines: m.itemRow("Summary at login", t.subtle.Render(word), inner)})
		}
	}
	bg.entries = append(bg.entries, entry{id: "audit", lines: m.itemRow("Actions taken", t.subtle.Render(m.lastAction()), inner)})
	out = append(out, bg)
	// The sections are in the order of their names, so one is found
	// without knowing what's in it.
	slices.SortStableFunc(out, func(a, b section) int {
		return strings.Compare(strings.ToLower(a.title), strings.ToLower(b.title))
	})
	// Where it's all saved is the last thing on the page, whichever
	// section that is.
	last := &out[len(out)-1]
	if len(last.outro) > 0 {
		last.outro = append(last.outro, "")
	}
	last.outro = append(last.outro, wrap(t.dim.Render("Saved in "+tildePath(m.env.Paths.ConfigFile())+", which you can also edit by hand. The background checks pick changes up on their own."), inner)...)
	return out
}

func (m *Model) settingDetail(key string, b *detailBuilder) detail {
	t := m.th
	plain := lipgloss.NewStyle()
	switch key {
	case "test":
		b.text("Sends a test message to "+strings.Join(health.Targets(m.env.Config.Notify), " and ")+", so you can see alerts arrive.", plain)
		return detail{title: "Test alert", body: b.body, fixes: []health.Fix{{Label: "Send a test alert", Special: "notify:test"}}}
	case "encryption":
		return m.encryptionDetail(b)
	case "packall":
		return m.packAllDetail(b)
	case "restoreall":
		return m.restoreAllDetail(b)
	case "weekly-now":
		b.text("Sends the weekly summary to "+strings.Join(health.Targets(m.env.Config.Notify), " and ")+" now, so you can see what it looks like. It's built from what the background checks recorded.", plain)
		return detail{title: "Weekly summary", body: b.body, fixes: []health.Fix{{Label: "Send it now", Special: "notify:weekly"}}}
	case "version":
		return m.versionDetail(b)
	case "motd":
		if m.watcher.Motd {
			b.text("A short summary of the server shows each time you log in over SSH.", plain)
			return detail{title: "Summary at login", headline: headline(t, health.OK, "on", ""), body: b.body,
				fixes: []health.Fix{{Label: "Hide summary at login", Special: "motd:off"}}}
		}
		b.text("opsie can print a short summary each time you log in over SSH: how the server is and what needs attention, from the background checks.", plain)
		return detail{title: "Summary at login", headline: headline(t, health.Info, "off", ""), body: b.body,
			fixes: []health.Fix{{Label: "Show summary at login", Special: "motd:on"}}}
	}
	s, ok := settingFor(key)
	if !ok {
		b.text("opsie doesn't have this setting.", t.subtle)
		return detail{title: "Setting", body: b.body}
	}
	eff, file := m.settingValues()
	cur := formatValue(eff[s.key])
	_, set := file[s.key]
	b.text(s.help, plain)
	shown := cur
	switch {
	case cur == "":
		shown = t.dim.Render("not set")
	case s.secret && s.kind == "list":
		list, _ := eff[s.key].([]any)
		shown = strconv.Itoa(len(list)) + " set"
	case s.secret:
		shown = maskSecret(cur)
	case !set:
		shown += t.dim.Render(" · default")
	}
	b.kv("Now", shown)
	b.kv("In file", s.key)
	args := func(extra ...string) map[string]string {
		a := map[string]string{"key": s.key}
		for i := 0; i+1 < len(extra); i += 2 {
			a[extra[i]] = extra[i+1]
		}
		return a
	}
	var fixes []health.Fix
	if s.kind == "choice" {
		for _, o := range s.options {
			label := o
			if o == cur {
				label = "✓ " + o
			}
			fixes = append(fixes, health.Fix{Label: label, Special: "config:set", Args: args("value", o)})
		}
		return detail{title: s.label, body: b.body, fixes: fixes}
	}
	if s.secret && s.kind == "list" {
		// Secrets can't be shown to be edited, so they're added and
		// removed one at a time.
		label := "Add a key"
		if cur != "" {
			label = "Add another key"
		}
		fixes = append(fixes, health.Fix{Label: label, Special: "config:add", Args: args(), Done: "Added the key",
			Input: &health.Input{Prompt: s.label, Arg: "value", Placeholder: s.placeholder, Kind: s.kind, Secret: true}})
		if list, ok := eff[s.key].([]any); ok {
			for _, x := range list {
				k := fmt.Sprint(x)
				fixes = append(fixes, health.Fix{Label: "Remove " + maskKey(k), Special: "config:remove", Args: args("value", k), Done: "Removed the key",
					Danger: true, Confirm: "Stop showing the key " + maskKey(k) + "? opsie forgets it; the key itself keeps working."})
			}
		}
		return detail{title: s.label, body: b.body, fixes: fixes}
	}
	label := "Change"
	if cur == "" {
		label = "Set"
	}
	in := &health.Input{Prompt: s.label, Arg: "value", Placeholder: s.placeholder, Kind: s.kind, Secret: s.secret}
	if !s.secret {
		in.Value = cur
	}
	fixes = append(fixes, health.Fix{Label: label, Special: "config:set", Args: args(), Input: in})
	if set && (s.optional || cur != "") {
		clear := "Use the default"
		if s.optional {
			clear = "Clear"
		}
		fixes = append(fixes, health.Fix{Label: clear, Special: "config:unset", Args: args()})
	}
	return detail{title: s.label, body: b.body, fixes: fixes}
}

// saveSetting writes a setting and starts using it right away.
func (m *Model) saveSetting(f health.Fix) tea.Cmd {
	s, ok := settingFor(f.Args["key"])
	if !ok {
		return nil
	}
	var v any
	if f.Special == "config:add" || f.Special == "config:remove" || f.Special == "config:name" {
		// Add one item to a list setting.
		var list []string
		if cur, ok := m.env.Config.Values()[s.key].([]any); ok {
			for _, x := range cur {
				list = append(list, fmt.Sprint(x))
			}
		}
		if f.Special == "config:remove" {
			list = slices.DeleteFunc(list, func(x string) bool { return x == f.Args["value"] })
		} else if f.Special == "config:name" {
			// Give one key of the list a name of its own.
			for i, x := range list {
				if x == f.Args["entry"] {
					list[i] = config.Key{Name: strings.TrimSpace(strings.ReplaceAll(f.Args["value"], "=", " ")), Secret: config.ParseKey(x).Secret}.String()
				}
			}
		} else {
			// Several can be typed or pasted at once.
			for _, x := range splitList(f.Args["value"]) {
				if !slices.Contains(list, x) {
					list = append(list, x)
				}
			}
		}
		v = list
	} else if f.Special == "config:set" {
		var err error
		if v, err = s.value(strings.TrimSpace(f.Args["value"])); err != nil {
			return m.setFlash("That isn't a valid value", flashError, 5*time.Second)
		}
	}
	path := m.env.Paths.ConfigFile()
	if err := config.Set(path, s.key, v); err != nil {
		if m.modal != nil {
			m.modal.stage, m.modal.err = stageView, err.Error()
		}
		return m.setFlash("Couldn't save: "+err.Error(), flashError, 6*time.Second)
	}
	cfg, err := config.Load(path)
	m.env.Config, m.env.ConfigErr = cfg, err
	m.applyTheme()
	m.modal = nil
	m.evaluate()
	var again tea.Cmd
	if strings.HasPrefix(s.key, "agents.") {
		// A new key or permission: ask with it now.
		m.agents.loading = false
		again = m.agentsPoll(true)
	}
	done := "Saved " + strings.ToLower(s.label)
	if f.Done != "" {
		done = f.Done
	}
	if v == nil {
		done = "Cleared " + strings.ToLower(s.label)
	}
	return tea.Batch(m.setFlash(done, flashOK, 4*time.Second), m.readWatcher(), again)
}

// splitList reads a list typed as "a, b c".
func splitList(text string) []string {
	return append([]string{}, strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' })...)
}

// lastAction says when opsie last changed something.
func (m *Model) lastAction() string {
	info, err := os.Stat(m.env.Paths.AuditLog())
	if err != nil {
		return "none yet"
	}
	return "last " + human.Duration(m.now.Sub(info.ModTime())) + " ago"
}
