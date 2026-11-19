package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const doubleClickWindow = 450 * time.Millisecond

type clickState struct {
	tab Tab
	row int
	at  time.Time
}

// mouseCell translates incoming mouse coordinates to 0-based frame cells.
// When Kitty pixel mouse (SGR 1016) is active, Bubble Tea delivers pixel
// coordinates, so divide by the real cell metrics.
func (m Model) mouseCell(x, y int) (int, int) {
	if m.gfx != nil && m.gfx.mouse && m.gfx.cell.Valid() {
		if x < 0 || y < 0 {
			return -1, -1
		}
		return x / m.gfx.cell.CellW, y / m.gfx.cell.CellH
	}
	return x, y
}

func (m Model) inGraphRegion(cx, cy int) bool {
	r := m.GraphRegion()
	return r.W > 0 && r.H > 0 && cx >= r.X && cx < r.X+r.W && cy >= r.Y && cy < r.Y+r.H
}

func (m Model) mouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if m.paused || !m.framed() {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseWheelUp:
		if m.tab == TabGraph && !m.graphUsesTUI() {
			m.graphKey("[")
		} else {
			m.move(-1)
		}
	case tea.MouseWheelDown:
		if m.tab == TabGraph && !m.graphUsesTUI() {
			m.graphKey("]")
		} else {
			m.move(1)
		}
	}
	return m, nil
}

func (m Model) clickableList() (es []entry, top, height int) {
	switch m.tab {
	case TabEpics:
		return m.epicsEntries(), borderWidth + headerLines, m.contentHeight()
	case TabOverview:
		return m.overviewEntries(), borderWidth + headerLines, m.contentHeight()
	case TabMembers:
		return m.memberEntries(), borderWidth + headerLines, m.contentHeight()
	case TabMail:
		return m.mailEntries(), borderWidth + headerLines + 1, max(m.contentHeight()-1, 0)
	case TabGraph:
		if m.graphUsesTUI() {
			r := m.GraphRegion()
			return m.graphTUIEntries(), r.Y, r.H
		}
	}
	return nil, 0, 0
}

func (m Model) mouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if m.paused || !m.framed() || msg.Button != tea.MouseLeft {
		return m, nil
	}
	cx, cy := m.mouseCell(msg.X, msg.Y)
	// When Kitty pixel mouse owns GraphRegion, let graphics() hit-test nodes
	// with full pixel precision. Clicks outside GraphRegion (such as the tab
	// bar) are handled below in cell coordinates.
	if m.tab == TabGraph && m.gfx != nil && m.gfx.mouse && m.gfx.tr.Shown() && m.inGraphRegion(cx, cy) {
		return m, nil
	}
	m.hint, m.hintBad = "", false
	if cy == borderWidth+1 {
		if t, ok := m.tabAt(cx); ok {
			m.mail.typing = false
			m.tab, m.tabSet = t, true
			m.lastClick = clickState{}
		}
		return m, nil
	}
	if !m.st.Ready() || cx < borderWidth || cx >= m.w-borderWidth {
		m.lastClick = clickState{}
		return m, nil
	}
	es, top, height := m.clickableList()
	if height <= 0 || len(es) == 0 || cy < top || cy >= top+height {
		m.lastClick = clickState{}
		return m, nil
	}
	start := m.viewStart(m.tab, es, height)
	idx := start + (cy - top)
	if idx < 0 || idx >= len(es) || es[idx].row < 0 {
		m.lastClick = clickState{}
		return m, nil
	}
	row := es[idx].row
	if m.tab == TabMail {
		m.mail.typing = false
		if row < m.sel[TabMail] {
			m.unfollow()
		}
		if row != m.sel[TabMail] {
			m.mail.cycleID = ""
		}
	}
	if row != m.sel[m.tab] {
		m.sel[m.tab] = row
		m.scroll[m.tab] = 0
	}
	if m.tab == TabGraph && m.graphUsesTUI() {
		if h := m.selectedGraphHandle(); h != "" {
			for i, a := range m.members() {
				if a.Handle == h {
					m.sel[TabMembers] = i
					break
				}
			}
		}
	}
	now := m.now()
	if !m.lastClick.at.IsZero() && m.lastClick.tab == m.tab && m.lastClick.row == row &&
		now.Sub(m.lastClick.at) >= 0 && now.Sub(m.lastClick.at) <= doubleClickWindow {
		m.clearPin(m.tab)
		m.lastClick = clickState{}
		return m.enter()
	}
	m.setPin(m.tab, start)
	m.lastClick = clickState{tab: m.tab, row: row, at: now}
	return m, nil
}
