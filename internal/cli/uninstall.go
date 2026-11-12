package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aloglu/opsie/internal/app"
	"github.com/aloglu/opsie/internal/motd"
	"github.com/aloglu/opsie/internal/priv"
	"github.com/aloglu/opsie/internal/tui"
	"github.com/aloglu/opsie/internal/update"
)

// uninstaller removes opsie from a server. Its fields are where things
// are and how to reach root, so a test can point it somewhere harmless.
type uninstaller struct {
	// say prints a line; look colors its parts, like the installer does.
	say          func(string)
	look         look
	home, shell  string
	exe          string
	paths        app.Paths
	keepSettings bool

	serviceInstalled func() bool
	// removeService turns the background service off and deletes what it
	// kept, as root.
	removeService func(ctx context.Context) error
	// leftovers are server settings that were changed through opsie and
	// stay as they are.
	leftovers []string
}

// look is the handful of styles the uninstall is drawn with. The zero
// value leaves text plain.
type look struct {
	ok, fail, bold, subtle, dim, key func(string) string
	bullet                           string
}

func (l look) or() look {
	plain := func(s string) string { return s }
	for _, f := range []*func(string) string{&l.ok, &l.fail, &l.bold, &l.subtle, &l.dim, &l.key} {
		if *f == nil {
			*f = plain
		}
	}
	if l.bullet == "" {
		l.bullet = "•"
	}
	return l
}

