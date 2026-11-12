package tui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/app"
	"github.com/aloglu/opsie/internal/update"
)

// PrintInstalled prints what the install script shows once opsie is in
// place: the logo, where it went, and what to do next, including how to
// fix PATH if the install directory isn't on it.
func PrintInstalled(env *app.Env) error {
	t := themeFor(env.Config)
	exe, err := update.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)

	var b strings.Builder
	b.WriteString("\n")
	for _, line := range t.bigWordmark() {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("\n  " + t.okText.Render("✓") + " " + t.bold.Render("opsie "+env.Version) + t.subtle.Render(" is installed in "+tildePath(dir)) + "\n\n")

	if !onPath(dir, os.Getenv("PATH")) {
		b.WriteString("  " + t.warnText.Render("One more step: ") + tildePath(dir) + " isn't on your PATH yet. Run:\n\n")
		b.WriteString("      " + t.key.Render(pathFixCommand(dir, os.Getenv("SHELL"))) + "\n\n")
		b.WriteString("  " + t.subtle.Render("then log in again.") + "\n\n")
	}

	row := func(label, command string) {
		b.WriteString("  " + t.dim.Render(fmt.Sprintf("%-14s", label)) + t.key.Render(command) + "\n")
	}
	row("Open it", "opsie")
	row("All commands", "opsie help")
	row("Update later", "opsie update")
	b.WriteString("\n")
	_, err = lipgloss.Fprint(env.Out, b.String())
	return err
}

func onPath(dir, path string) bool {
	for _, p := range filepath.SplitList(path) {
		if filepath.Clean(p) == filepath.Clean(dir) {
			return true
		}
	}
	return false
}

func pathFixCommand(dir, shell string) string {
	display := strings.Replace(tildePath(dir), "~", "$HOME", 1)
	switch filepath.Base(shell) {
	case "fish":
		return "fish_add_path " + display
	case "zsh":
		return `echo 'export PATH="` + display + `:$PATH"' >> ~/.zshrc`
	}
	return `echo 'export PATH="` + display + `:$PATH"' >> ~/.bashrc`
}

// Voice is how opsie's commands outside the dashboard (install, update,
// uninstall) look, so they read as one program: the wordmark, then lines
// in the theme's colors.
type Voice struct {
	t   theme
	out io.Writer
}

// NewVoice writes to the command's output in the configured theme.
func NewVoice(env *app.Env) Voice { return Voice{t: themeFor(env.Config), out: env.Out} }

// Say prints text, dropping colors when the output isn't a terminal.
func (v Voice) Say(text string) { _, _ = lipgloss.Fprint(v.out, text) }

// Wordmark prints the logo the installer and updater open with.
func (v Voice) Wordmark() {
	var b strings.Builder
	b.WriteString("\n")
	for _, line := range v.t.bigWordmark() {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("\n")
	v.Say(b.String())
}

func (v Voice) OK(s string) string     { return v.t.okText.Render(s) }
func (v Voice) Fail(s string) string   { return v.t.dangerText.Render(s) }
func (v Voice) Warn(s string) string   { return v.t.warnText.Render(s) }
func (v Voice) Bold(s string) string   { return v.t.bold.Render(s) }
func (v Voice) Subtle(s string) string { return v.t.subtle.Render(s) }
func (v Voice) Dim(s string) string    { return v.t.dim.Render(s) }
func (v Voice) Key(s string) string    { return v.t.key.Render(s) }
func (v Voice) Bullet() string         { return v.t.navText.Render("•") }
