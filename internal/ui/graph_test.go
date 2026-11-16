package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/est7/rex-amq-ui/internal/adapter"
	"github.com/est7/rex-amq-ui/internal/apc"
	"github.com/est7/rex-amq-ui/internal/graph"
	"github.com/est7/rex-amq-ui/internal/kitty"
	"github.com/est7/rex-amq-ui/internal/pixelmouse"
	"github.com/est7/rex-amq-ui/internal/theme"
)

// cellRecorder is a recorder that also answers the block's cell size.
type cellRecorder struct {
	recorder
	w, h int
	err  error
}

func (r *cellRecorder) CellSize(context.Context) (int, int, error) {
	r.note("cellsize")
	return r.w, r.h, r.err
}

// rig runs the panel the way the program does: each message a command
// produces goes through Filter, raw output is collected, everything else
// goes into Update, whose commands run in turn. Commands left when a run
// stops wait for the next one, as the program would run them: a working
// member animates forever, so every run is capped at n messages.
type rig struct {
	t       *testing.T
	m       Model
	pending []tea.Cmd
}

func newRig(t *testing.T, m Model) *rig { return &rig{t: t, m: m} }

// send updates the panel with msg and runs at most n messages after it.
func (r *rig) send(msg tea.Msg, n int) string {
	r.t.Helper()
	next, cmd := r.m.Update(msg)
	r.m = next.(Model)
	r.pending = append(r.pending, cmd)
	return r.run(n)
}

func (r *rig) run(n int) string {
	var out strings.Builder
	for len(r.pending) > 0 && n > 0 {
		c := r.pending[0]
		r.pending = r.pending[1:]
		if c == nil {
			continue
		}
		msg := c()
		if b, ok := msg.(tea.BatchMsg); ok {
			r.pending = append(r.pending, b...)
			continue
		}
		if msg == nil {
			continue
		}
		n--
		if msg = Filter(r.m, msg); msg == nil {
			continue
		}
		if raw, ok := msg.(tea.RawMsg); ok {
			out.WriteString(fmt.Sprint(raw.Msg))
			continue
		}
		next, more := r.m.Update(msg)
		r.m = next.(Model)
		r.pending = append(r.pending, more)
	}
	return out.String()
}

// shown is a rig on the Graph tab with its first frame out.
func shown(t *testing.T, ctl Controller) (*rig, string) {
	t.Helper()
	r := newRig(t, graphPanel(t, ctl))
	out := r.send(mustTab("graph"), 6)
	if transmitRE.FindString(out) == "" {
		t.Fatalf("no first frame: %q", out)
	}
	return r, out
}

// firstFrame is out up to the end of its first frame's commands.
func firstFrame(out string) string {
	loc := transmitRE.FindStringIndex(out)
	if loc == nil {
		return out
	}
	rest := out[loc[1]:]
	if strings.HasPrefix(rest, "\x1b_Ga=d,d=I") {
		end := strings.Index(rest, "\x1b\\") + 2
		return out[:loc[1]+end]
	}
	return out[:loc[1]]
}

const (
	cw, ch = 9, 22
	boardW = 120
	boardH = 40
)

func graphPanel(t *testing.T, ctl Controller) Model {
	t.Helper()
	now := time.Time(fixtures["edited-crew-attention"])
	return build(t, "edited-crew-attention", boardW, boardH,
		Deps{Layer: ctl, Clock: movingClock{&now}, Graphics: &Graphics{Dir: t.TempDir(), PID: 77}}, Options{})
}

var transmitRE = regexp.MustCompile(`\x1b7\x1b\[(\d+);(\d+)H\x1b_Ga=T,f=32,t=t,s=(\d+),v=(\d+),i=(\d),p=1,C=1,q=2;([A-Za-z0-9+/=]+)\x1b\\\x1b8`)

