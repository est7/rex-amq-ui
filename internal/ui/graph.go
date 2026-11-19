package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/est7/rex-amq-ui/internal/adapter"
	"github.com/est7/rex-amq-ui/internal/graph"
	"github.com/est7/rex-amq-ui/internal/kitty"
	"github.com/est7/rex-amq-ui/internal/layer"
	"github.com/est7/rex-amq-ui/internal/pixelmouse"
	"github.com/est7/rex-amq-ui/internal/raster"
)

// Graphics is where the Graph tab's image goes.
type Graphics struct {
	// Dir and PID name the Kitty frame files (kitty.DefaultDir and the
	// process id in production).
	Dir string
	PID int
	// Out is the terminal: the writer the program renders to, safe to
	// write while the renderer writes. Every graphics command is written
	// to it from Update, so they reach the terminal in the order Update
	// made them; a teardown can never be overtaken by the frame before it,
	// nor arrive after the next showing's frame.
	Out io.Writer
}

// settleDelay is how long a new or resized showing waits before its first
// frame: on a resize or a resume the renderer erases and repaints the
// screen, which deletes images, and that write can follow ours.
const settleDelay = 50 * time.Millisecond

// graphTab is the Graph tab's image: the graph model, the Kitty transport,
// and the frame loop. The Model holds it by pointer and only Update
// changes it (Bubble Tea runs Update on one goroutine); the commands it
// returns carry values, never the pointer.
//
// What it believes the terminal shows (an image, pixel mouse reports) is
// conservative: on as soon as any byte that may turn it on went out, off
// only after the write that turns it off succeeded; a failed teardown is
// tried again on the next Update.
type graphTab struct {
	out    io.Writer
	g      *graph.Model
	tr     *kitty.Transport
	sched  *kitty.Scheduler
	canvas *raster.Canvas
	cell   raster.Metrics
	// asking: a CellSize call is under way; again: something that can
	// change the cell size happened meanwhile, so ask once more after it.
	asking, again bool
	phase         phase
	// showing counts showings; a settle message of an earlier one is
	// stale.
	showing uint64
	// mouse: pixel mouse reports are on.
	mouse bool
	// dirty: the picture changed since the last frame.
	dirty bool
	// at, hide are the replay cursor and Hide retired the image shows.
	at   string
	hide bool
	// selected is the node the human clicked; a second click focuses it.
	selected  string
	visible   bool
	owner     string
	connected bool
	// stalled: the last frame was refused because the terminal had not
	// read the ones before it.
	stalled bool
	// scene is the last frame drawn, for hit tests.
	scene graph.Scene
}

// phase is where the image is in a showing.
type phase int

const (
	// hidden: no showing; nothing is drawn.
	hidden phase = iota
	// settling: shown, waiting settleDelay for the renderer's repaint
	// before the first frame.
	settling
	// drawing: frames go out as the scheduler paces them.
	drawing
)

// screen is the terminal as the Graph tab writes to it. A write cut short
// may leave the terminal's parser inside an escape sequence, so the next
// write first sends CAN, which aborts a control sequence, and ST, which
// ends a string such as an APC; both are no-ops otherwise. A short write
// with no error is io.ErrShortWrite.
type screen struct {
	w    io.Writer
	torn bool
}

const resetParser = "\x18\x1b\\"

func (s *screen) Write(p []byte) (int, error) {
	if s.torn {
		n, err := io.WriteString(s.w, resetParser)
		if err == nil && n < len(resetParser) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return 0, err
		}
		s.torn = false
	}
	n, err := s.w.Write(p)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	if err != nil && n > 0 {
		s.torn = true
	}
	return n, err
}

func newGraphTab(g Graphics, clock Clock) *graphTab {
	return &graphTab{out: &screen{w: g.Out}, g: graph.New(clock.Now), tr: kitty.NewTransport(g.Dir, g.PID), sched: kitty.NewScheduler(kitty.MaxFPS)}
}

// reset replaces the graph model for a newly subscribed session so nodes,
// pulses, and selection from the previous session cannot bleed over.
func (gt *graphTab) reset(clock Clock, st adapter.State) {
	gt.g = graph.New(clock.Now)
	if st.Ready() {
		gt.g.Update(st)
	}
	gt.at = ""
	gt.hide = false
	gt.selected = ""
	gt.scene = graph.Scene{}
	gt.dirty = true
}

// NeedsAnimation makes graphTab the scheduler's Animator: a frame is due
// when the picture changed or moves on its own.
func (gt *graphTab) NeedsAnimation() bool { return gt.dirty || gt.g.NeedsAnimation() }

