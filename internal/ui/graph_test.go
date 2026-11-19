package ui

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/est7/rex-amq-ui/internal/adapter"
	"github.com/est7/rex-amq-ui/internal/apc"
	"github.com/est7/rex-amq-ui/internal/graph"
	"github.com/est7/rex-amq-ui/internal/kitty"
	"github.com/est7/rex-amq-ui/internal/layer"
	"github.com/est7/rex-amq-ui/internal/pixelmouse"
	"github.com/est7/rex-amq-ui/internal/raster"
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

// fakeTerm is a Kitty terminal: it applies the graphics commands written
// to it in order and keeps what they leave on screen. When reads is set it
// reads each t=t frame file as it gets the command, deleting it as Kitty
// does; a file already gone loads no image.
type fakeTerm struct {
	reads bool
	// cut, when set, takes the next write's first cut(p) bytes and fails
	// it (once); dead fails every write from then on.
	cut  func([]byte) int
	dead bool
	// cutAt is where in log the last cut write ended.
	cutAt int
	log   strings.Builder
	// images are the placed image ids, each with the frame file number it
	// shows; mouse the mouse modes on.
	images map[int]int
	mouse  map[string]bool
	// seqs are the frame numbers transmitted, in order.
	seqs []int
}

func newTerm() *fakeTerm {
	return &fakeTerm{reads: true, images: map[int]int{}, mouse: map[string]bool{}}
}

var termCmd = regexp.MustCompile(`\x1b_Ga=T,f=32,t=t,s=\d+,v=\d+,i=(\d),p=1,C=1,q=2;([A-Za-z0-9+/=]+)\x1b\\|\x1b_Ga=d,d=I,i=(\d),q=2\x1b\\|(\x1b_Ga=d,d=A,q=2\x1b\\)|\x1b\[\?(1002|1006|1016)([hl])`)

var frameNo = regexp.MustCompile(`-(\d+)\.rgba$`)

func (f *fakeTerm) Write(p []byte) (int, error) {
	if f.dead {
		return 0, errors.New("terminal gone")
	}
	var err error
	if f.cut != nil {
		n := min(f.cut(p), len(p))
		f.cut = nil
		p, err = p[:n], io.ErrUnexpectedEOF
		defer func() { f.cutAt = f.log.Len() }()
	}
	f.log.Write(p)
	for _, m := range termCmd.FindAllStringSubmatch(string(p), -1) {
		switch {
		case m[1] != "":
			id, _ := strconv.Atoi(m[1])
			b, _ := base64.StdEncoding.DecodeString(m[2])
			path := string(b)
			n, _ := strconv.Atoi(frameNo.FindStringSubmatch(path)[1])
			f.seqs = append(f.seqs, n)
			if !f.reads {
				f.images[id] = n
				continue
			}
			if os.Remove(path) == nil {
				f.images[id] = n
			}
		case m[3] != "":
			id, _ := strconv.Atoi(m[3])
			delete(f.images, id)
		case m[4] != "":
			clear(f.images)
		case m[5] != "":
			f.mouse[m[5]] = m[6] == "h"
		}
	}
	return len(p), err
}

// showing reports whether exactly one image is up with pixel mouse on;
// blank whether nothing is up and every mouse mode is off.
func (f *fakeTerm) showing() bool {
	return len(f.images) == 1 && f.mouse["1002"] && f.mouse["1006"] && f.mouse["1016"]
}

func (f *fakeTerm) blank() bool {
	return len(f.images) == 0 && !f.mouse["1002"] && !f.mouse["1006"] && !f.mouse["1016"]
}

func (f *fakeTerm) String() string {
	return "images " + strconv.Itoa(len(f.images)) + " mouse " + strconv.FormatBool(f.mouse["1016"])
}

// rig runs the panel the way the program does, but lets a test choose the
// order: the messages commands produce wait in held until delivered, each
// through Filter and Update, whose commands run and add theirs.
type rig struct {
	t    *testing.T
	m    Model
	term *fakeTerm
	held []tea.Msg
}

func (r *rig) exec(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			r.exec(c)
		}
	default:
		r.held = append(r.held, msg)
	}
}

func (r *rig) deliver(msg tea.Msg) {
	if msg = Filter(r.m, msg); msg == nil {
		return
	}
	next, cmd := r.m.Update(msg)
	r.m = next.(Model)
	r.exec(cmd)
}

