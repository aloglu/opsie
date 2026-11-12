// Package cli parses opsie's command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	"github.com/aloglu/opsie/internal/app"
	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/daemon"
	"github.com/aloglu/opsie/internal/motd"
	"github.com/aloglu/opsie/internal/priv"
	"github.com/aloglu/opsie/internal/tui"
	"github.com/aloglu/opsie/internal/update"
)

const usage = `opsie keeps an eye on this server and lets you fix what it finds.

Usage:
  opsie            open the dashboard
  opsie update     install the newest release and show what changed
  opsie version    print the version
  opsie paths      show where opsie keeps its settings and logs
  opsie motd       print a short summary of the server
                   --install  show it every time you log in
                   --remove   stop showing it
  opsie themes     show the color themes for config.toml
  opsie daemon     run the background checks (the dashboard installs
                   this as a service; you don't need to run it yourself)
  opsie help       show this help

Settings live in config.toml (see "opsie paths").
`

// Main runs opsie and returns its exit code.
func Main(args []string, version, channel string) int {
	// The helper speaks JSON on stdin/stdout and must not print anything
	// else, so it's handled before anything else is set up.
	if len(args) > 0 && args[0] == "helper" {
		if err := priv.Serve(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "opsie helper:", err)
			return 1
		}
		return 0
	}

	v := app.ResolveVersion(version)
	dev := app.IsDev(v, channel)
	env := &app.Env{
		Version:    v,
		Dev:        dev,
		Paths:      app.NewPaths(dev),
		Out:        os.Stdout,
		Err:        os.Stderr,
		IsTerminal: term.IsTerminal(os.Stdout.Fd()) && term.IsTerminal(os.Stdin.Fd()),
	}
	env.Config, env.ConfigErr = config.Load(env.Paths.ConfigFile())

	cmd, rest := "", []string(nil)
	if len(args) > 0 {
		cmd, rest = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "":
		err = runDashboard(env)
	case "update":
		err = tui.RunUpdate(env, len(rest) > 0 && rest[0] == "--from-app")
	case "installed":
		err = tui.PrintInstalled(env)
	case "version", "--version", "-v":
		fmt.Fprint(env.Out, "opsie ", v)
		if dev {
			fmt.Fprint(env.Out, " (development build)")
		}
		fmt.Fprintln(env.Out)
	case "paths":
		fmt.Fprintf(env.Out, "config    %s\nstate     %s\naudit log %s\nservice   %s\n", env.Paths.ConfigFile(), env.Paths.State, env.Paths.AuditLog(), env.Paths.Service)
	case "help", "-h", "--help":
		fmt.Fprint(env.Out, usage)
	case "preview":
		err = preview(env, rest)
	case "daemon":
		err = runDaemon(env, rest)
	case "motd", "--motd":
		err = runMotd(env, rest)
	case "themes":
		err = tui.PrintThemes(env)
	default:
		fmt.Fprintf(env.Err, "opsie: there's no %q command.\n\n%s", cmd, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(env.Err, "opsie:", err)
		return 1
	}
	return 0
}

func runDashboard(env *app.Env) error {
	if !env.IsTerminal {
		return errors.New("the dashboard needs a terminal; run opsie from an SSH session")
	}
	final, err := tea.NewProgram(tui.New(env)).Run()
	if err != nil {
		return err
	}
	if m, ok := final.(*tui.Model); ok && m.Reexec {
		// opsie just replaced its own binary: start the new one in place.
		exe, err := update.Executable()
		if err != nil {
			return err
		}
		return syscall.Exec(exe, []string{"opsie"}, os.Environ())
	}
	return nil
}

// preview prints one frame at a size: opsie preview 45x30 [help|detail].
func preview(env *app.Env, args []string) error {
	w, h := 45, 30
	if len(args) > 0 {
		ws, hs, ok := strings.Cut(args[0], "x")
		var err1, err2 error
		w, err1 = strconv.Atoi(ws)
		h, err2 = strconv.Atoi(hs)
		if !ok || err1 != nil || err2 != nil {
			return errors.New("size looks like 45x30")
		}
	}
	screen := ""
	if len(args) > 1 {
		screen = args[1]
	}
	fmt.Fprintln(env.Out, tui.Preview(env, w, h, screen))
	return nil
}

// runDaemon runs the background checks in the foreground until stopped.
func runDaemon(env *app.Env, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	configPath := fs.String("config", env.Paths.ConfigFile(), "config.toml to read")
	user := fs.String("user", os.Getenv("SUDO_USER"), "the account checks about \"you\" are for")
	state := fs.String("state", env.Paths.Service, "where to keep status and history")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return daemon.Run(ctx, daemon.Options{ConfigPath: *configPath, User: *user, StateDir: *state, Version: env.Version, Log: env.Out, Dev: env.Dev})
}

func runMotd(env *app.Env, args []string) error {
	if len(args) == 0 {
		return tui.PrintMotd(env)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	shell := os.Getenv("SHELL")
	switch args[0] {
	case "--install":
		if env.Dev {
			return errors.New("development builds don't add themselves to your login; install a release of opsie for that")
		}
		exe, err := update.Executable()
		if err != nil {
			return err
		}
		path, err := motd.Install(home, shell, exe)
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Out, "opsie's summary now shows when you log in (added to %s).\nTo stop it: opsie motd --remove\n", path)
		return nil
	case "--remove":
		path, had, err := motd.Remove(home, shell)
		if err != nil {
			return err
		}
		if !had {
			fmt.Fprintf(env.Out, "The summary wasn't set up in %s, so there's nothing to remove.\n", path)
			return nil
		}
		fmt.Fprintf(env.Out, "Removed opsie's summary from %s.\n", path)
		return nil
	}
	return fmt.Errorf("motd takes --install or --remove, not %q", args[0])
}
