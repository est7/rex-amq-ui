package ui

import (
	"cmp"
	"context"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/est7/rex-amq-ui/internal/adapter"
)

// key handles a key press: the search box first while it is open, then
// the keys every tab shares, then the current tab's.
func (m Model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	var ask tea.Cmd
	if m.paused {
		// The Lua plugin only writes APC sequences, so a key comes from a
		// human looking at the panel: it shows, though no resume came.
		m.resume()
		ask = m.askColours()
	}
	m.hint, m.hintBad = "", false
	m.lastClick = clickState{}
	if ask != nil {
		next, cmd := m.key(k)
		return next, tea.Batch(ask, cmd)
	}
	if m.tab == TabMail && m.mail.typing {
		return m.searchKey(k), nil
	}
	switch s {
	case "Q", "ctrl+c":
		return m, tea.Quit
	case "tab":
		m.cycle(1)
		return m, nil
	case "shift+tab":
		m.cycle(-1)
		return m, nil
	case "H":
		m.cycle(-1)
		return m, nil
	case "L":
		if m.tab == TabGraph && m.graphAt != "" {
			m.graphAt = ""
			return m, nil
		}
		m.cycle(1)
		return m, nil
	case "[":
		if m.tab != TabGraph {
			m.cycle(-1)
			return m, nil
		}
	case "]":
		if m.tab != TabGraph {
			m.cycle(1)
			return m, nil
		}
	case "alt+1", "f1":
		m.selectTabIndex(0)
		return m, nil
	case "alt+2", "f2":
		m.selectTabIndex(1)
		return m, nil
	case "alt+3", "f3":
		m.selectTabIndex(2)
		return m, nil
	case "alt+4", "f4":
		m.selectTabIndex(3)
		return m, nil
	case "alt+5", "f5":
		m.selectTabIndex(4)
		return m, nil
	case "z":
		return m, m.zoom()
	case "j", "down":
		m.move(1)
		return m, nil
	case "k", "up":
		m.move(-1)
		return m, nil
	case "g", "home":
		if m.tab == TabMail && m.sel[TabMail] != 0 {
			m.mail.cycleID = ""
		}
		m.sel[m.tab] = 0
		m.scroll[m.tab] = 0
		m.clearPin(m.tab)
		m.unfollow()
		return m, nil
	case "G", "end":
		last := max(m.rowCount(m.tab)-1, 0)
		if m.tab == TabMail && m.sel[TabMail] != last {
			m.mail.cycleID = ""
		}
		m.sel[m.tab] = last
		m.scroll[m.tab] = m.maxScroll(m.tab, last)
		m.clearPin(m.tab)
		return m, nil
	case "esc", "q":
		if m.stepBack() {
			return m, nil
		}
		if m.DrilledDown() {
			return m.popBack()
		}
		return m, m.minimize()
	case "backspace":
		if m.stepBack() {
			return m, nil
		}
		if m.DrilledDown() {
			return m.popBack()
		}
		return m, nil
	case "s":
		if m.DrilledDown() {
			return m, m.switchToEpic(m.drill.epic)
		}
		if m.tab == TabEpics {
			if e, ok := m.selectedEpic(); ok {
				return m, m.switchToEpic(e)
			}
		}
		return m, nil
	case "enter":
		return m.enter()
	}
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' && (m.tab == TabMembers || m.tab == TabEpics) {
		if i := int(s[0] - '1'); i < m.rowCount(m.tab) {
			m.sel[m.tab] = i
			m.scroll[m.tab] = 0
			m.clearPin(m.tab)
		}
		return m, nil
	}
	switch m.tab {
	case TabEpics:
		if s == "o" {
			return m, m.openEpicHandBack()
		}
	case TabOverview:
		if s == "o" {
			return m, m.openHandBack()
		}
	case TabMembers:
		if s == "r" {
			m.retired = !m.retired
			m.sel[TabMembers] = clampSel(m.sel[TabMembers], m.rowCount(TabMembers))
			m.clearPin(TabMembers)
		}
	case TabMail:
		return m.mailKey(s)
	case TabGraph:
		m.graphKey(s)
	}
	return m, nil
}