// run delivers held messages in order, at most n: a working member
// animates forever.
func (r *rig) run(n int) {
	for n > 0 && len(r.held) > 0 {
		msg := r.held[0]
		r.held = r.held[1:]
		n--
		r.deliver(msg)
	}
}

// send delivers msg, runs at most n messages after it, and returns what
// the terminal got meanwhile.
func (r *rig) send(msg tea.Msg, n int) string {
	before := r.term.log.Len()
	r.deliver(msg)
	r.run(n)
	return r.term.log.String()[before:]
}

// take removes and returns the held messages of type T, keeping the rest.
func take[T tea.Msg](r *rig) []tea.Msg {
	var out, keep []tea.Msg
	for _, m := range r.held {
		if _, ok := m.(T); ok {
			out = append(out, m)
		} else {
			keep = append(keep, m)
		}
	}
	r.held = keep
	return out
}

const (
	cw, ch = 9, 22
	boardW = 120
	boardH = 40
)

func panelOn(t *testing.T, fixture string, ctl Controller, term *fakeTerm) *rig {
	t.Helper()
	now := time.Time(fixtures[fixture])
	m := build(t, fixture, boardW, boardH,
		Deps{Layer: ctl, Clock: movingClock{&now}, Graphics: &Graphics{Dir: t.TempDir(), PID: 77, Out: term}}, Options{})
	return &rig{t: t, m: m, term: term}
}

// shown is a rig on the Graph tab of the crew fixture (two working
// members: it animates) with its first frame on screen.
func shown(t *testing.T, ctl Controller) *rig {
	t.Helper()
	r := panelOn(t, "edited-crew-attention", ctl, newTerm())
	r.send(mustTab("graph"), 6)
	if !r.term.showing() {
		t.Fatalf("no first frame: %s, log %q", r.term, r.term.log.String())
	}
	return r
}

var transmitRE = regexp.MustCompile(`\x1b7\x1b\[(\d+);(\d+)H\x1b_Ga=T,f=32,t=t,s=(\d+),v=(\d+),i=(\d),p=1,C=1,q=2;([A-Za-z0-9+/=]+)\x1b\\\x1b8`)

const clearOff = "\x1b_Ga=d,d=A,q=2\x1b\\" + pixelmouse.Disable

func TestGraphTabShowsFrameInRegion(t *testing.T) {
	ctl := &cellRecorder{w: cw, h: ch}
	r := shown(t, ctl)
	out := r.term.log.String()
	reg := r.m.GraphRegion()
	if !strings.HasPrefix(out, pixelmouse.Enable) {
		t.Fatalf("terminal got %q: want pixel mouse on, then the frame", out)
	}
	tx := transmitRE.FindStringSubmatch(out)
	want := []string{strconv.Itoa(reg.Y + 1), strconv.Itoa(reg.X + 1), strconv.Itoa(reg.W * cw), strconv.Itoa(reg.H * ch), "1"}
	if tx == nil || strings.Join(tx[1:6], ",") != strings.Join(want, ",") {
		t.Fatalf("first frame %q, want at row,col %s,%s size %sx%s id %s", tx, want[0], want[1], want[2], want[3], want[4])
	}
	if strings.Contains(out[:strings.Index(out, tx[0])+len(tx[0])], "a=d") {
		t.Errorf("first frame deletes an image: %q", out)
	}
	if !strings.Contains(strings.Join(ctl.got(), " "), "cellsize") {
		t.Errorf("calls %v: the cell size must come from the block", ctl.got())
	}
}

func TestGraphTabTextFrameUnchanged(t *testing.T) {
	with := shown(t, &cellRecorder{w: cw, h: ch}).m
	without := step(t, build(t, "edited-crew-attention", boardW, boardH, Deps{}, Options{}), mustTab("graph"))
	if with.Frame() != without.Frame() {
		t.Errorf("the Graph tab's cells change when the image is on: the region must stay background")
	}
}

