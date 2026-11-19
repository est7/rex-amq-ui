// Package ui is the panel's terminal UI: a Bubble Tea model that folds the
// adapter's panel stream, Rex's view of the panel's layer, the private APC
// channel, and keys into one state, and renders it as five tabs (Epics,
// Overview, Members, Mail, Graph) at the layer's size.
//
// The model only reads. It never sends, reads, or marks mail, and never
// starts, wakes, adopts, or retires an agent: its only effects are on its
// own layer (Controller), opening a hand-back for the human to read, and
// the Graph tab's image (Kitty frame files and graphics commands, with
// Deps.Graphics).
package ui

import (
	"cmp"
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
	// JumpToSession switches the Rex app to session, focuses block inside
	// that session, and minimizes the panel in its own session.
	JumpToSession(ctx context.Context, session, block string) error
	// SwitchTo shows another session, then minimizes the layer; it fails
	// with layer.ErrRemoteControlOff when the Rex app refuses.
	SwitchTo(ctx context.Context, session string) error
	// OpenBeside runs argv in a new pane split in before anchor.
	OpenBeside(ctx context.Context, anchor, label string, argv []string) error
	// CellSize is the pixel size of one cell of the panel's terminal.
	CellSize(ctx context.Context) (w, h int, err error)
}

// Clock is the time relative times are measured from.
type Clock interface{ Now() time.Time }

// SystemClock is the wall clock.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

// StreamUpdate is an adapter.Update tagged with the subscription generation
// it belongs to. Update drops any StreamUpdate whose Gen does not match the
// model's active subscription so late frames or statuses from a cancelled
// stream cannot corrupt a newly selected session.
type StreamUpdate struct {
	Gen    uint64
	Update adapter.Update
}

// Deps are the model's ways out. Zero fields disable what they serve, with
// a hint in the footer when the human asks for it.
type Deps struct {
	Layer Controller
	Clock Clock
	// Subscribe switches the live adapter stream to session and tags its
	// delivered updates with gen (StreamUpdate).
	Subscribe func(session string, gen uint64)
	// Exec runs a program to completion with env added to this process's
	// environment (md-viewer).
	Exec func(ctx context.Context, env []string, name string, args ...string) error
	// LookPath finds a program on PATH.
	LookPath func(name string) (string, error)
	// CacheDir is where opened hand-backs and persisted panel state are written.
	CacheDir string
	// Editor is $EDITOR, the fallback viewer.
	Editor string
	// Graphics, when set, draws the Graph tab's image with Kitty graphics;
	// without it the region stays background.
	Graphics *Graphics
}

// Options are the panel's starting state.
type Options struct {
	// Session is the root Rex session the panel was started for.
	Session string
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

	session   string
	streamGen uint64
	drill     *drillState

	st     adapter.State
	status adapter.StatusUpdate
	lay    layer.State
	pal    theme.Palette
	termFg color.Color
	// askedOwner is the block owner OSC 10/11 was last asked under;
	// answered, whether the terminal replied.
	askedOwner string
	answered   bool

	w, h        int
	tab         Tab
	tabSet      bool // the human or a command chose the tab
	size        layer.Size
	paused      bool
	frozen      string        // the frame on screen while paused
	frozenMouse tea.MouseMode // the mouse mode in force while paused
	// sizeHeld: a resize came while paused; Update passes it on to the
	// renderer on resume.
	sizeHeld bool
	// shown counts pauses and resumes. An action that leaves the panel
	// records it when it starts and pauses the panel on success only if no
	// pause or resume came in between: actions finish asynchronously, and
	// one that ends after the panel came back must not hide it again.
	shown   int
	hint    string
	hintBad bool

	sel       map[Tab]int
	scroll    map[Tab]int
	lastClick clickState
	retired   bool // the Retired group is open
	// owed is when each open obligation was first seen (owedSince).
	owed map[string]time.Time

	mail mailState
	// graphAt is the replay cursor: the id of the message shown, "" for
	// Live. It is an id, not an index, because the list under it changes
	// (removals, snapshots, Hide retired); a cursor whose message left the
	// list is Live.
	graphAt     string
	hideRetired bool
	// gfx is the Graph tab's image; nil without Deps.Graphics.
	gfx *graphTab
}

// drillState holds the Hub session we drilled down from and the Epic being
// viewed in-place. Nil at the root level.
type drillState struct {
	hubSession string
	hubLabel   string
	hubSt      adapter.State
	hubStatus  adapter.StatusUpdate
	hubSel     int
	epic       adapter.Epic
}

type mailState struct {
	typing   bool
	query    string
	from     string // handle the list is filtered by
	cycleID  string // message ID whose participants `p` is cycling through
	follow   bool
	expanded map[string]bool
}

