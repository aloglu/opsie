package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/app"
	"github.com/aloglu/opsie/internal/priv"
	"github.com/aloglu/opsie/internal/update"
)

// How much of the changelog `opsie update` shows.
const (
	maxReleasesShown = 5
	maxNotesPerRel   = 6
)

type updatePhase int

const (
	phaseChecking updatePhase = iota
	phaseInstalling
	phaseDone
	phaseUpToDate
	phaseFailed
)

type (
	pendingMsg struct {
		pending []update.Release
		err     error
	}
	installedMsg struct {
		err     error
		service string
	}
)

// updater is the small program behind `opsie update`.
type updater struct {
	env     *app.Env
	th      theme
	spin    spinner.Model
	phase   updatePhase
	pending []update.Release
	target  string
	err     error
	// service says what happened to the background service, if any.
	service string
	width   int
	// fromApp means the dashboard started this update and restarts once
	// it's done, so wait for a key before handing back.
	fromApp bool
}

// RunUpdate implements `opsie update`.
func RunUpdate(env *app.Env, fromApp bool) error {
	if env.Dev {
		fmt.Fprintf(env.Out, "This is a development build (%s); it doesn't update itself.\nBuild a new one with: git pull && make build\n", env.Version)
		return nil
	}
	target, err := update.Executable()
	if err != nil {
		return err
	}
	if !env.IsTerminal {
		return runUpdatePlain(env, env.Out, target)
	}
	u := &updater{
		env: env, th: themeFor(env.Config), target: target, fromApp: fromApp, width: 80,
		spin: spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
	final, err := tea.NewProgram(u).Run()
	if err != nil {
		return err
	}
	if u, ok := final.(*updater); ok && u.phase == phaseFailed {
		return u.err
	}
	return nil
}

func runUpdatePlain(env *app.Env, out io.Writer, target string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	releases, err := update.Releases(ctx)
	if err != nil {
		return err
	}
	pending := update.Pending(env.Version, releases)
	if len(pending) == 0 {
		fmt.Fprintf(out, "opsie %s is up to date.\n", env.Version)
		return nil
	}
	fmt.Fprintf(out, "opsie %s → %s\n\n", env.Version, pending[0].Tag)
	for _, r := range shownReleases(pending) {
		fmt.Fprintln(out, r.Tag)
		for _, note := range update.Highlights(r.Body, maxNotesPerRel) {
			fmt.Fprintln(out, "  • "+note)
		}
	}
	if err := update.InstallRelease(ctx, pending[0].Tag, target); err != nil {
		return err
	}
	update.Forget(env.Paths.MetaFile(), pending[0].Tag)
	fmt.Fprintf(out, "\nUpdated to %s.\n", pending[0].Tag)
	if note := refreshService(ctx, target, env.Paths.ConfigFile()); note != "" {
		fmt.Fprintln(out, note)
	}
	return nil
}

func shownReleases(pending []update.Release) []update.Release {
	if len(pending) > maxReleasesShown {
		return pending[:maxReleasesShown]
	}
	return pending
}

func (u *updater) Init() tea.Cmd {
	return tea.Batch(u.spin.Tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		releases, err := update.Releases(ctx)
		return pendingMsg{pending: update.Pending(u.env.Version, releases), err: err}
	})
}

func (u *updater) finish() tea.Cmd {
	if u.fromApp && u.phase != phaseFailed {
		return nil // wait for a key
	}
	return tea.Quit
}

func (u *updater) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		u.width = msg.Width
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" || u.phase >= phaseDone {
			if u.phase == phaseChecking || u.phase == phaseInstalling {
				u.phase, u.err = phaseFailed, fmt.Errorf("cancelled")
			}
			return u, tea.Quit
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		u.spin, cmd = u.spin.Update(msg)
		return u, cmd
	case pendingMsg:
		switch {
		case msg.err != nil:
			u.phase, u.err = phaseFailed, msg.err
			return u, tea.Quit
		case len(msg.pending) == 0:
			u.phase = phaseUpToDate
			return u, u.finish()
		}
		u.pending, u.phase = msg.pending, phaseInstalling
		tag, target, cfg := msg.pending[0].Tag, u.target, u.env.Paths.ConfigFile()
		return u, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := update.InstallRelease(ctx, tag, target); err != nil {
				return installedMsg{err: err}
			}
			return installedMsg{service: refreshService(ctx, target, cfg)}
		}
	case installedMsg:
		if msg.err != nil {
			u.phase, u.err = phaseFailed, msg.err
			return u, tea.Quit
		}
		update.Forget(u.env.Paths.MetaFile(), u.pending[0].Tag)
		u.phase, u.service = phaseDone, msg.service
		return u, u.finish()
	}
	return u, nil
}