func TestGraphTabLeavingClears(t *testing.T) {
	r := shown(t, &cellRecorder{w: cw, h: ch})
	if out := r.send(key("tab"), 12); out != clearOff || !r.term.blank() {
		t.Fatalf("leaving Graph wrote %q (%s), want ClearAll then pixel mouse off", out, r.term)
	}
	if out := r.send(key("tab"), 12); out != "" {
		t.Fatalf("on another tab the panel wrote %q", out)
	}
	r.send(mustTab("graph"), 6)
	if !r.term.showing() {
		t.Errorf("coming back: %s", r.term)
	}
}

func TestGraphTabPauseSilences(t *testing.T) {
	r := shown(t, &cellRecorder{w: cw, h: ch})
	if len(r.held) == 0 {
		t.Fatalf("no frame loop pending: the fixture's working members should animate")
	}
	ticks := take[graphTick](r)
	if out := r.send(apc.Command{Verb: apc.Pause}, 0); out != clearOff {
		t.Fatalf("pause wrote %q, want ClearAll then pixel mouse off", out)
	}
	before := r.term.log.Len()
	for _, tk := range ticks {
		r.deliver(tk)
	}
	r.run(12)
	for _, f := range frames(t, "edited-crew-attention") {
		r.send(adapter.FrameUpdate{Frame: f}, 6)
	}
	if out := r.term.log.String()[before:]; out != "" || !r.term.blank() {
		t.Errorf("paused panel wrote %q", out)
	}
	r.send(apc.Command{Verb: apc.Resume}, 12)
	if !r.term.showing() {
		t.Errorf("resume: %s", r.term)
	}
}

// TestGraphTabQuickReenterTeardown: leave, come back, and leave again
// before anything the first leave started has run. However late the held
// messages arrive, the terminal ends with no image and pixel mouse off.
func TestGraphTabQuickReenterTeardown(t *testing.T) {
	r := shown(t, &cellRecorder{w: cw, h: ch})
	r.deliver(key("tab"))
	r.deliver(mustTab("graph"))
	r.deliver(key("tab"))
	held := r.held
	r.held = nil
	for i := len(held) - 1; i >= 0; i-- {
		r.deliver(held[i])
	}
	r.run(20)
	if !r.term.blank() {
		t.Fatalf("after leaving twice: %s, log tail %q", r.term, tail(r.term.log.String()))
	}
	before := r.term.log.Len()
	if err := r.m.CloseGraphics(); err != nil || r.term.log.Len() != before || !r.term.blank() {
		t.Errorf("CloseGraphics on a clear terminal: err %v, wrote %q", err, r.term.log.String()[before:])
	}
}

// TestGraphTabOldTeardownCannotEraseNewShowing: leave, come back and draw;
// what the leave started arrives last and must not touch the new image.
func TestGraphTabOldTeardownCannotEraseNewShowing(t *testing.T) {
	r := shown(t, &cellRecorder{w: cw, h: ch})
	r.deliver(key("tab"))
	old := r.held
	r.held = nil
	r.send(mustTab("graph"), 8)
	if !r.term.showing() {
		t.Fatalf("the new showing drew nothing: %s", r.term)
	}
	for _, m := range old {
		r.deliver(m)
	}
	r.run(8)
	if !r.term.showing() {
		t.Errorf("the old showing's messages took the new image down: %s, tail %q", r.term, tail(r.term.log.String()))
	}
}

// TestGraphTabFramesInOrder: ticks replayed late, newest first, draw
// nothing; frames reach the terminal in the order they were made.
func TestGraphTabFramesInOrder(t *testing.T) {
	r := shown(t, &cellRecorder{w: cw, h: ch})
	var seen []tea.Msg
	for range 4 {
		ticks := take[graphTick](r)
		seen = append(seen, ticks...)
		for _, m := range ticks {
			r.deliver(m)
		}
	}
	if len(seen) < 3 {
		t.Fatalf("only %d ticks: the fixture should animate", len(seen))
	}
	before := len(r.term.seqs)
	for i := len(seen) - 1; i >= 0; i-- {
		r.deliver(seen[i])
	}
	if got := len(r.term.seqs) - before; got != 0 {
		t.Errorf("%d frames drawn by ticks already used", got)
	}
	for i := 1; i < len(r.term.seqs); i++ {
		if r.term.seqs[i] <= r.term.seqs[i-1] {
			t.Fatalf("frames reached the terminal out of order: %v", r.term.seqs)
		}
	}
	if !r.term.showing() {
		t.Errorf("after replayed ticks: %s", r.term)
	}
}

