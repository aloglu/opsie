package tui

import (
	"fmt"
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
