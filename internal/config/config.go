// Package config reads opsie's settings file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the contents of config.toml. Every field is optional.
type Config struct {
	// RefreshSeconds is how often live stats (CPU, memory, network) update.
	RefreshSeconds float64 `toml:"refresh_seconds"`
	// CheckSeconds is how often the slower health checks run.
	CheckSeconds int `toml:"check_seconds"`
	// Theme is the color scheme, one of Themes.
	Theme string `toml:"theme"`
	// Background is "dark" or "light" to say what the terminal's is, or
	// "auto" to ask the terminal.
	Background string `toml:"background"`
	// AppsDir is where your apps' folders are, each with a compose file.
	// "~" is your home.
	AppsDir    string     `toml:"apps_dir"`
	Thresholds Thresholds `toml:"thresholds"`
	Notify     Notify     `toml:"notify"`
	Sites      Sites      `toml:"sites"`
	Images     Images     `toml:"images"`
	Backup     Backup     `toml:"backup"`
	Docker     Docker     `toml:"docker"`
	Agents     Agents     `toml:"agents"`

	badTheme string
	badAt    string
}

// Themes are the color schemes opsie has.
var Themes = []string{"opsie", "terminal", "mono"}

// Backgrounds are what Background can be.
var Backgrounds = []string{"auto", "dark", "light"}

// Apps is AppsDir with "~" expanded to home.
func (c Config) Apps(home string) string {
	d := strings.TrimSpace(c.AppsDir)
	if d == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(d, "~/"); ok {
		return filepath.Join(home, rest)
	}
	if !filepath.IsAbs(d) {
		return filepath.Join(home, d)
	}
	return filepath.Clean(d)
}

// Agents holds settings about the AI coding agents on the server.
type Agents struct {
	// ClaudeLimits lets opsie ask Anthropic for the Claude plan's limits
	// with the login Claude Code keeps.
	ClaudeLimits bool `toml:"claude_limits"`
	// OpenRouterKey and DeepSeekKey are API keys for those services; with
	// one set, the Agents tab asks the service what the key has spent.
	OpenRouterKey string `toml:"openrouter_key"`
	DeepSeekKey   string `toml:"deepseek_key"`
}

// Docker holds settings about containers.
type Docker struct {
	// PublicPorts are published ports that are meant to be reachable from
	// the internet, like "game-server:27015/udp", so they aren't warned
	// about.
	PublicPorts []string `toml:"public_ports"`
}

// Backup is where app backups go and how they're kept.
type Backup struct {
	// Dir is the folder for backups; "" is ~/opsie-backups. "~" is your
	// home.
	Dir string `toml:"dir"`
	// Keep is how many backups of each app are kept; older ones go.
	Keep int `toml:"keep"`
	// EncryptTo is an age public key (age1…). When it's set, backups are
	// encrypted to it and only its secret key can open them.
	EncryptTo string `toml:"encrypt_to"`
	// Nightly are the apps (compose projects) the background service backs
	// up every night at At, a time like "03:30". Stop stops each app while
	// it's copied, so databases are saved whole.
	Nightly []string `toml:"nightly"`
	At      string   `toml:"at"`
	Stop    bool     `toml:"stop"`
	// SSH copies each backup to another machine, like
	// "backup@nas.example:/srv/backups/harbor", with your SSH key.
	SSH     string `toml:"ssh"`
	SSHPort int    `toml:"ssh_port"`
	// S3 copies each backup to S3-compatible storage.
	S3 S3 `toml:"s3"`
}

// S3 is a bucket on S3-compatible storage: AWS, Backblaze B2, Cloudflare
// R2, Wasabi, MinIO and others.
type S3 struct {
	Endpoint string `toml:"endpoint"` // like https://s3.eu-central-003.backblazeb2.com
	Region   string `toml:"region"`   // "auto" for R2
	Bucket   string `toml:"bucket"`
	Prefix   string `toml:"prefix"` // a folder in the bucket
	KeyID    string `toml:"key_id"`
	Secret   string `toml:"secret"`
}

// Expand turns "~" in a path into home.
func Expand(d, home string) string {
	d = strings.TrimSpace(d)
	if d == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(d, "~/"); ok {
		return filepath.Join(home, rest)
	}
	if !filepath.IsAbs(d) {
		return filepath.Join(home, d)
	}
	return filepath.Clean(d)
}