func tail(s string) string { return s[max(0, len(s)-160):] }

// idle is the crew fixture with every member idle: nothing animates, so
// the image is drawn only when something changes.
func idle(t *testing.T, term *fakeTerm) *rig {
	t.Helper()
	r := panelOn(t, "recorded-crew-lifecycle", &cellRecorder{w: cw, h: ch}, term)
	return r
}

// TestGraphTabStaleTickKeepsRedraw: an idle Graph whose first tick is held
// across a leave and a return must still draw the new showing.
func TestGraphTabStaleTickKeepsRedraw(t *testing.T) {
	for _, c := range []struct {
		name  string
		leave tea.Msg
		back  tea.Msg
	}{
		{"leave and return", key("tab"), mustTab("graph")},
		{"pause and resume", apc.Command{Verb: apc.Pause}, apc.Command{Verb: apc.Resume}},
		{"resize", tea.WindowSizeMsg{Width: boardW, Height: boardH - 2}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := idle(t, newTerm())
			r.deliver(mustTab("graph"))
			r.run(2) // cellSize, settle
			old := take[graphTick](r)
			if len(old) != 1 {
				t.Fatalf("held %d ticks after settling, want the first frame's", len(old))
			}
			r.deliver(c.leave)
			if c.back != nil {
				r.deliver(c.back)
			}
			fresh := r.held
			r.held = nil
			for _, m := range fresh {
				if _, ok := m.(cellSize); ok {
					r.deliver(m)
				}
			}
			r.deliver(old[0])
			for _, m := range fresh {
				if _, ok := m.(cellSize); !ok {
					r.deliver(m)
				}
			}
			r.run(12)
			if !r.term.showing() {
				t.Errorf("idle Graph after a stale tick: %s", r.term)
			}
		})
	}
}

func TestGraphTabStaleOutputCannotReachTerminal(t *testing.T) {
	r := shown(t, &cellRecorder{w: cw, h: ch})
	for _, m := range r.held {
		if raw, ok := m.(tea.RawMsg); ok {
			t.Errorf("graphics go through tea.Raw (%q): concurrent commands can reorder them", raw.Msg)
		}
	}
}

func TestGraphTabNoCellSizeNoImage(t *testing.T) {
	r := panelOn(t, "edited-crew-attention", &cellRecorder{err: errors.New("no size")}, newTerm())
	if out := r.send(mustTab("graph"), 12); out != "" {
		t.Errorf("without a cell size the panel wrote %q", out)
	}
	if r.m.GraphRegion().H == 0 || !strings.Contains(plain(r.m.Frame()), "Live") {
		t.Errorf("the Graph tab's text is gone")
	}
	if out := panelOn(t, "edited-crew-attention", nil, newTerm()).send(mustTab("graph"), 12); out != "" {
		t.Errorf("without Rex the panel wrote %q", out)
	}
}

func TestGraphTabFailedMeasureDoesNotSpin(t *testing.T) {
	ctl := &cellRecorder{err: errors.New("no size")}
	r := panelOn(t, "edited-crew-attention", ctl, newTerm())
	r.send(mustTab("graph"), 500)
	if len(r.held) != 0 {
		t.Errorf("%d messages still held after 500: a failed measure keeps asking", len(r.held))
	}
	if n := strings.Count(strings.Join(ctl.got(), " "), "cellsize"); n != 1 {
		t.Errorf("asked the cell size %d times for one showing, want 1", n)
	}
	r.send(layer.State{Connected: false}, 20)
	ctl.err, ctl.w, ctl.h = nil, cw, ch
	r.send(layer.State{Connected: true}, 20)
	if !r.term.showing() {
		t.Errorf("the connection coming up did not measure and draw: %s", r.term)
	}
	off := panelOn(t, "edited-crew-attention", nil, newTerm())
	off.send(mustTab("graph"), 500)
	if len(off.held) != 0 {
		t.Errorf("without Rex, %d messages still held after 500", len(off.held))
	}
}

