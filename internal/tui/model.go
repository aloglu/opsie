// Package tui is opsie's terminal interface.
package tui

import (
	"context"
	"encoding/json"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aloglu/opsie/internal/app"
	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/facts"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/metrics"
	"github.com/aloglu/opsie/internal/priv"
	"github.com/aloglu/opsie/internal/update"
	"github.com/aloglu/opsie/internal/watch"
)

// historyLen is how many samples the charts remember; wider than any
// terminal so charts fill on big screens.
const historyLen = 300

type series []float64

func (s *series) push(v float64) {
	*s = append(*s, v)
	if len(*s) > historyLen {
		*s = append((*s)[:0], (*s)[len(*s)-historyLen:]...)
	}
}

// Model is the dashboard.
type Model struct {
	env *app.Env
	th  theme
	// ground is the terminal's background as it answered, dark or light.
	ground ground
	// du and clean are disk measurements, made when asked for.
	du    map[string]duEntry
	clean cleanState
	// hist is what the background checks recorded.
	hist histState
	// cron is what the crontabs hold, read with the Services tab.
	cron cronState
	// bk is what's in the backup folder.
	bk backupsState
	// audit is the action log, as last read.
	audit auditCache
	// net is the interfaces and speed tests; agents the AI agents' usage.
	net    netState
	agents agentsState

	width, height int
	now           time.Time

	collector *metrics.Collector
	snap      metrics.Snapshot
	haveSnap  bool
	cpu, mem  series
	rx, tx    series

	facts     facts.Facts
	haveFacts bool
	gathering bool
	nextFacts time.Time

	admin         *priv.Client
	adminErr      error
	adminStarting bool

	results []health.Result

	watcher     health.Watcher
	haveWatcher bool
	history     []watch.Event

	tab   tabID
	lists [tabCount]list
	// grow is how many rows the charts gain when the screen has height
	// to spare.
	grow int

	dock  dockerState
	svc   svcState
	procs procState
	logs  *logView

	modal      *modal
	help       bool
	helpScroll int

	flash flash

	updateVersion string
	// Reexec is set when opsie updated itself and should restart.
	Reexec bool

	hits []hit
	// spots are where the page's items were drawn, in page coordinates,
	// for moving between columns.
	spots []hit
}

type flashKind int

const (
	flashInfo flashKind = iota
	flashOK
	flashError
)

type flash struct {
	text  string
	kind  flashKind
	id    int
	until time.Time
}

// New builds the dashboard.
func New(env *app.Env) *Model {
	m := &Model{
		env:       env,
		now:       time.Now(),
		collector: metrics.NewCollector(),
	}
	m.applyTheme()
	for i := range m.lists {
		m.lists[i].follow = true
	}
	if env.ConfigErr != nil {
		m.setFlash("Config has an error, using defaults: "+env.ConfigErr.Error(), flashError, 10*time.Second)
	}
	if !env.Dev {
		if latest, newer := update.Cached(env.Paths.MetaFile(), env.Version); newer {
			m.updateVersion = latest
		}
	}
	return m
}

// Messages.
type (
	clockMsg    time.Time
	sampleMsg   metrics.Snapshot
	factsDueMsg time.Time
	factsMsg    facts.Facts
	adminMsg    struct {
		client *priv.Client
		err    error
	}
	fixDoneMsg struct {
		fix health.Fix
		out json.RawMessage
		err error
	}
	shellDoneMsg struct {
		container string
		err       error
	}
	unlockDoneMsg   struct{ err error }
	selfUpdatedMsg  struct{ err error }
	updateFoundMsg  string
	flashExpiredMsg int
	countdownMsg    int
)

func (m *Model) Init() tea.Cmd {
	m.adminStarting = true
	return tea.Batch(
		tea.RequestBackgroundColor,
		m.sampleNow(),
		startAdmin(),
		clockTick(),
		m.checkUpdate(),
	)
}

func clockTick() tea.Cmd {
	now := time.Now()
	return tea.Tick(now.Truncate(time.Second).Add(time.Second).Sub(now), func(t time.Time) tea.Msg { return clockMsg(t) })
}