func TestGraphTabShowsFrameInRegion(t *testing.T) {
	ctl := &cellRecorder{w: cw, h: ch}
	g, out := shown(t, ctl)
	m := g.m
	out = firstFrame(out)
	r := m.GraphRegion()
	if !strings.HasPrefix(out, pixelmouse.Enable) {
		t.Fatalf("output %q: want pixel mouse on, then the frame", out)
	}
	tx := transmitRE.FindStringSubmatch(out)
	if tx == nil {
		t.Fatalf("no Kitty frame in %q", out)
	}
	want := []string{fmt.Sprint(r.Y + 1), fmt.Sprint(r.X + 1), fmt.Sprint(r.W * cw), fmt.Sprint(r.H * ch), "1"}
	if got := tx[1:6]; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("frame at row,col %s,%s size %sx%s id %s; want the region's top-left cell %v and its pixels", got[0], got[1], got[2], got[3], got[4], want)
	}
	if strings.Contains(out, "a=d") {
		t.Errorf("first frame deletes an image: %q", out)
	}
	file := filepath.Join(m.deps.Graphics.Dir, "tty-graphics-protocol-rex-amq-ui-77-1.rgba")
	if st, err := os.Stat(file); err != nil || st.Size() != int64(r.W*cw*r.H*ch*4) {
		t.Errorf("frame file %v %v, want %d RGBA bytes", st, err, r.W*cw*r.H*ch*4)
	}
	if !strings.Contains(strings.Join(ctl.got(), " "), "cellsize") {
		t.Errorf("calls %v: the cell size must come from the block", ctl.got())
	}
}

func TestGraphTabTextFrameUnchanged(t *testing.T) {
	g, _ := shown(t, &cellRecorder{w: cw, h: ch})
	with := g.m
	without := step(t, build(t, "edited-crew-attention", boardW, boardH, Deps{}, Options{}), mustTab("graph"))
	if with.Frame() != without.Frame() {
		t.Errorf("the Graph tab's cells change when the image is on: the region must stay background")
	}
}

func TestGraphTabLeavingClears(t *testing.T) {
	g, _ := shown(t, &cellRecorder{w: cw, h: ch})
	if out := g.send(key("tab"), 12); out != string(kitty.NewTransport("", 0).ClearAll())+pixelmouse.Disable {
		t.Fatalf("leaving Graph wrote %q, want ClearAll then pixel mouse off", out)
	}
	if out := g.run(12); out != "" {
		t.Fatalf("after leaving Graph the frame loop wrote %q", out)
	}
	out := firstFrame(g.send(mustTab("graph"), 6))
	if !strings.HasPrefix(out, pixelmouse.Enable) || strings.Contains(out, "a=d") || transmitRE.FindString(out) == "" {
		t.Errorf("coming back wrote %q, want mouse on and a fresh frame with nothing to delete", out)
	}
}

func TestGraphTabPauseSilences(t *testing.T) {
	g, _ := shown(t, &cellRecorder{w: cw, h: ch})
	// The animation loop's next tick is pending: the pause must outrun it.
	if len(g.pending) == 0 {
		t.Fatalf("no frame loop pending: the fixture's working members should animate")
	}
	next, cmd := g.m.Update(apc.Command{Verb: apc.Pause})
	g.m = next.(Model)
	g.pending = append([]tea.Cmd{cmd}, g.pending...)
	out := g.run(4)
	if out != string(kitty.NewTransport("", 0).ClearAll())+pixelmouse.Disable {
		t.Fatalf("pause wrote %q, want ClearAll then pixel mouse off (and the pending tick nothing)", out)
	}
	out = g.run(12)
	for _, f := range frames(t, "edited-crew-attention") {
		out += g.send(adapter.FrameUpdate{Frame: f}, 6)
	}
	if out != "" {
		t.Errorf("paused panel wrote %q", out)
	}
	if out = g.send(apc.Command{Verb: apc.Resume}, 12); transmitRE.FindString(out) == "" {
		t.Errorf("resume wrote %q, want the frame again", out)
	}
}