// New is the panel before any frame.
func New(d Deps, o Options) Model {
	if d.Clock == nil {
		d.Clock = SystemClock{}
	}
	m := Model{deps: d, session: o.Session, pal: theme.Default(), size: o.Size, sel: map[Tab]int{}, scroll: map[Tab]int{},
		mail: mailState{follow: true, expanded: map[string]bool{}}}
	m.lay.Focused = o.Focused
	m.lay.Size = o.Size
	if d.Graphics != nil {
		m.gfx = newGraphTab(*d.Graphics, d.Clock)
	}
	m.tab = TabOverview
	if t, ok := tabNamed(o.Tab); ok {
		m.tab, m.tabSet = t, true
	}
	return m
}

// DrilledDown reports whether the panel is currently viewing a linked Epic
// in-place from a Hub session.
func (m Model) DrilledDown() bool { return m.drill != nil }

func (m Model) currentSession() string {
	if m.drill != nil {
		return cmp.Or(m.st.Scope.SessionID, m.drill.epic.SessionID)
	}
	return cmp.Or(m.st.Scope.SessionID, m.session)
}

func (m Model) Init() tea.Cmd { return nil }

// actionDone is the result of a call on the layer or an opener.
type actionDone struct {
	what string
	// leaves: the call minimized the panel on success.
	leaves bool
	// shown is Model.shown when the call started.
	shown   int
	size    *layer.Size
	saveErr error
	err     error
}

// heldSize is a terminal resize that arrived while the panel was paused
// (Filter). The renderer must not see it then: it would repaint.
type heldSize tea.WindowSizeMsg

// Filter is the program's message filter (tea.WithFilter): while the panel
// is paused it keeps resizes from the renderer, which repaints on every
// one, and hands them to the model, which passes the size on when it
// resumes.
func Filter(model tea.Model, msg tea.Msg) tea.Msg {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		if m, ok := model.(Model); ok && m.paused {
			return heldSize(size)
		}
	}
	return msg
}

// Update folds msg into the model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if su, ok := msg.(StreamUpdate); ok {
		if su.Gen != m.streamGen {
			return m, nil
		}
		msg = su.Update
	}
	next, cmd := m.update(msg)
	n := next.(Model)
	if n.sizeHeld && !n.paused {
		n.sizeHeld = false
		size := tea.WindowSizeMsg{Width: n.w, Height: n.h}
		cmd = tea.Batch(cmd, func() tea.Msg { return size })
	}
	if n.gfx != nil {
		cmd = tea.Batch(cmd, n.graphics(msg))
	}
	return n, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case heldSize:
		m.w, m.h, m.sizeHeld = msg.Width, msg.Height, true
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
	case tea.MouseClickMsg:
		return m.mouseClick(msg)
	case tea.MouseWheelMsg:
		return m.mouseWheel(msg)
	}
	return m, nil
}

// settle keeps the tab and selections valid after the state changed.
func (m *Model) settle() {
	m.trackOwed()
	tabs := m.tabs()
	if !m.tabSet && m.st.Ready() && !m.DrilledDown() && m.personal() {
		m.tab, m.tabSet = TabEpics, true
	}
	if !containsTab(tabs, m.tab) {
		m.tab = tabs[0]
	}
	if m.mail.follow {
		last := max(len(m.mailRows())-1, 0)
		if m.sel[TabMail] != last {
			m.sel[TabMail] = last
			m.scroll[TabMail] = 0
		}
	}
	for _, t := range tabs {
		prev := m.sel[t]
		m.sel[t] = clampSel(m.sel[t], m.rowCount(t))
		if m.sel[t] != prev {
			m.scroll[t] = 0
		} else {
			m.scroll[t] = min(max(m.scroll[t], 0), m.maxScroll(t, m.sel[t]))
		}
	}
	if _, ok := m.replayIndex(); !ok {
		m.graphAt = ""
	}
}

func clampSel(i, n int) int { return max(0, min(i, n-1)) }

func (m Model) personal() bool { return m.st.Panel != nil && m.st.Panel.Personal() }

// tabs are the tabs shown, in order: Epics first in a personal session,
// omitted while drilled down into a linked Epic.
func (m Model) tabs() []Tab {
	t := []Tab{TabOverview, TabMembers, TabMail, TabGraph}
	if !m.DrilledDown() && m.personal() {
		t = append([]Tab{TabEpics}, t...)
	}
	return t
}