func (m *Model) cycle(d int) {
	tabs := m.tabs()
	i := 0
	for j, t := range tabs {
		if t == m.tab {
			i = j
		}
	}
	m.tab, m.tabSet = tabs[(i+d+len(tabs))%len(tabs)], true
}

func (m *Model) selectTabIndex(idx int) {
	tabs := m.tabs()
	if idx >= 0 && idx < len(tabs) {
		m.tab, m.tabSet = tabs[idx], true
	}
}

func (m *Model) move(d int) {
	if d == 0 {
		return
	}
	t := m.tab
	m.clearPin(t)
	n := m.rowCount(t)
	if n == 0 {
		m.sel[t] = 0
		m.scroll[t] = 0
		return
	}
	sel := clampSel(m.sel[t], n)
	maxOff := m.maxScroll(t, sel)
	off := min(max(m.scroll[t], 0), maxOff)
	if d > 0 {
		if off < maxOff {
			m.scroll[t] = off + 1
			return
		}
		next := clampSel(sel+1, n)
		if next != sel {
			m.sel[t] = next
			m.scroll[t] = 0
			if t == TabMail {
				m.mail.cycleID = ""
			}
		} else {
			m.scroll[t] = maxOff
		}
		return
	}
	m.unfollow()
	if off > 0 {
		m.scroll[t] = off - 1
		return
	}
	prev := clampSel(sel-1, n)
	if prev != sel {
		m.sel[t] = prev
		m.scroll[t] = m.maxScroll(t, prev)
		if t == TabMail {
			m.mail.cycleID = ""
		}
	} else {
		m.scroll[t] = 0
	}
}

func (m *Model) unfollow() {
	if m.tab == TabMail {
		m.mail.follow = false
	}
}

// stepBack undoes one level of the current tab's state; false at the top.
func (m *Model) stepBack() bool {
	switch m.tab {
	case TabMail:
		cur, hasCur := m.selectedMail()
		switch {
		case len(m.mail.expanded) > 0:
			clear(m.mail.expanded)
		case m.mail.from != "":
			m.mail.from = ""
			m.mail.cycleID = ""
		case m.mail.query != "":
			m.mail.query = ""
			m.mail.cycleID = ""
		default:
			return false
		}
		if hasCur {
			m.anchorMail(cur.ID)
		} else {
			m.sel[TabMail] = clampSel(m.sel[TabMail], m.rowCount(TabMail))
			m.scroll[TabMail] = 0
		}
		m.clearPin(TabMail)
		return true
	case TabMembers:
		if m.retired {
			m.retired = false
			m.sel[TabMembers] = clampSel(m.sel[TabMembers], m.rowCount(TabMembers))
			m.scroll[TabMembers] = 0
			m.clearPin(TabMembers)
			return true
		}
	case TabGraph:
		if m.graphAt != "" {
			m.graphAt = ""
			return true
		}
	}
	return false
}

// enter acts on the selected row: jump to a member's pane, open the
// hand-back on Overview, drill into an Epic in-place, or toggle a
// message's excerpt.
func (m Model) enter() (tea.Model, tea.Cmd) {
	switch m.tab {
	case TabMembers:
		if handle := m.selectedHandle(); handle != "" {
			return m, m.focus(handle)
		}
	case TabOverview:
		if handle := m.selectedHandle(); handle != "" {
			return m, m.focus(handle)
		}
		if hbRow, ok := m.overviewHandBackRow(); ok && m.sel[TabOverview] == hbRow {
			return m, m.openHandBack()
		}
	case TabEpics:
		return m.drillDown()
	case TabMail:
		m.toggleExcerpt()
	case TabGraph:
		if m.graphUsesTUI() {
			if handle := m.selectedGraphHandle(); handle != "" {
				return m, m.focus(handle)
			}
		}
	}
	return m, nil
}