func TestGraphTabStaleOutputDropped(t *testing.T) {
	g, _ := shown(t, &cellRecorder{w: cw, h: ch})
	m := g.m
	cur := tea.RawMsg{Msg: graphicsOut{gen: m.gfx.gen, seq: "x"}}
	old := tea.RawMsg{Msg: graphicsOut{gen: m.gfx.gen - 1, seq: "x"}}
	other := tea.RawMsg{Msg: "\x1b]11;?\x07"}
	if Filter(m, cur) == nil || Filter(m, other) == nil {
		t.Errorf("Filter dropped current graphics or someone else's raw output")
	}
	if Filter(m, old) != nil {
		t.Errorf("Filter let a frame of an earlier showing through")
	}
}

func TestGraphTabNoCellSizeNoImage(t *testing.T) {
	g := newRig(t, graphPanel(t, &cellRecorder{err: errors.New("no size")}))
	if out := g.send(mustTab("graph"), 12); out != "" {
		t.Errorf("without a cell size the panel wrote %q", out)
	}
	if g.m.GraphRegion().H == 0 || !strings.Contains(plain(g.m.Frame()), "Live") {
		t.Errorf("the Graph tab's text is gone")
	}
	if out := newRig(t, graphPanel(t, nil)).send(mustTab("graph"), 12); out != "" {
		t.Errorf("without Rex the panel wrote %q", out)
	}
}

func TestGraphTabResizeRedraws(t *testing.T) {
	g, _ := shown(t, &cellRecorder{w: cw, h: ch})
	g.run(4) // let the animation settle into its loop
	out := g.send(tea.WindowSizeMsg{Width: 100, Height: 30}, 12)
	m := g.m
	tx := transmitRE.FindStringSubmatch(out)
	if tx == nil {
		t.Fatalf("resize wrote %q, want a new frame", out)
	}
	if r := m.GraphRegion(); tx[3] != fmt.Sprint(r.W*cw) || tx[4] != fmt.Sprint(r.H*ch) {
		t.Errorf("frame %sx%s after resize, want %dx%d", tx[3], tx[4], r.W*cw, r.H*ch)
	}
	if first := firstFrame(out); !strings.Contains(first, "\x1b\\\x1b8\x1b_Ga=d,d=I,i=") {
		t.Errorf("resize frame %q: want the new image up, then the old one deleted", first)
	}
}

// nodeClick is a left click on the center of node id, in block pixels.
func nodeClick(t *testing.T, m Model, id string) tea.MouseClickMsg {
	t.Helper()
	r := m.GraphRegion()
	for _, n := range m.gfx.scene.Nodes {
		if n.ID == id {
			c := n.Rect.Center()
			return tea.MouseClickMsg{X: r.X*cw + int(c.X), Y: r.Y*ch + int(c.Y), Button: tea.MouseLeft}
		}
	}
	t.Fatalf("no node %s in the last frame", id)
	return tea.MouseClickMsg{}
}

func TestGraphTabClickSelectsThenFocuses(t *testing.T) {
	ctl := &cellRecorder{w: cw, h: ch}
	g, _ := shown(t, ctl)
	out := g.send(nodeClick(t, g.m, "claude-02"), 12)
	if h := g.m.memberHandle(g.m.sel[TabMembers]); h != "claude-02" {
		t.Errorf("click selected member %q, want claude-02", h)
	}
	if !nodeSelected(g.m, "claude-02") || transmitRE.FindString(out) == "" {
		t.Errorf("the frame after the click does not show claude-02 selected (%q)", out)
	}
	if strings.Contains(strings.Join(ctl.got(), " "), "jump") {
		t.Fatalf("the first click jumped: %v", ctl.got())
	}
	r := g.m.GraphRegion()
	g.send(tea.MouseClickMsg{X: r.X*cw + 1, Y: r.Y*ch + 1, Button: tea.MouseLeft}, 6)
	if !nodeSelected(g.m, "claude-02") || g.m.gfx.selected != "claude-02" {
		t.Errorf("a click on empty space changed the selection")
	}
	// A retired member has no pane: the second click says so and stays.
	g.send(nodeClick(t, g.m, "codex-03"), 6)
	g.send(nodeClick(t, g.m, "codex-03"), 6)
	if g.m.Paused() || !strings.Contains(g.m.hint, "codex-03 has no pane") {
		t.Errorf("second click on a member without a pane: paused %v hint %q", g.m.Paused(), g.m.hint)
	}
	g.send(nodeClick(t, g.m, "claude-02"), 6)
	out = g.send(nodeClick(t, g.m, "claude-02"), 12)
	if got := strings.Join(ctl.got(), " "); !strings.Contains(got, "jump block:claude-02") {
		t.Fatalf("second click: calls %v, want a jump to claude-02's pane", ctl.got())
	}
	if !g.m.Paused() || !strings.Contains(out, "a=d,d=A") {
		t.Errorf("after the jump the panel should pause and clear its image: paused %v, out %q", g.m.Paused(), out)
	}
}