// Images says whether opsie asks registries about newer images.
type Images struct {
	// Check asks Docker Hub, GitHub and other registries every few hours
	// whether the tags the containers run point at a newer image.
	Check bool `toml:"check"`
}

// Sites are the public websites opsie visits to check they answer.
type Sites struct {
	// Check turns the checks on; they cover every tunnel hostname.
	Check bool `toml:"check"`
	// Extra are more addresses to check, like a site hosted elsewhere.
	Extra []string `toml:"extra"`
	// SlowSeconds is how long an answer takes before it's a warning.
	SlowSeconds float64 `toml:"slow_seconds"`
}

// Notify says where the background checks send alerts, and when.
type Notify struct {
	// NtfyURL is an ntfy topic URL, like https://ntfy.sh/some-secret-name.
	NtfyURL   string `toml:"ntfy_url"`
	NtfyToken string `toml:"ntfy_token"`
	// TelegramToken is a bot token from @BotFather; TelegramChat the chat
	// to post to.
	TelegramToken string `toml:"telegram_token"`
	TelegramChat  string `toml:"telegram_chat"`
	// WebhookURL gets a JSON POST for every alert.
	WebhookURL string `toml:"webhook_url"`
	// Level is the least serious status that's sent: "warn" or "crit".
	Level string `toml:"level"`
	// QuietHours, like "23:00-08:00", holds back everything but problems.
	QuietHours string `toml:"quiet_hours"`
	// RepeatHours re-sends a problem that's still there after this long;
	// 0 never does.
	RepeatHours float64 `toml:"repeat_hours"`
	// Logins is which SSH logins send an alert: "new" (from an address
	// not seen before), "all", or "off".
	Logins string `toml:"logins"`
	// Weekly sends a summary of the past week at a time like "sun 18:00";
	// "" doesn't.
	Weekly string `toml:"weekly"`
}

// Targets reports whether any destination is set.
func (n Notify) Targets() bool {
	return n.NtfyURL != "" || (n.TelegramToken != "" && n.TelegramChat != "") || n.WebhookURL != ""
}

// Quiet reports whether t falls in the quiet hours.
func (n Notify) Quiet(t time.Time) bool {
	from, to, ok := parseHours(n.QuietHours)
	if !ok {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	if from <= to {
		return m >= from && m < to
	}
	return m >= from || m < to // across midnight
}

func parseHours(s string) (from, to int, ok bool) {
	a, b, found := strings.Cut(strings.TrimSpace(s), "-")
	if !found {
		return 0, 0, false
	}
	clock := func(s string) (int, bool) {
		t, err := time.Parse("15:04", strings.TrimSpace(s))
		if err != nil {
			return 0, false
		}
		return t.Hour()*60 + t.Minute(), true
	}
	from, ok1 := clock(a)
	to, ok2 := clock(b)
	return from, to, ok1 && ok2 && from != to
}

// Thresholds decide when a reading counts as a warning or a problem.
// Percentages are 0–100; load is per CPU core.
type Thresholds struct {
	DiskWarn   float64 `toml:"disk_warn"`
	DiskCrit   float64 `toml:"disk_crit"`
	MemoryWarn float64 `toml:"memory_warn"`
	MemoryCrit float64 `toml:"memory_crit"`
	SwapWarn   float64 `toml:"swap_warn"`
	LoadWarn   float64 `toml:"load_warn"`
	LoadCrit   float64 `toml:"load_crit"`
}

// Default returns the settings used when config.toml doesn't say otherwise.
func Default() Config {
	return Config{
		RefreshSeconds: 2,
		CheckSeconds:   30,
		Thresholds: Thresholds{
			DiskWarn: 85, DiskCrit: 95,
			MemoryWarn: 90, MemoryCrit: 97,
			SwapWarn: 60,
			LoadWarn: 1.5, LoadCrit: 3,
		},
		Notify:     Notify{Level: "warn", RepeatHours: 12, Logins: "new"},
		Sites:      Sites{Check: true, SlowSeconds: 5},
		Images:     Images{Check: true},
		Theme:      "opsie",
		Background: "auto",
		AppsDir:    "~/apps",
		Backup:     Backup{Keep: 5, At: "03:30", Stop: true},
	}
}

// Load reads path. A missing file isn't an error; a broken one returns the
// defaults along with the error so opsie can still start.
func Load(path string) (Config, error) {
	cfg := Default()
	_, err := toml.DecodeFile(path, &cfg)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Default(), fmt.Errorf("%s: %w", path, err)
	}
	cfg.clamp()
	return cfg, nil
}

