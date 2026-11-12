package tui

import (
	tea "charm.land/bubbletea/v2"
)

// hit is a clickable rectangle, recorded while rendering so taps always
// land on what's drawn.
type hit struct {
	x, y, w, h int
	click      func(m *Model) tea.Cmd
	// id is set when the hit is a selectable item of the page, so the
	// arrow keys can find what's beside it.
	id string
}

func (h hit) contains(x, y int) bool {
	return x >= h.x && x < h.x+h.w && y >= h.y && y < h.y+h.h
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if key == "ctrl+c" {
		return m.quit()
	}
	if m.help {
		switch key {
		case "up", "k":
			m.helpScroll--
		case "down", "j":
			m.helpScroll++
		default:
			m.help, m.helpScroll = false, 0
		}
		return nil
	}
	if m.modal != nil {
		if m.modal.stage == stageInput {
			return m.inputKey(msg)
		}
		return m.modalKey(key)
	}
	page := max(m.bodyHeight()-2, 1)
	if m.logs != nil {
		return m.logsKey(key, page)
	}
	l := &m.lists[m.tab]
	switch key {
	case "q":
		return m.quit()
	case "?":
		m.help = true
	case "up", "k":
		m.moveAlong(-1)
	case "down", "j":
		m.moveAlong(1)
	case "home", "g":
		m.move(-1 << 30)
		l.scroll = 0
	case "end", "G":
		m.move(1 << 30)
	case "pgup", "b":
		l.scroll -= page
	case "pgdown", "f", "space":
		l.scroll += page
	case "enter":
		return m.openSelected()
	case "left", "h":
		m.moveAcross(-1)
	case "right", "l":
		m.moveAcross(1)
	case "r":
		return m.refresh()
	case "s":
		if m.tab == tabProcs {
			m.setProcSort(!m.procs.byMem)
		}
	case "tab":
		return m.switchTab((m.tab + 1) % tabCount)
	case "shift+tab":
		return m.switchTab((m.tab + tabCount - 1) % tabCount)
	default:
		for i, tb := range tabs {
			if key == tabKey(i) {
				return m.switchTab(tb.id)
			}
		}
	}
	return nil
}

func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	mouse := msg.Mouse()
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return nil
		}
		if m.help {
			m.help = false
			return nil
		}
		for i := len(m.hits) - 1; i >= 0; i-- {
			if h := m.hits[i]; h.contains(mouse.X, mouse.Y) && h.click != nil {
				return h.click(m)
			}
		}
	case tea.MouseWheelMsg:
		delta := 0
		switch msg.Button {
		case tea.MouseWheelUp:
			delta = -2
		case tea.MouseWheelDown:
			delta = 2
		default:
			return nil
		}
		switch {
		case m.help:
			m.helpScroll += delta
		case m.modal != nil:
			m.modal.scroll += delta
		case m.logs != nil:
			key := "down"
			if delta < 0 {
				key = "up"
			}
			for range 3 {
				m.logsKey(key, 0)
			}
		default:
			m.lists[m.tab].scroll += delta
		}
	}
	return nil
}