func (u *updater) View() tea.View {
	t := u.th
	w := min(max(u.width, 30), 90)
	var b strings.Builder
	b.WriteString("\n")
	for _, line := range t.bigWordmark() {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("\n")

	switch u.phase {
	case phaseChecking:
		b.WriteString("  " + u.spin.View() + t.subtle.Render(" Checking for a new version…") + "\n")
	case phaseUpToDate:
		b.WriteString("  " + t.okText.Render("✓") + " opsie " + t.bold.Render(u.env.Version) + " is the latest version.\n")
	case phaseFailed:
		b.WriteString("  " + t.dangerText.Render("✗ Couldn't update: ") + u.err.Error() + "\n")
	}

	if len(u.pending) > 0 {
		newest := u.pending[0]
		b.WriteString("  " + t.subtle.Render(u.env.Version) + t.dim.Render(" → ") + t.gradient(newest.Tag, true))
		if n := len(u.pending); n > 1 {
			b.WriteString(t.dim.Render(fmt.Sprintf("  (%d releases)", n)))
		}
		b.WriteString("\n")
		for _, r := range shownReleases(u.pending) {
			b.WriteString("\n  " + t.bold.Render(r.Tag))
			if !r.Published.IsZero() {
				b.WriteString(t.dim.Render("  " + r.Published.Format("2 Jan 2006")))
			}
			b.WriteString("\n")
			notes := update.Highlights(r.Body, maxNotesPerRel)
			if len(notes) == 0 {
				notes = []string{"Fixes and improvements."}
			}
			for _, note := range notes {
				lines := wrap(note, w-6)
				for i, line := range lines {
					bullet := "    "
					if i == 0 {
						bullet = "  " + t.navText.Render("•") + " "
					}
					b.WriteString("  " + bullet + line + "\n")
				}
			}
		}
		if extra := len(u.pending) - maxReleasesShown; extra > 0 {
			b.WriteString("\n  " + t.dim.Render(fmt.Sprintf("…and %d earlier releases.", extra)) + "\n")
		}
		b.WriteString("\n")
		switch u.phase {
		case phaseInstalling:
			b.WriteString("  " + u.spin.View() + t.subtle.Render(" Installing "+newest.Tag+"…") + "\n")
		case phaseDone:
			b.WriteString("  " + t.okText.Render("✓") + " Updated to " + t.bold.Render(newest.Tag) + t.subtle.Render(" in "+tildePath(filepath.Dir(u.target))) + "\n")
			if u.service != "" {
				b.WriteString("  " + t.subtle.Render(u.service) + "\n")
			}
			b.WriteString("  " + t.dim.Render("All release notes:") + "\n")
			b.WriteString("  " + t.dim.Render("github.com/"+app.Repo+"/releases") + "\n")
		}
	}

	if u.fromApp && (u.phase == phaseDone || u.phase == phaseUpToDate) {
		b.WriteString("\n  " + t.key.Render("Press any key") + t.subtle.Render(" to go back to opsie.") + "\n")
	}
	b.WriteString("\n")
	return tea.NewView(lipgloss.NewStyle().MaxWidth(max(u.width, 30)).Render(b.String()))
}

// tildePath shortens a path under the home directory to ~/….
func tildePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~/" + rest
	}
	return path
}

// refreshService brings the background service up to the binary just
// installed, if it's set up. It only works when sudo needs no password;
// otherwise the dashboard offers it next time.
func refreshService(ctx context.Context, exe, configPath string) string {
	if !priv.ServiceInstalled() {
		return ""
	}
	admin, err := priv.StartExe(ctx, exe)
	if err != nil {
		return "Background checks still run the old version; open opsie to update them."
	}
	defer admin.Close()
	if err := admin.Call(ctx, "service.refresh", map[string]string{"config": configPath}, nil); err != nil {
		return "Couldn't update the background checks: " + err.Error()
	}
	return "Background checks updated too."
}