// Messages of the image's own loop.
type (
	cellSize struct {
		w, h int
		err  error
	}
	graphTick   struct{ t kitty.Tick }
	graphSettle struct{ showing uint64 }
)

// graphics keeps the image in step with the model after msg: it measures
// the cell (when the tab shows, on a resize, an owner change, or a new
// connection), shows and hides the image with the tab, feeds the graph,
// turns clicks into selections, and paces frames. It runs after every
// Update.
func (m *Model) graphics(msg tea.Msg) tea.Cmd {
	gt := m.gfx
	var cmds []tea.Cmd
	measure := false
	switch msg := msg.(type) {
	case cellSize:
		gt.asking = false
		measure, gt.again = gt.again, false
		gt.cell = raster.Metrics{}
		if msg.err == nil && msg.w > 0 && msg.h > 0 {
			gt.cell = raster.Metrics{CellW: msg.w, CellH: msg.h}
		}
		gt.dirty = true
	case adapter.FrameUpdate:
		if _, beat := msg.Frame.(adapter.Heartbeat); !beat {
			gt.g.Update(m.st)
			gt.dirty = true
		}
	case tea.WindowSizeMsg:
		measure = true
		gt.restart()
	case layer.State:
		if msg.Owner != gt.owner || (msg.Connected && !gt.connected) {
			measure = true
		}
		gt.owner, gt.connected = msg.Owner, msg.Connected
	case tea.ForegroundColorMsg, tea.BackgroundColorMsg:
		gt.dirty = true
	case tea.MouseClickMsg:
		cmds = append(cmds, m.click(msg))
	case graphSettle:
		if msg.showing == gt.showing && gt.phase == settling {
			gt.phase = drawing
		}
	case graphTick:
		cmds = append(cmds, m.frame(msg.t))
	}

	if gt.at != m.graphAt || gt.hide != m.hideRetired {
		gt.at, gt.hide = m.graphAt, m.hideRetired
		gt.g.Show(gt.at, gt.hide)
		gt.dirty = true
	}
	gt.g.Select(gt.selected)

	r := m.GraphRegion()
	visible := m.tab == TabGraph && !m.paused && r.W > 0 && r.H > 0
	if visible && !gt.visible {
		measure = true
	}
	gt.visible = visible
	gt.g.SetPaused(!visible)
	// The size is asked on a trigger only, never because the last answer
	// was a failure: without Rex that would ask in a loop.
	switch {
	case visible && measure && gt.asking:
		gt.again = true
	case visible && measure:
		gt.asking = true
		cmds = append(cmds, m.measure())
	}

	showing := visible && gt.cell.Valid()
	switch {
	case !showing && (gt.phase != hidden || gt.tr.Shown() || gt.mouse):
		m.hideImage()
	case showing && gt.phase == hidden:
		cmds = append(cmds, gt.settle())
	case showing && gt.phase == drawing:
		cmds = append(cmds, m.kick())
	}
	return tea.Batch(cmds...)
}

// settle starts a showing: the first frame waits settleDelay.
func (gt *graphTab) settle() tea.Cmd {
	gt.phase, gt.dirty = settling, true
	gt.showing++
	n := gt.showing
	return tea.Tick(settleDelay, func(time.Time) tea.Msg { return graphSettle{n} })
}

// restart makes the next frame wait for the renderer again: a resize
// erases the screen. The image may still be up; the next frame replaces
// it.
func (gt *graphTab) restart() {
	if gt.phase != hidden {
		gt.sched.Stop()
		gt.phase = hidden
	}
}

// hideImage ends the showing: it deletes every image and turns pixel
// mouse reports off. What fails to reach the terminal stays owed, and the
// next Update tries again.
func (m *Model) hideImage() {
	gt := m.gfx
	gt.sched.Stop()
	gt.phase = hidden
	var errs []error
	if gt.tr.Shown() {
		errs = append(errs, gt.tr.Clear(gt.out))
	}
	if gt.mouse {
		if _, err := io.WriteString(gt.out, pixelmouse.Disable); err != nil {
			errs = append(errs, fmt.Errorf("pixel mouse off: %w", err))
		} else {
			gt.mouse = false
		}
	}
	if err := errors.Join(errs...); err != nil {
		m.hint, m.hintBad = "graph: "+err.Error(), true
	}
}

// kick starts the frame loop when a frame is due and none is running.
func (m *Model) kick() tea.Cmd {
	gt := m.gfx
	t, d, ok := gt.sched.Kick(gt, m.deps.Clock.Now())
	if !ok {
		return nil
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return graphTick{t} })
}