// drillDown enters the selected Epic in-place: saves the Hub session state,
// resets Epic-local UI and Graph state, and switches the live stream to the
// Epic's session.
func (m Model) drillDown() (Model, tea.Cmd) {
	e, ok := m.selectedEpic()
	if !ok {
		return m, nil
	}
	if e.SessionID == "" {
		m.hint, m.hintBad = "epic has no session", true
		return m, nil
	}
	hubLabel := ""
	if m.st.Panel != nil {
		hubLabel = m.st.Panel.SessionLabel
	}
	if hubLabel == "" {
		hubLabel = "hub"
	}
	m.drill = &drillState{
		hubSession: cmp.Or(m.st.Scope.SessionID, m.session),
		hubLabel:   hubLabel,
		hubSt:      m.st,
		hubStatus:  m.status,
		hubSel:     m.sel[TabEpics],
		epic:       e,
	}
	m.st = adapter.State{}
	m.status = adapter.StatusUpdate{Status: adapter.Connecting}
	m.tab, m.tabSet = TabOverview, true
	m.hint, m.hintBad = "", false
	m.sel = map[Tab]int{}
	m.scroll = map[Tab]int{}
	m.lastClick = clickState{}
	m.retired = false
	m.owed = nil
	m.mail = mailState{follow: true, expanded: map[string]bool{}}
	m.graphAt = ""
	m.hideRetired = false
	if m.gfx != nil {
		m.gfx.reset(m.deps.Clock, m.st)
	}
	m.streamGen++
	return m, m.subscribe(e.SessionID, m.streamGen)
}

// popBack leaves a drilled-down Epic and restores the saved Hub Epics view
// while resubscribing the live stream to the Hub session.
func (m Model) popBack() (Model, tea.Cmd) {
	if m.drill == nil {
		return m, nil
	}
	d := m.drill
	m.drill = nil
	m.st = d.hubSt
	m.status = d.hubStatus
	m.tab, m.tabSet = TabEpics, true
	m.hint, m.hintBad = "", false
	m.sel = map[Tab]int{TabEpics: d.hubSel}
	m.scroll = map[Tab]int{}
	m.lastClick = clickState{}
	m.retired = false
	m.owed = nil
	m.mail = mailState{follow: true, expanded: map[string]bool{}}
	m.graphAt = ""
	m.hideRetired = false
	m.settle()
	if m.gfx != nil {
		m.gfx.reset(m.deps.Clock, m.st)
	}
	m.streamGen++
	return m, m.subscribe(d.hubSession, m.streamGen)
}

func (m Model) subscribe(session string, gen uint64) tea.Cmd {
	sub := m.deps.Subscribe
	if sub == nil {
		return nil
	}
	return func() tea.Msg {
		sub(session, gen)
		return nil
	}
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

// mouseMode is the Bubble Tea mouse mode while unpaused. While the Kitty
// Graph tab is settling or drawing with SGR pixel mouse (1002/1006/1016),
// Bubble Tea stays in MouseModeNone so it never clobbers pixelmouse.Enable;
// leaving Kitty Graph transitions MouseModeNone -> MouseModeCellMotion so
// Bubble Tea re-arms SGR cell mouse (?1002h ?1006h) right after
// pixelmouse.Disable.
func (m Model) mouseMode() tea.MouseMode {
	if m.gfx != nil && (m.gfx.mouse || m.gfx.phase != hidden) {
		return tea.MouseModeNone
	}
	return tea.MouseModeCellMotion
}

// pause freezes the frame on screen: View keeps returning it, so nothing
// is written while the layer is hidden, and frames still fold.
func (m *Model) pause() {
	if !m.paused {
		m.frozen = m.render()
		m.frozenMouse = m.mouseMode()
		m.paused = true
		m.shown++
	}
}

func (m *Model) resume() {
	if m.paused {
		m.paused, m.frozen, m.frozenMouse = false, "", tea.MouseModeNone
		m.shown++
	}
}

// Paused reports whether the panel has stopped drawing.
func (m Model) Paused() bool { return m.paused }

// act runs f on the layer off the update loop.
func (m Model) act(what string, leaves bool, f func(context.Context, Controller) error) tea.Cmd {
	if m.deps.Layer == nil {
		return func() tea.Msg { return actionDone{what: what, err: layer.ErrDisconnected} }
	}
	ctl, shown := m.deps.Layer, m.shown
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return actionDone{what: what, leaves: leaves, shown: shown, err: f(ctx, ctl)}
	}
}

func (m Model) zoom() tea.Cmd {
	to := m.size.Other()
	cacheDir := m.deps.CacheDir
	cmd := m.act("zoom", false, func(ctx context.Context, c Controller) error { return c.Resize(ctx, to) })
	return func() tea.Msg {
		d := cmd().(actionDone)
		d.size = &to
		if d.err == nil && cacheDir != "" {
			d.saveErr = layer.SaveSize(cacheDir, to)
		}
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
	if d.saveErr != nil {
		m.hint, m.hintBad = "save size failed: "+d.saveErr.Error(), true
	}
	if d.leaves && d.shown == m.shown {
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
	frame, mouse := m.frozen, m.frozenMouse
	if !m.paused {
		frame, mouse = m.render(), m.mouseMode()
	}
	v := tea.NewView(frame)
	v.AltScreen = true
	v.MouseMode = mouse
	return v
}

// Frame is the current frame as View would draw it unpaused.
func (m Model) Frame() string { return m.render() }