func (m *Model) refreshEvery() time.Duration {
	return time.Duration(m.env.Config.RefreshSeconds * float64(time.Second))
}

func (m *Model) sampleNow() tea.Cmd {
	c := m.collector
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return sampleMsg(c.Collect(ctx))
	}
}

func (m *Model) sampleLater() tea.Cmd {
	c := m.collector
	return tea.Tick(m.refreshEvery(), func(time.Time) tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return sampleMsg(c.Collect(ctx))
	})
}

func startAdmin() tea.Cmd {
	return func() tea.Msg {
		client, err := priv.Start(context.Background())
		return adminMsg{client: client, err: err}
	}
}

// gather runs the health checks' slow readings now.
func (m *Model) gather() tea.Cmd {
	if m.gathering {
		return nil
	}
	m.gathering = true
	admin, adminErr, cfg := m.admin, m.adminErr, m.env.Config
	return tea.Batch(func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		f := facts.Gather(ctx, admin, adminErr)
		f.CheckOnline(ctx, cfg)
		return factsMsg(f)
	}, m.readWatcher())
}

func (m *Model) scheduleFacts() tea.Cmd {
	at := time.Now().Add(time.Duration(m.env.Config.CheckSeconds) * time.Second)
	m.nextFacts = at
	return tea.Tick(time.Until(at), func(time.Time) tea.Msg { return factsDueMsg(at) })
}