func (c *Config) clamp() {
	d := Default()
	if c.RefreshSeconds < 0.5 || c.RefreshSeconds > 60 {
		c.RefreshSeconds = d.RefreshSeconds
	}
	if c.CheckSeconds < 5 || c.CheckSeconds > 3600 {
		c.CheckSeconds = d.CheckSeconds
	}
	fix := func(v *float64, def float64) {
		if *v <= 0 {
			*v = def
		}
	}
	t, dt := &c.Thresholds, d.Thresholds
	fix(&t.DiskWarn, dt.DiskWarn)
	fix(&t.DiskCrit, dt.DiskCrit)
	fix(&t.MemoryWarn, dt.MemoryWarn)
	fix(&t.MemoryCrit, dt.MemoryCrit)
	fix(&t.SwapWarn, dt.SwapWarn)
	fix(&t.LoadWarn, dt.LoadWarn)
	fix(&t.LoadCrit, dt.LoadCrit)
	if !slices.Contains(Themes, c.Theme) {
		c.badTheme, c.Theme = c.Theme, d.Theme
	}
	if !slices.Contains(Backgrounds, c.Background) {
		c.Background = d.Background
	}
	if strings.TrimSpace(c.AppsDir) == "" {
		c.AppsDir = d.AppsDir
	}
	if c.Backup.Keep < 1 || c.Backup.Keep > 100 {
		c.Backup.Keep = d.Backup.Keep
	}
	if _, err := time.Parse("15:04", c.Backup.At); err != nil {
		c.badAt, c.Backup.At = c.Backup.At, d.Backup.At
	}
	if c.Sites.SlowSeconds < 0.5 || c.Sites.SlowSeconds > 60 {
		c.Sites.SlowSeconds = d.Sites.SlowSeconds
	}
	n := &c.Notify
	if n.Level != "warn" && n.Level != "crit" {
		n.Level = "warn"
	}
	if n.Logins != "all" && n.Logins != "off" {
		n.Logins = "new"
	}
	if n.RepeatHours < 0 || n.RepeatHours > 24*30 {
		n.RepeatHours = 12
	}
}

// Problems lists settings that look wrong, for showing to the user.
func (c Config) Problems() []string {
	var out []string
	if c.badTheme != "" {
		out = append(out, fmt.Sprintf("theme %q isn't one of %s", c.badTheme, strings.Join(Themes, ", ")))
	}
	if c.badAt != "" {
		out = append(out, fmt.Sprintf("backup at %q should be a time like \"03:30\"", c.badAt))
	}
	if w := strings.TrimSpace(c.Notify.Weekly); w != "" && !validWeekly(w) {
		out = append(out, fmt.Sprintf("weekly %q should be a day and time, like \"sun 18:00\"", c.Notify.Weekly))
	}
	if c.Notify.QuietHours != "" {
		if _, _, ok := parseHours(c.Notify.QuietHours); !ok {
			out = append(out, fmt.Sprintf("quiet_hours %q should look like \"23:00-08:00\"", c.Notify.QuietHours))
		}
	}
	t := c.Thresholds
	for _, p := range []struct {
		name       string
		warn, crit float64
	}{{"disk", t.DiskWarn, t.DiskCrit}, {"memory", t.MemoryWarn, t.MemoryCrit}, {"load", t.LoadWarn, t.LoadCrit}} {
		if p.warn >= p.crit {
			out = append(out, fmt.Sprintf("the %s warning (%s) should be lower than the %s problem (%s)", p.name, trim(p.warn), p.name, trim(p.crit)))
		}
	}
	if (c.Notify.TelegramToken == "") != (c.Notify.TelegramChat == "") {
		out = append(out, "Telegram needs both telegram_token and telegram_chat")
	}
	return out
}

func trim(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func validWeekly(s string) bool {
	d, clock, ok := strings.Cut(strings.ToLower(s), " ")
	_, err := time.Parse("15:04", strings.TrimSpace(clock))
	return ok && err == nil && strings.Contains("sun mon tue wed thu fri sat", d) && len(d) == 3
}