func nodeSelected(m Model, id string) bool {
	for _, n := range m.gfx.scene.Nodes {
		if n.ID == id {
			return n.Selected
		}
	}
	return false
}

func TestGraphTabReplayKeysDriveImage(t *testing.T) {
	g, _ := shown(t, &cellRecorder{w: cw, h: ch})
	total := func() int {
		n := 0
		for _, e := range g.m.gfx.scene.Edges {
			n += e.Count
		}
		return n
	}
	live := total()
	msgs := g.m.graphMessages()
	last := msgs[len(msgs)-1]
	if out := g.send(key("["), 12); transmitRE.FindString(out) == "" {
		t.Fatalf("[ wrote %q, want a new frame", out)
	}
	if got := total(); got != live-len(last.To) {
		t.Errorf("after [ the image counts %d messages per recipient, want %d (live %d less the last message's %d recipients)", got, live-len(last.To), live, len(last.To))
	}
	g.send(key("L"), 12)
	if got := total(); got != live {
		t.Errorf("after L the image counts %d, want %d", got, live)
	}
}

func TestGraphTabLegendColoursMatchStrokes(t *testing.T) {
	m := step(t, build(t, "edited-crew-attention", boardW, boardH, Deps{}, Options{}), mustTab("graph"))
	var legend string
	for _, l := range strings.Split(m.Frame(), "\n") {
		if strings.Contains(plain(l), "drained") && strings.Contains(plain(l), "obligation") {
			legend = l
		}
	}
	if legend == "" {
		t.Fatalf("no legend line in\n%s", plain(m.Frame()))
	}
	p := graph.FromTheme(theme.Default())
	for _, c := range []struct {
		glyph string
		col   string
	}{
		{"──", sgrFg(p.Drained)},
		{"━━", sgrFg(p.Obligation)},
		{"┄", sgrFg(p.Queued)},
	} {
		if s := styleBefore(t, legend, c.glyph); !strings.Contains(s, c.col) {
			t.Errorf("legend %q drawn %q, want the stroke colour %s", c.glyph, s, c.col)
		}
	}
	second := legend[strings.Index(legend, "┄")+len("┄"):]
	if s := styleBefore(t, second, "┄"); !strings.Contains(s, sgrFg(p.Alert)) {
		t.Errorf("legend's bounced dash drawn %q, want the alert colour", s)
	}
}

func TestGraphTabCloseGraphics(t *testing.T) {
	g, _ := shown(t, &cellRecorder{w: cw, h: ch})
	m := g.m
	seq, err := m.CloseGraphics()
	if err != nil || seq != string(kitty.NewTransport("", 0).ClearAll())+pixelmouse.Disable {
		t.Errorf("CloseGraphics = %q, %v; want ClearAll and mouse off", seq, err)
	}
	if files, _ := filepath.Glob(filepath.Join(m.deps.Graphics.Dir, "*.rgba")); len(files) != 0 {
		t.Errorf("frame files left: %v", files)
	}
	idle := graphPanel(t, &cellRecorder{w: cw, h: ch})
	if seq, err := idle.CloseGraphics(); seq != "" || err != nil {
		t.Errorf("CloseGraphics with nothing shown = %q, %v; want nothing (no APC on a panel that never drew)", seq, err)
	}
	if seq, err := build(t, "edited-crew-attention", boardW, boardH, Deps{}, Options{}).CloseGraphics(); seq != "" || err != nil {
		t.Errorf("CloseGraphics without graphics = %q, %v", seq, err)
	}
}