func (m *Model) checkUpdate() tea.Cmd {
	if m.env.Dev {
		return nil
	}
	env := m.env
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if latest, newer := update.Daily(ctx, env.Paths.MetaFile(), env.Version); newer {
			return updateFoundMsg(latest)
		}
		return nil
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		for i := range m.lists {
			m.lists[i].follow = true
		}
		return m, nil

	case tea.BackgroundColorMsg:
		m.ground = groundLight
		if msg.IsDark() {
			m.ground = groundDark
		}
		m.applyTheme()
		return m, nil

	case clockMsg:
		m.now = time.Time(msg)
		return m, clockTick()

	case sampleMsg:
		s := metrics.Snapshot(msg)
		m.snap, m.haveSnap = s, true
		m.cpu.push(s.CPU)
		m.mem.push(s.Mem.Percent)
		if s.Net.Ready {
			m.rx.push(s.Net.RxRate)
			m.tx.push(s.Net.TxRate)
		}
		m.evaluate()
		return m, m.sampleLater()

	case adminMsg:
		m.adminStarting = false
		if m.admin != nil {
			m.admin.Close()
		}
		m.admin, m.adminErr = msg.client, msg.err
		if msg.err == nil && m.haveFacts {
			m.setFlash("Admin access unlocked", flashOK, 3*time.Second)
		}
		m.gathering = false
		return m, tea.Batch(m.gather(), m.tabPoll())

	case factsDueMsg:
		if time.Time(msg).Equal(m.nextFacts) {
			if m.admin != nil && !m.admin.Alive() {
				m.admin, m.adminErr = nil, priv.ErrHelperGone
			}
			return m, m.gather()
		}
		return m, nil

	case watcherMsg:
		m.watcherUpdated(msg)
		return m, nil

	case factsMsg:
		m.facts, m.haveFacts, m.gathering = facts.Facts(msg), true, false
		m.evaluate()
		// The tabs that read files rather than poll keep up with the checks.
		var again tea.Cmd
		if m.tab == tabAgents || m.tab == tabNetwork {
			again = m.tabPoll()
		}
		return m, tea.Batch(m.scheduleFacts(), again)

	case histMsg:
		m.histUpdated(msg)
		return m, nil
	case duMsg:
		m.duUpdated(msg)
		return m, nil
	case cleanMsg:
		m.cleanUpdated(msg)
		return m, nil
	case cronMsg:
		m.cronUpdated(msg)
		return m, nil
	case backupsMsg:
		m.backupsUpdated(msg)
		return m, nil
	case netMsg:
		m.netUpdated(msg)
		return m, nil
	case speedMsg:
		return m, m.speedDone(msg)
	case agentsMsg:
		m.agentsUpdated(msg)
		m.syncLists()
		return m, nil
	case copyDoneMsg:
		return m, m.copyDone(msg)
	case dockerMsg:
		return m, m.dockerUpdated(msg)

	case dockerDueMsg:
		if int(msg) == m.dock.gen && m.tab == tabDocker {
			return m, m.pollDocker()
		}
		return m, nil

	case preflightMsg:
		st := preflightState{p: msg.p}
		if msg.err != nil {
			st.err = msg.err.Error()
		}
		m.dock.preflight[msg.project] = st
		return m, nil

	case servicesMsg:
		return m, m.servicesUpdated(msg)

	case servicesDueMsg:
		if int(msg) == m.svc.gen && m.wantsServices() {
			return m, m.pollServices()
		}
		return m, nil

	case procsMsg:
		return m, m.procsUpdated(msg)

	case procsDueMsg:
		if int(msg) == m.procs.gen && m.tab == tabProcs {
			return m, m.pollProcs()
		}
		return m, nil

	case logsMsg:
		return m, m.logsUpdated(msg)

	case logsDueMsg:
		if m.logs != nil && int(msg) == m.logs.gen {
			return m, m.fetchLogs()
		}
		return m, nil

	case shellDoneMsg:
		if msg.err != nil {
			return m, m.setFlash("Shell in "+msg.container+" ended: "+msg.err.Error(), flashError, 6*time.Second)
		}
		return m, nil

	case updateFoundMsg:
		m.updateVersion = string(msg)
		m.evaluate()
		return m, nil

	case fixDoneMsg:
		return m, m.fixDone(msg)

	case unlockDoneMsg:
		if msg.err != nil {
			return m, m.setFlash("Not unlocked: "+msg.err.Error(), flashError, 5*time.Second)
		}
		m.adminStarting = true
		return m, startAdmin()

	case selfUpdatedMsg:
		if msg.err != nil {
			return m, m.setFlash("Update didn't finish: "+msg.err.Error(), flashError, 8*time.Second)
		}
		m.Reexec = true
		return m, m.quit()

	case countdownMsg:
		return m, m.countdownTick(int(msg))

	case flashExpiredMsg:
		if int(msg) == m.flash.id {
			m.flash = flash{}
		}
		return m, nil

	case tea.KeyPressMsg:
		return m, m.handleKey(msg)

	case tea.MouseMsg:
		return m, m.handleMouse(msg)
	}
	// Anything else may be for the text field: pastes, cursor blinks.
	if m.modal != nil && m.modal.stage == stageInput {
		var cmd tea.Cmd
		m.modal.input, cmd = m.modal.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// evaluate reruns the checklist, keeping the same items selected.
func (m *Model) evaluate() {
	m.results = health.Evaluate(health.Readings{
		Snap: m.snap, HaveSnap: m.haveSnap,
		Facts: m.facts, HaveFacts: m.haveFacts,
		Thresholds:  m.env.Config.Thresholds,
		Sites:       m.env.Config.Sites,
		PublicPorts: m.env.Config.Docker.PublicPorts,
		Update:      m.updateVersion,
		Watcher:     m.watcher, HaveWatcher: m.haveWatcher,
	})
	m.syncLists()
}

func (m *Model) setFlash(text string, kind flashKind, d time.Duration) tea.Cmd {
	id := m.flash.id + 1
	m.flash = flash{text: text, kind: kind, id: id, until: time.Now().Add(d)}
	return tea.Tick(d, func(time.Time) tea.Msg { return flashExpiredMsg(id) })
}

func (m *Model) quit() tea.Cmd {
	if m.admin != nil {
		m.admin.Close()
	}
	return tea.Quit
}

// applyTheme picks the colors from the settings.
func (m *Model) applyTheme() {
	m.th = themeOn(m.env.Config, m.ground)
}

// themeOn is the settings' theme for a ground, unless the settings say
// which ground it is.
func themeOn(cfg config.Config, g ground) theme {
	switch cfg.Background {
	case "dark":
		g = groundDark
	case "light":
		g = groundLight
	}
	return themeNamed(cfg.Theme, g)
}

// themeFor is the theme for output that can't ask the terminal about its
// background, like the login summary.
func themeFor(cfg config.Config) theme { return themeOn(cfg, groundUnknown) }
