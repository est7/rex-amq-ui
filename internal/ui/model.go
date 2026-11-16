// Package ui is the panel's terminal UI: a Bubble Tea model that folds the
// adapter's panel stream, Rex's view of the panel's layer, the private APC
// channel, and keys into one state, and renders it as five tabs (Epics,
// Overview, Members, Mail, Graph) at the layer's size.
//
// The model only reads. It never sends, reads, or marks mail, and never
// starts, wakes, adopts, or retires an agent: its only effects are on its
// own layer (Controller) and opening a hand-back for the human to read.
package ui

import (
	"context"
	"errors"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/est7/rex-amq-ui/internal/adapter"
	"github.com/est7/rex-amq-ui/internal/apc"
	"github.com/est7/rex-amq-ui/internal/layer"
	"github.com/est7/rex-amq-ui/internal/theme"
)

// Controller is the panel's own layer (layer.Panel).
type Controller interface {
	Resize(ctx context.Context, s layer.Size) error
	Minimize(ctx context.Context) error
	// Jump minimizes the layer, then focuses block.
	Jump(ctx context.Context, block string) error
	// SwitchTo shows another session, then minimizes the layer; it fails
	// with layer.ErrRemoteControlOff when the Rex app refuses.
	SwitchTo(ctx context.Context, session string) error
	// OpenBeside runs argv in a new pane split in before anchor.
	OpenBeside(ctx context.Context, anchor, label string, argv []string) error
}

// Clock is the time relative times are measured from.
type Clock interface{ Now() time.Time }

// SystemClock is the wall clock.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

// Deps are the model's ways out. Zero fields disable what they serve, with
// a hint in the footer when the human asks for it.
type Deps struct {
	Layer Controller
	Clock Clock
	// Exec runs a program to completion with env added to this process's
	// environment (md-viewer).
	Exec func(ctx context.Context, env []string, name string, args ...string) error
	// LookPath finds a program on PATH.
	LookPath func(name string) (string, error)
	// CacheDir is where opened hand-backs are written.
	CacheDir string
	// Editor is $EDITOR, the fallback viewer.
	Editor string
}

// Options are the panel's starting state.
type Options struct {
	// Tab is the tab to show first (an apc.Tabs name); "" is Epics in a
	// personal session and Overview elsewhere.
	Tab  string
	Size layer.Size
	// Focused is the pane the human was in when the panel opened.
	Focused string
}

// Tab is one of the panel's views.
type Tab int

const (
	TabEpics Tab = iota
	TabOverview
	TabMembers
	TabMail
	TabGraph
)

var tabNames = map[Tab]string{TabEpics: "epics", TabOverview: "overview", TabMembers: "members", TabMail: "mail", TabGraph: "graph"}

func tabNamed(name string) (Tab, bool) {
	for t, n := range tabNames {
		if n == name {
			return t, true
		}
	}
	return 0, false
}

// Model is the panel. It is a value: Update returns the next one.
type Model struct {
	deps Deps

	st     adapter.State
	status adapter.StatusUpdate
	lay    layer.State
	pal    theme.Palette
	termFg color.Color
	// askedOwner is the block owner OSC 10/11 was last asked under;
	// answered, whether the terminal replied.
	askedOwner string
	answered   bool

	w, h    int
	tab     Tab
	tabSet  bool // the human or a command chose the tab
	size    layer.Size
	paused  bool
	frozen  string // the frame on screen while paused
	hint    string
	hintBad bool

	sel     map[Tab]int
	retired bool // the Retired group is open

	mail mailState
	// graph is the replay cursor: -1 follows Live, else the index of the
	// message shown.
	graph       int
	hideRetired bool
}

type mailState struct {
	typing   bool
	query    string
	from     string // handle the list is filtered by
	follow   bool
	expanded map[string]bool
}

// New is the panel before any frame.
func New(d Deps, o Options) Model {
	if d.Clock == nil {
		d.Clock = SystemClock{}
	}
	m := Model{deps: d, pal: theme.Default(), size: o.Size, sel: map[Tab]int{}, graph: -1,
		mail: mailState{follow: true, expanded: map[string]bool{}}}
	m.lay.Focused = o.Focused
	m.lay.Size = o.Size
	m.tab = TabOverview
	if t, ok := tabNamed(o.Tab); ok {
		m.tab, m.tabSet = t, true
	}
	return m
}

func (m Model) Init() tea.Cmd { return nil }

// actionDone is the result of a call on the layer or an opener.
type actionDone struct {
	what string
	// leaves: the call minimized the panel on success.
	leaves bool
	size   *layer.Size
	err    error
}

// Update folds msg into the model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case adapter.FrameUpdate:
		m.st = m.st.Apply(msg.Frame)
		m.settle()
	case adapter.StatusUpdate:
		m.status = msg
	case layer.State:
		return m.layerChanged(msg)
	case apc.Command:
		return m.command(msg)
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.ForegroundColorMsg:
		m.termFg = msg.Color
		if m.termFg != nil {
			m.pal = theme.FromTerminal(m.termFg, m.pal.Bg)
		}
	case tea.BackgroundColorMsg:
		m.answered = true
		fg := m.termFg
		if fg == nil {
			fg = m.pal.Fg
		}
		if msg.Color != nil {
			m.pal = theme.FromTerminal(fg, msg.Color)
		}
	case actionDone:
		return m.done(msg)
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