func (m *Model) switchToEpic(e adapter.Epic) tea.Cmd {
	if e.SessionID == "" {
		m.hint, m.hintBad = "epic has no session", true
		return nil
	}
	name := cmp.Or(e.Name, e.SessionID)
	session := e.SessionID
	return m.act("switch to "+name, true, func(ctx context.Context, c Controller) error { return c.SwitchTo(ctx, session) })
}

// focus goes to handle's pane, minimizing the panel; without a pane it
// says so in the footer. When drilled down into another session, it
// switches the Rex app to that session and focuses the block there.
func (m *Model) focus(handle string) tea.Cmd {
	a, ok := m.agent(handle)
	if !ok || a.Pane != "present" || a.BlockID == "" {
		m.hint, m.hintBad = handle+" has no pane to go to", true
		return nil
	}
	block := a.BlockID
	if m.DrilledDown() {
		session := cmp.Or(a.SessionID, m.drill.epic.SessionID)
		if session != "" && session != m.drill.hubSession {
			return m.act("focus "+handle, true, func(ctx context.Context, c Controller) error {
				return c.JumpToSession(ctx, session, block)
			})
		}
	}
	return m.act("focus "+handle, true, func(ctx context.Context, c Controller) error { return c.Jump(ctx, block) })
}

func (m Model) mailKey(s string) (Model, tea.Cmd) {
	switch s {
	case "/":
		m.mail.typing = true
	case "p":
		m.cycleMailParticipant()
		m.clearPin(TabMail)
		return m, nil
	case "f":
		m.mail.follow = !m.mail.follow
		m.clearPin(TabMail)
		if m.mail.follow {
			last := max(m.rowCount(TabMail)-1, 0)
			if m.sel[TabMail] != last {
				m.mail.cycleID = ""
				m.scroll[TabMail] = 0
			}
			m.sel[TabMail] = last
		}
	case "space":
		m.toggleExcerpt()
	case "o":
		return m, m.openMail()
	}
	m.sel[TabMail] = clampSel(m.sel[TabMail], m.rowCount(TabMail))
	return m, nil
}

func (m Model) searchKey(k tea.KeyPressMsg) Model {
	switch k.String() {
	case "enter":
		m.mail.typing = false
	case "esc":
		m.mail.typing, m.mail.query = false, ""
	case "backspace":
		if _, n := utf8.DecodeLastRuneInString(m.mail.query); n > 0 {
			m.mail.query = m.mail.query[:len(m.mail.query)-n]
		}
	default:
		if k.Text != "" {
			m.mail.query += clean(k.Text)
		}
	}
	m.mail.cycleID = ""
	m.sel[TabMail] = clampSel(m.sel[TabMail], m.rowCount(TabMail))
	m.scroll[TabMail] = 0
	m.clearPin(TabMail)
	return m
}

func (m *Model) graphKey(s string) {
	msgs := m.graphMessages()
	i, replaying := m.replayIndex()
	if !replaying {
		i = len(msgs) - 1
	}
	switch s {
	case "[":
		if len(msgs) > 0 {
			m.graphAt = msgs[max(i-1, 0)].ID
		}
	case "]":
		if replaying {
			if i+1 >= len(msgs)-1 {
				m.graphAt = ""
			} else {
				m.graphAt = msgs[i+1].ID
			}
		}
	case "L":
		m.graphAt = ""
	case "h":
		m.hideRetired = !m.hideRetired
		if _, ok := m.replayIndex(); !ok {
			m.graphAt = ""
		}
		m.sel[TabGraph] = clampSel(m.sel[TabGraph], m.rowCount(TabGraph))
		m.scroll[TabGraph] = 0
		m.clearPin(TabGraph)
	case "t":
		if m.graphUsesTUI() {
			m.graphTUI = false
			if m.gfx != nil {
				m.gfx.stalled = false
				m.gfx.dirty = true
				if !m.gfx.cell.Valid() {
					m.hint = "graph: Kitty image unavailable (no cell metrics); using TUI"
				}
			} else {
				m.hint = "graph: Kitty image unavailable; using TUI"
			}
		} else {
			m.graphTUI = true
		}
	}
}