// step is one thing uninstalling does, said before and done after.
type step struct {
	what string
	do   func(ctx context.Context) error
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func tilde(home, path string) string {
	if home != "" && strings.HasPrefix(path, home+"/") {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// plan lists what there is to remove, in the order it's removed: the
// things that need the binary first, the binary last.
func (u *uninstaller) plan() (steps []step, kept []string) {
	if motd.Installed(u.home, u.shell) {
		steps = append(steps, step{"the login summary in " + tilde(u.home, motd.File(u.home, u.shell)), func(context.Context) error {
			_, _, err := motd.Remove(u.home, u.shell)
			return err
		}})
	}
	if u.serviceInstalled() {
		steps = append(steps, step{"the background checks service and its history (needs sudo)", u.removeService})
	}
	key := u.paths.BackupKey()
	hasBackups := exists(u.paths.Backups)
	switch {
	case u.keepSettings:
		kept = append(kept, "your settings and history in "+tilde(u.home, u.paths.Config)+" and "+tilde(u.home, u.paths.State))
	default:
		if exists(u.paths.Config) {
			what := "your settings in " + tilde(u.home, u.paths.Config)
			// Without its key an encrypted backup can't ever be opened,
			// so the key outlives the settings.
			keepKey := exists(key)
			if keepKey {
				what += " (except the backup key)"
				kept = append(kept, "the key that opens your encrypted backups, "+tilde(u.home, key))
			}
			steps = append(steps, step{what, func(context.Context) error { return removeAllBut(u.paths.Config, key, keepKey) }})
		}
		if exists(u.paths.State) {
			steps = append(steps, step{"its history and audit log in " + tilde(u.home, u.paths.State), func(context.Context) error { return os.RemoveAll(u.paths.State) }})
		}
	}
	if hasBackups {
		kept = append(kept, "your app backups in "+tilde(u.home, u.paths.Backups))
	}
	kept = append(kept, u.leftovers...)
	steps = append(steps, step{"opsie itself, " + tilde(u.home, u.exe), func(context.Context) error { return os.Remove(u.exe) }})
	return steps, kept
}

// removeAllBut deletes a folder, sparing one file in it when asked.
func removeAllBut(dir, spare string, keep bool) error {
	if !keep {
		return os.RemoveAll(dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if p == spare {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

// run removes everything in the plan, carrying on past a step that fails
// so one problem doesn't leave the rest behind. The binary only goes if
// everything before it did, so the command can be run again.
func (u *uninstaller) run(ctx context.Context, steps []step) error {
	failed := 0
	l := u.look.or()
	for i, s := range steps {
		last := i == len(steps)-1
		if last && failed > 0 {
			u.say("  " + l.dim("·") + l.subtle(" kept "+strings.TrimPrefix(s.what, "opsie itself, ")+" so you can try again") + "\n")
			break
		}
		if err := s.do(ctx); err != nil {
			failed++
			u.say("  " + l.fail("✗") + " " + s.what + l.fail(": "+err.Error()) + "\n")
			continue
		}
		u.say("  " + l.ok("✓") + l.subtle(" removed ") + s.what + "\n")
	}
	if failed > 0 {
		return errors.New("some things couldn't be removed; fix what's above and run opsie uninstall again")
	}
	return nil
}

// serverLeftovers are the server's own settings that opsie wrote when
// asked to. They're the server's now: removing them would quietly turn
// password logins back on or change who can use sudo.
func serverLeftovers() []string {
	var out []string
	if exists("/etc/ssh/sshd_config.d/0-opsie.conf") {
		out = append(out, "the SSH login settings you chose, /etc/ssh/sshd_config.d/0-opsie.conf")
	}
	if m, _ := filepath.Glob("/etc/sudoers.d/*opsie*"); len(m) > 0 {
		out = append(out, "the sudo settings you chose, "+strings.Join(m, " and "))
	}
	return out
}

// removeServiceAsRoot asks the helper, getting sudo ready first if it
// wants a password.
func removeServiceAsRoot(interactive bool) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		c, err := priv.Start(ctx)
		if errors.Is(err, priv.ErrNeedsPassword) && interactive {
			unlock := exec.CommandContext(ctx, "sudo", "-v", "-p", "  opsie needs your sudo password to remove the service: ")
			unlock.Stdin, unlock.Stdout, unlock.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := unlock.Run(); err != nil {
				return errors.New("sudo didn't accept the password")
			}
			c, err = priv.Start(ctx)
		}
		if err != nil {
			return err
		}
		defer c.Close()
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		return c.Call(cctx, "service.remove", map[string]string{"purge": "1"}, nil)
	}
}

func runUninstall(env *app.Env, args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	yes := fs.Bool("yes", false, "don't ask first")
	keep := fs.Bool("keep-settings", false, "leave settings and history")
	if err := fs.Parse(args); err != nil {
		return errors.New("uninstall takes --yes and --keep-settings")
	}
	if env.Dev {
		return errors.New("this is a development build, which was never installed: delete the checkout, and " + env.Paths.Config + " if you like")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	exe, err := update.Executable()
	if err != nil {
		return err
	}
	v := tui.NewVoice(env)
	l := look{ok: v.OK, fail: v.Fail, bold: v.Bold, subtle: v.Subtle, dim: v.Dim, key: v.Key, bullet: v.Bullet()}
	u := &uninstaller{
		say: v.Say, look: l, home: home, shell: os.Getenv("SHELL"), exe: exe, paths: env.Paths, keepSettings: *keep,
		serviceInstalled: priv.ServiceInstalled, removeService: removeServiceAsRoot(env.IsTerminal),
		leftovers: serverLeftovers(),
	}
	steps, kept := u.plan()

	v.Wordmark()
	v.Say("  " + l.bold("Uninstalling opsie "+env.Version) + l.subtle(" removes:") + "\n")
	for _, s := range steps {
		v.Say("    " + l.bullet + " " + s.what + "\n")
	}
	if len(kept) > 0 {
		v.Say("\n  " + l.subtle("It leaves alone:") + "\n")
		for _, k := range kept {
			v.Say("    " + l.dim("·") + " " + l.subtle(k) + "\n")
		}
	}
	v.Say("\n")
	if !*yes {
		if !env.IsTerminal {
			return errors.New("to uninstall without being asked, run opsie uninstall --yes")
		}
		v.Say("  " + l.bold("Remove opsie?") + " " + l.key("y") + l.subtle(" yes  ") + l.key("N") + l.subtle(" no  "))
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			v.Say("\n  " + l.subtle("Nothing was removed.") + "\n\n")
			return nil
		}
		v.Say("\n")
	}
	if err := u.run(context.Background(), steps); err != nil {
		v.Say("\n")
		return err
	}
	v.Say("\n  " + l.ok("✓") + " " + l.bold("opsie is gone.") + l.subtle(" To bring it back:") + "\n\n")
	v.Say("      " + l.key("curl -fsSL https://raw.githubusercontent.com/"+app.Repo+"/main/install.sh | sh") + "\n\n")
	return nil
}