// settle keeps the tab and selections valid after the state changed.
func (m *Model) settle() {
	tabs := m.tabs()
	if !m.tabSet && m.st.Ready() && m.personal() {
		m.tab, m.tabSet = TabEpics, true
	}
	if !containsTab(tabs, m.tab) {
		m.tab = tabs[0]
	}
	if m.mail.follow {
		m.sel[TabMail] = len(m.mailRows()) - 1
	}
	for _, t := range tabs {
		m.sel[t] = clampSel(m.sel[t], m.rowCount(t))
	}
}

func clampSel(i, n int) int { return max(0, min(i, n-1)) }

func (m Model) personal() bool { return m.st.Panel != nil && m.st.Panel.Personal() }

// tabs are the tabs shown, in order: Epics first in a personal session.
func (m Model) tabs() []Tab {
	t := []Tab{TabOverview, TabMembers, TabMail, TabGraph}
	if m.personal() {
		t = append([]Tab{TabEpics}, t...)
	}
	return t
}

func containsTab(ts []Tab, t Tab) bool {
	for _, x := range ts {
		if x == t {
			return true
		}
	}
	return false
}

// layerChanged follows the layer: pause while it is minimized, resume when
// it comes back, and ask the terminal its colours once an app owns it.
func (m Model) layerChanged(s layer.State) (tea.Model, tea.Cmd) {
	was := m.lay
	m.lay = s
	m.size = s.Size
	if s.Focused == "" {
		m.lay.Focused = was.Focused
	}
	switch {
	case s.Minimized && !was.Minimized:
		m.pause()
	case !s.Minimized && was.Minimized:
		m.resume()
	}
	return m, m.askColours()
}

// askColours asks for OSC 10/11 while the panel shows, once per owner of
// its block until the terminal answers: Rex drops the query while no app
// owns the block, and the launcher owns it for a moment first.
func (m *Model) askColours() tea.Cmd {
	o := m.lay.Owner
	if m.answered || o == "" || o == m.askedOwner || m.paused {
		return nil
	}
	m.askedOwner = o
	return tea.Batch(tea.RequestForegroundColor, tea.RequestBackgroundColor)
}

func (m Model) command(c apc.Command) (tea.Model, tea.Cmd) {
	switch c.Verb {
	case apc.Pause:
		m.pause()
	case apc.Resume:
		m.resume()
		return m, m.askColours()
	case apc.Zoom:
		return m, m.zoom()
	case apc.Tab:
		if t, ok := tabNamed(c.Tab); ok && containsTab(m.tabs(), t) {
			m.tab, m.tabSet = t, true
		}
	}
	return m, nil
}

// pause freezes the frame on screen: View keeps returning it, so nothing
// is written while the layer is hidden, and frames still fold.
func (m *Model) pause() {
	if !m.paused {
		m.frozen = m.render()
		m.paused = true
	}
}

func (m *Model) resume() { m.paused, m.frozen = false, "" }

// Paused reports whether the panel has stopped drawing.
func (m Model) Paused() bool { return m.paused }

// act runs f on the layer off the update loop.
func (m Model) act(what string, leaves bool, f func(context.Context, Controller) error) tea.Cmd {
	if m.deps.Layer == nil {
		return func() tea.Msg { return actionDone{what: what, err: layer.ErrDisconnected} }
	}
	ctl := m.deps.Layer
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return actionDone{what: what, leaves: leaves, err: f(ctx, ctl)}
	}
}

func (m Model) zoom() tea.Cmd {
	to := m.size.Other()
	cmd := m.act("zoom", false, func(ctx context.Context, c Controller) error { return c.Resize(ctx, to) })
	return func() tea.Msg {
		d := cmd().(actionDone)
		d.size = &to
		return d
	}
}

func (m Model) minimize() tea.Cmd {
	return m.act("minimize", true, func(ctx context.Context, c Controller) error { return c.Minimize(ctx) })
}

func (m Model) done(d actionDone) (tea.Model, tea.Cmd) {
	if d.err != nil {
		m.hint, m.hintBad = hintFor(d.what, d.err), true
		return m, nil
	}
	m.hint, m.hintBad = "", false
	if d.size != nil {
		m.size = *d.size
	}
	if d.leaves {
		m.pause()
	}
	return m, nil
}

func hintFor(what string, err error) string {
	switch {
	case errors.Is(err, layer.ErrRemoteControlOff):
		return "Rex refused: " + err.Error()
	case errors.Is(err, layer.ErrDisconnected):
		return strings.TrimPrefix(err.Error(), "layer: ")
	}
	return what + " failed: " + err.Error()
}

// View is the frame: the current one, or while paused the frame that was
// on screen when the panel paused.
func (m Model) View() tea.View {
	frame := m.frozen
	if !m.paused {
		frame = m.render()
	}
	v := tea.NewView(frame)
	v.AltScreen = true
	return v
}

// Frame is the current frame as View would draw it unpaused.
func (m Model) Frame() string { return m.render() }
