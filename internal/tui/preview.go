package tui

import (
	"context"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aloglu/opsie/internal/app"
	"github.com/aloglu/opsie/internal/facts"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/priv"
)

// Preview renders one frame of the dashboard at a given size with live
// data, for checking layouts without resizing a terminal. It backs the
// hidden `opsie preview WxH` command.
func Preview(env *app.Env, width, height int, screen string) string {
	m := New(env)
	// A preview can't ask a terminal about its background, so it's told.
	switch os.Getenv("OPSIE_GROUND") {
	case "dark":
		m.ground = groundDark
	case "light":
		m.ground = groundLight
	}
	m.applyTheme()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m.Update(sampleMsg(m.collector.Collect(ctx)))
	admin, err := priv.Start(ctx)
	if admin != nil {
		defer admin.Close()
	}
	m.admin, m.adminErr = admin, err
	// A second sample gives CPU and network rates something to compare.
	time.Sleep(time.Second)
	m.Update(sampleMsg(m.collector.Collect(ctx)))
	f := facts.Gather(ctx, admin, err)
	f.CheckOnline(ctx, env.Config)
	m.Update(factsMsg(f))
	m.Update(m.readWatcher()())
	m.width, m.height = width, height

	// run executes a command and feeds its message back, like the program
	// loop would.
	run := func(cmd func() tea.Cmd) {
		if c := cmd(); c != nil {
			if msg := c(); msg != nil {
				m.Update(msg)
			}
		}
	}
	// runAll is run for commands that batch others.
	var runAll func(c tea.Cmd)
	runAll = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			for _, c := range msg {
				runAll(c)
			}
		case nil:
		default:
			m.Update(msg)
		}
	}
	name, arg, _ := strings.Cut(screen, ":")
	switch name {
	case "explore":
		if arg == "" {
			arg = "/"
		}
		runAll(m.explore(arg, false))
	case "clean":
		runAll(m.runFix(health.Fix{Special: "clean"}))
	case "help":
		m.help = true
	case "detail":
		if arg != "" {
			m.openItem(tabOverview, arg)
		} else {
			m.openSelected()
		}
	case "security", "updates", "services", "procs", "settings", "storage", "network", "agents":
		m.tab = map[string]tabID{"security": tabSecurity, "updates": tabUpdates, "services": tabServices, "procs": tabProcs, "settings": tabSettings,
			"storage": tabStorage, "network": tabNetwork, "agents": tabAgents}[name]
		if m.tab == tabStorage || m.tab == tabNetwork || m.tab == tabAgents {
			runAll(m.tabPoll())
		}
		if m.tab == tabServices || strings.HasPrefix(arg, "unit:") {
			runAll(m.pollServices())
		}
		if m.tab == tabProcs {
			run(m.pollProcs)
		}
		if arg == "logs" {
			m.tab = tabServices
		}
		if strings.HasPrefix(arg, "logs:") {
			run(func() tea.Cmd { return m.openUnitLogs(strings.TrimPrefix(arg, "logs:")) })
		} else if arg != "" {
			m.openItem(m.tab, arg)
		}
	case "backup":
		m.tab = tabDocker
		run(m.pollDocker)
		runAll(m.runFix(health.Fix{Special: "backup:" + arg}))
	case "docker", "container", "project", "prune", "logs":
		m.tab = tabDocker
		m.dock.dfAt = time.Time{}
		run(m.pollDocker)
		run(m.pollDocker) // a second reading gives network speeds
		switch name {
		case "container":
			m.openItem(tabDocker, "ctr:"+arg)
		case "project":
			m.openItem(tabDocker, "proj:"+arg)
			run(func() tea.Cmd { return m.preflight(arg) })
		case "prune":
			m.openItem(tabDocker, "prune")
		case "logs":
			run(func() tea.Cmd { return m.openLogs(arg) })
		case "docker":
			if arg != "" {
				m.openItem(tabDocker, arg)
			}
		}
	}
	return m.render()
}