func TestGraphTabResizeRedraws(t *testing.T) {
	r := shown(t, &cellRecorder{w: cw, h: ch})
	r.run(4)
	out := r.send(tea.WindowSizeMsg{Width: 100, Height: 30}, 12)
	tx := transmitRE.FindStringSubmatch(out)
	if tx == nil {
		t.Fatalf("resize wrote %q, want a new frame", out)
	}
	if reg := r.m.GraphRegion(); tx[3] != strconv.Itoa(reg.W*cw) || tx[4] != strconv.Itoa(reg.H*ch) {
		t.Errorf("frame %sx%s after resize, want %dx%d", tx[3], tx[4], reg.W*cw, reg.H*ch)
	}
	if !r.term.showing() {
		t.Errorf("after resize: %s", r.term)
	}
}

// TestGraphTabUnreadFramesBounded: a terminal that never reads frame files
// costs at most kitty.MaxUnread of them and says so; one that reads keeps
// none behind.
func TestGraphTabUnreadFramesBounded(t *testing.T) {
	stuck := newTerm()
	stuck.reads = false
	r := panelOn(t, "edited-crew-attention", &cellRecorder{w: cw, h: ch}, stuck)
	r.send(mustTab("graph"), 40)
	dir := r.m.deps.Graphics.Dir
	files, _ := filepath.Glob(filepath.Join(dir, "*.rgba"))
	if len(files) != kitty.MaxUnread {
		t.Errorf("%d frame files with a terminal that never reads, want %d", len(files), kitty.MaxUnread)
	}
	if !strings.Contains(r.m.hint, "not read") || !r.m.hintBad {
		t.Errorf("hint %q: the stalled image must be visible as a failure", r.m.hint)
	}
	for _, f := range files {
		os.Remove(f)
	}
	before := len(stuck.seqs)
	r.run(6)
	if len(stuck.seqs) == before {
		t.Errorf("no frame after the terminal caught up")
	}

	r = shown(t, &cellRecorder{w: cw, h: ch})
	r.run(40)
	if files, _ := filepath.Glob(filepath.Join(r.m.deps.Graphics.Dir, "*.rgba")); len(files) != 0 {
		t.Errorf("a reading terminal left %d frame files", len(files))
	}
}

// nodeClick is a left click on the center of node id, in block pixels.
func nodeClick(t *testing.T, m Model, id string) tea.MouseClickMsg {
	t.Helper()
	reg := m.GraphRegion()
	for _, n := range m.gfx.scene.Nodes {
		if n.ID == id {
			c := n.Rect.Center()
			return tea.MouseClickMsg{X: reg.X*cw + int(c.X), Y: reg.Y*ch + int(c.Y), Button: tea.MouseLeft}
		}
	}
	t.Fatalf("no node %s in the last frame", id)
	return tea.MouseClickMsg{}
}

func nodeSelected(m Model, id string) bool {
	for _, n := range m.gfx.scene.Nodes {
		if n.ID == id {
			return n.Selected
		}
	}
	return false
}

func TestGraphTabClickSelectsThenFocuses(t *testing.T) {
	ctl := &cellRecorder{w: cw, h: ch}
	r := shown(t, ctl)
	out := r.send(nodeClick(t, r.m, "claude-02"), 12)
	if h := r.m.memberHandle(r.m.sel[TabMembers]); h != "claude-02" {
		t.Errorf("click selected member %q, want claude-02", h)
	}
	if !nodeSelected(r.m, "claude-02") || transmitRE.FindString(out) == "" {
		t.Errorf("the frame after the click does not show claude-02 selected (%q)", out)
	}
	if strings.Contains(strings.Join(ctl.got(), " "), "jump") {
		t.Fatalf("the first click jumped: %v", ctl.got())
	}
	reg := r.m.GraphRegion()
	r.send(tea.MouseClickMsg{X: reg.X*cw + 1, Y: reg.Y*ch + 1, Button: tea.MouseLeft}, 6)
	if r.m.gfx.selected != "claude-02" {
		t.Errorf("a click on empty space changed the selection")
	}
	r.send(nodeClick(t, r.m, "codex-03"), 6)
	r.send(nodeClick(t, r.m, "codex-03"), 6)
	if r.m.Paused() || !strings.Contains(r.m.hint, "codex-03 has no pane") {
		t.Errorf("second click on a member without a pane: paused %v hint %q", r.m.Paused(), r.m.hint)
	}
	r.send(nodeClick(t, r.m, "claude-02"), 6)
	r.send(nodeClick(t, r.m, "claude-02"), 12)
	if got := strings.Join(ctl.got(), " "); !strings.Contains(got, "jump block:claude-02") {
		t.Fatalf("second click: calls %v, want a jump to claude-02's pane", ctl.got())
	}
	if !r.m.Paused() || !r.term.blank() {
		t.Errorf("after the jump the panel should pause and clear its image: paused %v, %s", r.m.Paused(), r.term)
	}
}