// frame draws the image when the loop's tick t fires, and arms the next.
// A tick of a stopped loop, or one that fires while no showing is drawing,
// changes nothing: the redraw it would have consumed stays due.
func (m *Model) frame(t kitty.Tick) tea.Cmd {
	gt := m.gfx
	if gt.phase != drawing || !gt.sched.Owns(t) {
		return nil
	}
	gt.dirty = false
	_, nt, next, again := gt.sched.Fire(t, gt, m.deps.Clock.Now())
	if err := m.draw(); err != nil {
		if errors.Is(err, kitty.ErrBacklog) {
			// Keep trying at the frame rate until the terminal reads.
			gt.dirty = true
			if !gt.stalled {
				gt.stalled = true
				m.hint, m.hintBad = "graph: the terminal has not read the last frames", true
			}
			if !again {
				return m.kick()
			}
		} else {
			m.hint, m.hintBad = "graph: "+err.Error(), true
		}
	} else if gt.stalled {
		gt.stalled = false
		m.hint, m.hintBad = "", false
	}
	if !again {
		return nil
	}
	return tea.Tick(next, func(time.Time) tea.Msg { return graphTick{nt} })
}

// draw paints the scene into the region and writes the frame to the
// terminal, switching pixel mouse reports on before the first.
func (m *Model) draw() error {
	gt := m.gfx
	if !gt.tr.Room() {
		// The terminal has not read the last frames: painting one now
		// would be thrown away.
		return kitty.ErrBacklog
	}
	r := m.GraphRegion()
	sz := graph.Size{Cols: r.W, Rows: r.H, Cell: gt.cell}
	w, h := sz.Pixels()
	if gt.canvas == nil || gt.canvas.Bounds().Max != (raster.Point{X: float64(w), Y: float64(h)}) || gt.canvas.Metrics() != gt.cell {
		gt.canvas = raster.NewCanvas(w, h, gt.cell)
	}
	gt.scene = gt.g.Scene(sz)
	graph.Paint(gt.canvas, gt.scene, graph.FromTheme(m.pal))
	if !gt.mouse {
		// Any byte of it may have turned a mode on: owe the off from the
		// first byte, not from success.
		n, err := io.WriteString(gt.out, pixelmouse.Enable)
		gt.mouse = n > 0
		if err != nil {
			return fmt.Errorf("pixel mouse on: %w", err)
		}
	}
	return gt.tr.Show(gt.out, gt.canvas.Image(), kitty.Cell{Col: r.X, Row: r.Y})
}

// measure reads the cell size from the panel's block.
func (m *Model) measure() tea.Cmd {
	ctl := m.deps.Layer
	if ctl == nil {
		return func() tea.Msg { return cellSize{err: layer.ErrDisconnected} }
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		w, h, err := ctl.CellSize(ctx)
		return cellSize{w: w, h: h, err: err}
	}
}

// click selects the member whose node is under a left click, in Members
// too; a click on the node already selected goes to its pane, as Enter
// does on Members.
func (m *Model) click(c tea.MouseClickMsg) tea.Cmd {
	gt := m.gfx
	if c.Button != tea.MouseLeft || !gt.tr.Shown() {
		return nil
	}
	r := m.GraphRegion()
	var targets []pixelmouse.Target
	for _, n := range gt.scene.Nodes {
		targets = append(targets, pixelmouse.Target{ID: n.ID, Rect: n.Rect.Pixels()})
	}
	id, ok := pixelmouse.HitTest(c.X, c.Y, pixelmouse.Region{Col: r.X, Row: r.Y, CellW: gt.cell.CellW, CellH: gt.cell.CellH}, targets)
	if !ok {
		return nil
	}
	if id == gt.selected {
		return m.focus(id)
	}
	gt.selected, gt.dirty = id, true
	for i, a := range m.members() {
		if a.Handle == id {
			m.sel[TabMembers] = i
		}
	}
	return nil
}

// CloseGraphics runs when the panel exits: it deletes the image and turns
// pixel mouse reports off if they may be on, writing to the terminal after
// the program's own reset, and removes the frame files the terminal has
// not read. Every copy of a Model shares its graphics, so any copy (the
// one the program started with, when a panic left no final one) closes
// them. It reports an error while a teardown could not be written.
func (m Model) CloseGraphics() error {
	if m.gfx == nil {
		return nil
	}
	m.hideImage()
	var err error
	if m.gfx.tr.Shown() || m.gfx.mouse {
		err = fmt.Errorf("graph: the image or pixel mouse may still be on: %s", m.hint)
	}
	return errors.Join(err, m.gfx.tr.Cleanup())
}
