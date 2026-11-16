package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/est7/rex-amq-ui/internal/adapter"
	"github.com/est7/rex-amq-ui/internal/graph"
	"github.com/est7/rex-amq-ui/internal/kitty"
	"github.com/est7/rex-amq-ui/internal/layer"
	"github.com/est7/rex-amq-ui/internal/pixelmouse"
	"github.com/est7/rex-amq-ui/internal/raster"
)

// Graphics is where the Graph tab's image goes: Kitty frame files in Dir
// (kitty.DefaultDir in production) named for process PID.
type Graphics struct {
	Dir string
	PID int
}

// settleDelay is how long a new or resized image waits for the renderer:
// on a resize or a resume it erases and repaints the screen, which deletes
// images, and its write follows ours within the same tick.
const settleDelay = 50 * time.Millisecond

// graphTab is the Graph tab's image: the graph model, the Kitty transport,
// and the frame loop. The Model holds it by pointer; only Update changes
// it (Bubble Tea runs Update and Filter on one goroutine), and the
// commands it returns carry values, never the pointer.
type graphTab struct {
	g      *graph.Model
	tr     *kitty.Transport
	sched  *kitty.Scheduler
	canvas *raster.Canvas
	cell   raster.Metrics
	// asking: a CellSize call is under way; again: something that can
	// change the cell size happened meanwhile, so ask once more after it.
	asking, again bool
	phase         phase
	// on: a frame may be on screen; mouse: pixel mouse reports are on.
	on, mouse bool
	// dirty: the picture changed since the last frame.
	dirty bool
	// gen counts showings; output of an earlier one is stale (Filter).
	gen uint64
	// at, hide are the replay cursor and Hide retired the image shows.
	at   string
	hide bool
	// selected is the node the human clicked; a second click focuses it.
	selected  string
	visible   bool
	owner     string
	connected bool
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

func newGraphTab(g Graphics, clock Clock) *graphTab {
	return &graphTab{g: graph.New(clock.Now), tr: kitty.NewTransport(g.Dir, g.PID), sched: kitty.NewScheduler(30)}
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
	graphSettle struct{ gen uint64 }
	// graphicsOut is bytes for the terminal from showing gen; it prints
	// through tea.Raw, and Filter drops it once gen is over.
	graphicsOut struct {
		gen uint64
		seq string
	}
)

func (g graphicsOut) String() string { return g.seq }

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
		if msg.gen == gt.gen && gt.phase == settling {
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
	case !showing && (gt.phase != hidden || gt.on || gt.mouse):
		cmds = append(cmds, gt.hideImage())
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
	gen := gt.gen
	return tea.Tick(settleDelay, func(time.Time) tea.Msg { return graphSettle{gen} })
}

// restart makes the next frame wait for the renderer again: a resize
// erases the screen. The image may still be up, so on stays.
func (gt *graphTab) restart() {
	if gt.phase != hidden {
		gt.sched.Stop()
		gt.phase = hidden
	}
}

// hideImage deletes every image and turns pixel mouse reports off; the
// frames still in flight turn stale.
func (gt *graphTab) hideImage() tea.Cmd {
	gt.sched.Stop()
	gt.gen++
	gt.phase = hidden
	seq := ""
	if gt.on {
		seq = string(gt.tr.ClearAll())
	}
	if gt.mouse {
		seq += pixelmouse.Disable
	}
	gt.on, gt.mouse = false, false
	if seq == "" {
		return nil
	}
	return tea.Raw(graphicsOut{gen: gt.gen, seq: seq})
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
func (m *Model) frame(t kitty.Tick) tea.Cmd {
	gt := m.gfx
	gt.dirty = false
	render, next, again := gt.sched.Fire(t, gt, m.deps.Clock.Now())
	if !render || !gt.visible || !gt.cell.Valid() || gt.phase != drawing {
		return nil
	}
	var cmds []tea.Cmd
	if out := m.draw(); out != "" {
		cmds = append(cmds, tea.Raw(graphicsOut{gen: gt.gen, seq: out}))
	}
	if again {
		cmds = append(cmds, tea.Tick(next, func(time.Time) tea.Msg { return graphTick{t} }))
	}
	return tea.Batch(cmds...)
}

// draw paints the scene into the region and returns the bytes that show
// it, pixel mouse reports switched on with the first.
func (m *Model) draw() string {
	gt := m.gfx
	r := m.GraphRegion()
	sz := graph.Size{Cols: r.W, Rows: r.H, Cell: gt.cell}
	w, h := sz.Pixels()
	if gt.canvas == nil || gt.canvas.Bounds().Max != (raster.Point{X: float64(w), Y: float64(h)}) || gt.canvas.Metrics() != gt.cell {
		gt.canvas = raster.NewCanvas(w, h, gt.cell)
	}
	gt.scene = gt.g.Scene(sz)
	graph.Paint(gt.canvas, gt.scene, graph.FromTheme(m.pal))
	b, err := gt.tr.Frame(gt.canvas.Image(), kitty.Cell{Col: r.X, Row: r.Y})
	if err != nil {
		m.hint, m.hintBad = "graph: "+err.Error(), true
		return ""
	}
	seq := string(b)
	if !gt.mouse {
		seq, gt.mouse = pixelmouse.Enable+seq, true
	}
	gt.on = true
	return seq
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
	if c.Button != tea.MouseLeft || !gt.on {
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

// CloseGraphics is what to write when the panel exits: delete the image
// and turn pixel mouse reports off, if it ever drew; it also removes the
// frame files the terminal has not read.
func (m Model) CloseGraphics() (string, error) {
	if m.gfx == nil {
		return "", nil
	}
	var seq string
	if cmd := m.gfx.hideImage(); cmd != nil {
		seq = fmt.Sprint(cmd().(tea.RawMsg).Msg)
	}
	return seq, m.gfx.tr.Cleanup()
}