func TestGraphTabReplayKeysDriveImage(t *testing.T) {
	r := shown(t, &cellRecorder{w: cw, h: ch})
	total := func() int {
		n := 0
		for _, e := range r.m.gfx.scene.Edges {
			n += e.Count
		}
		return n
	}
	live := total()
	msgs := r.m.graphMessages()
	last := msgs[len(msgs)-1]
	if out := r.send(key("["), 12); transmitRE.FindString(out) == "" {
		t.Fatalf("[ wrote %q, want a new frame", out)
	}
	if got := total(); got != live-len(last.To) {
		t.Errorf("after [ the image counts %d messages per recipient, want %d", got, live-len(last.To))
	}
	r.send(key("L"), 12)
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
	r := shown(t, &cellRecorder{w: cw, h: ch})
	before := r.term.log.Len()
	if err := r.m.CloseGraphics(); err != nil || r.term.log.String()[before:] != clearOff || !r.term.blank() {
		t.Errorf("CloseGraphics: err %v, wrote %q; want ClearAll and mouse off", err, r.term.log.String()[before:])
	}
	if files, _ := filepath.Glob(filepath.Join(r.m.deps.Graphics.Dir, "*.rgba")); len(files) != 0 {
		t.Errorf("frame files left: %v", files)
	}
	idleTerm := newTerm()
	if err := panelOn(t, "edited-crew-attention", &cellRecorder{w: cw, h: ch}, idleTerm).m.CloseGraphics(); err != nil || idleTerm.log.Len() != 0 {
		t.Errorf("CloseGraphics with nothing shown: err %v, wrote %q (no APC on a panel that never drew)", err, idleTerm.log.String())
	}
	if err := build(t, "edited-crew-attention", boardW, boardH, Deps{}, Options{}).CloseGraphics(); err != nil {
		t.Errorf("CloseGraphics without graphics: %v", err)
	}
}

// BenchmarkGraphTabFrame is one wired frame: scene, paint, frame file and
// command, at a Board on a Retina display (23x49-pixel cells), against a
// terminal that reads every file.
func BenchmarkGraphTabFrame(b *testing.B) {
	now := time.Time(fixtures["edited-crew-attention"])
	m := build(b, "edited-crew-attention", 98, 34, Deps{Clock: movingClock{&now}, Graphics: &Graphics{Dir: b.TempDir(), PID: 1, Out: newTerm()}}, Options{})
	m.gfx.cell = raster.Metrics{CellW: 23, CellH: 49}
	for b.Loop() {
		if err := m.draw(); err != nil {
			b.Fatal(err)
		}
	}
}

var _ = graph.Size{}
var _ = theme.Default

// afterTransmit cuts a frame's write right after its a=T command: the
// terminal has the image, not the cursor restore or the old image's
// delete.
func afterTransmit(p []byte) int {
	if i := strings.Index(string(p), "\x1b\\\x1b8"); i >= 0 {
		return i + 2
	}
	return len(p)
}

