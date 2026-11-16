package ui

import (
	"context"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
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
	case "z":
		return m, m.zoom()
	case "j", "down":
		m.move(1)
		return m, nil
	case "k", "up":
		m.move(-1)
		return m, nil
	case "g", "home":
		m.sel[m.tab] = 0
		m.unfollow()
		return m, nil
	case "G", "end":
		m.sel[m.tab] = max(m.rowCount(m.tab)-1, 0)
		return m, nil
	case "esc", "q":
		if m.stepBack() {
			return m, nil
		}
		return m, m.minimize()
	case "enter":
		return m.enter()
	}
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' && (m.tab == TabMembers || m.tab == TabEpics) {
		if i := int(s[0] - '1'); i < m.rowCount(m.tab) {
			m.sel[m.tab] = i
		}
		return m, nil
	}
	switch m.tab {
	case TabOverview:
		if s == "o" {
			return m, m.openHandBack()
		}
	case TabMembers:
		if s == "r" {
			m.retired = !m.retired
			m.sel[TabMembers] = clampSel(m.sel[TabMembers], m.rowCount(TabMembers))
		}
	case TabMail:
		return m.mailKey(s), nil
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

func (m *Model) move(d int) {
	m.sel[m.tab] = clampSel(m.sel[m.tab]+d, m.rowCount(m.tab))
	if d < 0 {
		m.unfollow()
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
		switch {
		case len(m.mail.expanded) > 0:
			clear(m.mail.expanded)
		case m.mail.from != "":
			m.mail.from = ""
		case m.mail.query != "":
			m.mail.query = ""
		default:
			return false
		}
		m.sel[TabMail] = clampSel(m.sel[TabMail], m.rowCount(TabMail))
		return true
	case TabMembers:
		if m.retired {
			m.retired = false
			m.sel[TabMembers] = clampSel(m.sel[TabMembers], m.rowCount(TabMembers))
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

// enter acts on the selected row: jump to a member's pane, switch to an
// Epic's session, or open a message's excerpt.
func (m Model) enter() (tea.Model, tea.Cmd) {
	switch m.tab {
	case TabMembers, TabOverview:
		if handle := m.selectedHandle(); handle != "" {
			return m, m.focus(handle)
		}
	case TabEpics:
		e, ok := m.selectedEpic()
		if !ok {
			return m, nil
		}
		session := e.SessionID
		return m, m.act("switch to "+e.Name, true, func(ctx context.Context, c Controller) error { return c.SwitchTo(ctx, session) })
	case TabMail:
		m.toggleExcerpt()
	}
	return m, nil
}

// focus goes to handle's pane, minimizing the panel; without a pane it
// says so in the footer.
func (m *Model) focus(handle string) tea.Cmd {
	a, ok := m.agent(handle)
	if !ok || a.Pane != "present" || a.BlockID == "" {
		m.hint, m.hintBad = handle+" has no pane to go to", true
		return nil
	}
	block := a.BlockID
	return m.act("focus "+handle, true, func(ctx context.Context, c Controller) error { return c.Jump(ctx, block) })
}

func (m Model) mailKey(s string) Model {
	switch s {
	case "/":
		m.mail.typing = true
	case "p":
		if m.mail.from != "" {
			m.mail.from = ""
		} else if h := m.memberHandle(m.sel[TabMembers]); h != "" {
			m.mail.from = h
		}
	case "f":
		m.mail.follow = !m.mail.follow
		if m.mail.follow {
			m.sel[TabMail] = max(m.rowCount(TabMail)-1, 0)
		}
	case "space":
		m.toggleExcerpt()
	}
	m.sel[TabMail] = clampSel(m.sel[TabMail], m.rowCount(TabMail))
	return m
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
	m.sel[TabMail] = clampSel(m.sel[TabMail], m.rowCount(TabMail))
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
	}
}

// matches reports whether subject holds the search query, ignoring case.
func matches(subject, query string) bool {
	return query == "" || strings.Contains(strings.ToLower(subject), strings.ToLower(query))
}