// TestGraphTabPartialWriteStillTornDown: a write the terminal took only
// part of may have put an image or a mouse mode up; leaving and exiting
// must still clear both, after resetting the terminal's parser.
func TestGraphTabPartialWriteStillTornDown(t *testing.T) {
	t.Run("frame cut after its image", func(t *testing.T) {
		r := shown(t, &cellRecorder{w: cw, h: ch})
		r.term.cut = afterTransmit
		r.send(nodeClick(t, r.m, "claude-02"), 3) // a change: the next frame is cut
		if r.term.cut != nil {
			t.Fatal("no frame was written after the click")
		}
		out := r.send(key("tab"), 6)
		if !r.term.blank() {
			t.Fatalf("after a cut frame and leaving: %s, wrote %q", r.term, out)
		}
		if next := r.term.log.String()[r.term.cutAt:]; !strings.HasPrefix(next, resetParser) {
			t.Errorf("the first write after a cut one %q: want CAN ST first, ending any half-sent sequence", next[:min(len(next), 40)])
		}
	})
	t.Run("first frame cut, then exit", func(t *testing.T) {
		term := newTerm()
		term.cut = func(p []byte) int {
			if strings.Contains(string(p), "a=T") {
				return afterTransmit(p)
			}
			return len(p) + 1
		}
		r := panelOn(t, "edited-crew-attention", &cellRecorder{w: cw, h: ch}, term)
		r.send(mustTab("graph"), 4)
		if len(term.images) == 0 {
			t.Fatalf("the cut frame did not reach the terminal: %s", term)
		}
		if err := r.m.CloseGraphics(); err != nil || !term.blank() {
			t.Errorf("CloseGraphics after a cut first frame: %v, %s", err, term)
		}
	})
	t.Run("mouse enable cut", func(t *testing.T) {
		term := newTerm()
		term.cut = func(p []byte) int { return len("\x1b[?1002h") }
		r := panelOn(t, "recorded-crew-lifecycle", &cellRecorder{w: cw, h: ch}, term)
		r.send(mustTab("graph"), 4)
		if !term.mouse["1002"] {
			t.Fatalf("the cut enable did not reach the terminal")
		}
		r.send(key("tab"), 4)
		if !term.blank() {
			t.Errorf("leaving after a cut mouse enable: %s, log %q", term, term.log.String())
		}
	})
	t.Run("terminal gone: exit reports it", func(t *testing.T) {
		r := shown(t, &cellRecorder{w: cw, h: ch})
		r.term.dead = true
		if err := r.m.CloseGraphics(); err == nil {
			t.Errorf("CloseGraphics with an unwritable terminal and an image up returned nil")
		}
	})
}

func TestGraphTabDrillDownResetsModelAndTearsDownOnPop(t *testing.T) {
	ctl := &cellRecorder{w: cw, h: ch}
	r := panelOn(t, "edited-personal-cockpit", ctl, newTerm())
	// Show Hub's Graph tab first so Hub nodes (pi-05) are loaded into gfx.
	r.send(mustTab("graph"), 8)
	if !r.term.showing() {
		t.Fatalf("hub graph did not draw: %s", r.term)
	}
	r.send(mustTab("epics"), 4)
	if !r.term.blank() {
		t.Fatalf("leaving hub graph did not clear: %s", r.term)
	}
	// Drill into auth-refresh and feed the Epic stream.
	r.send(key("enter"), 4)
	for _, f := range frames(t, "edited-crew-attention") {
		r.send(StreamUpdate{Gen: 1, Update: adapter.FrameUpdate{Frame: f}}, 2)
	}
	r.send(mustTab("graph"), 8)
	if !r.term.showing() {
		t.Fatalf("epic graph did not draw: %s", r.term)
	}
	for _, n := range r.m.gfx.scene.Nodes {
		if n.ID == "pi-05" {
			t.Fatalf("hub node pi-05 bled into drilled-down epic graph: %+v", r.m.gfx.scene.Nodes)
		}
	}
	// Pop back directly from Epic's Graph tab: Kitty image & pixel mouse must clear.
	if out := r.send(key("esc"), 6); out != clearOff || !r.term.blank() || r.m.DrilledDown() || r.m.tab != TabEpics {
		t.Fatalf("esc from epic graph: out=%q term=%s drilled=%v tab=%v", out, r.term, r.m.DrilledDown(), r.m.tab)
	}
	// Returning to Hub Graph shows Hub node pi-05 and no Epic nodes.
	r.send(mustTab("graph"), 8)
	if !r.term.showing() {
		t.Fatalf("hub graph after pop did not draw: %s", r.term)
	}
	sawHub := false
	for _, n := range r.m.gfx.scene.Nodes {
		if n.ID == "pi-05" {
			sawHub = true
		}
		if n.ID == "claude-01" || n.ID == "claude-02" {
			t.Fatalf("epic node %s bled back into hub graph", n.ID)
		}
	}
	if !sawHub {
		t.Fatalf("hub node pi-05 missing after pop-back: %+v", r.m.gfx.scene.Nodes)
	}
}
