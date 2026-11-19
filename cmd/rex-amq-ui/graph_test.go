package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/est7/rex-amq-ui/internal/adapter"
	"github.com/est7/rex-amq-ui/internal/apc"
	"github.com/est7/rex-amq-ui/internal/graph"
	"github.com/est7/rex-amq-ui/internal/layer"
	"github.com/est7/rex-amq-ui/internal/pixelmouse"
	"github.com/est7/rex-amq-ui/internal/raster"
	"github.com/est7/rex-amq-ui/internal/rexctl"
	"github.com/est7/rex-amq-ui/internal/rexctl/rexctltest"
	"github.com/est7/rex-amq-ui/internal/ui"
)

const (
	gSession = "session:s"
	gPanel   = "block:panel"
)

// graphRex is a fake Rex with the fixture's panes tiled and the panel's
// layer floating, its block measuring 9x22-pixel cells.
func graphRex(t *testing.T) *rexctltest.Server {
	s := rexctltest.NewServer(t)
	tiled := rexctl.Layer{LayerID: "layer:tiled", Kind: rexctl.LayerTiled, Bounds: rexctl.Bounds{0, 0, 1, 1},
		Layout: &rexctl.LayoutNode{BlockID: "block:claude-01", Rect: rexctl.Bounds{0, 0, 1, 1}},
		Blocks: []rexctl.Block{{BlockID: "block:claude-01"}, {BlockID: "block:claude-02"}}}
	board := layer.Board.Bounds()
	panel := rexctl.Layer{LayerID: "layer:panel", Kind: rexctl.LayerFloating, Bounds: board,
		Layout: &rexctl.LayoutNode{BlockID: gPanel, Rect: rexctl.Bounds{0, 0, 1, 1}},
		Blocks: []rexctl.Block{{BlockID: gPanel, Label: layer.Label, Owner: "client:app"}}}
	s.SetView(rexctl.View{Revision: 1, SessionID: gSession, ActiveWindowID: "window:1", Windows: []rexctl.Window{{
		WindowID: "window:1", FocusedBlockID: gPanel, Layers: []rexctl.Layer{tiled, panel}}}})
	s.Handle("block.method", func(rexctltest.Request) (any, *rexctltest.Error) {
		return map[string]any{"columns": 120, "rows": 40, "cell_width_px": 9, "cell_height_px": 22, "owner": "client:app"}, nil
	})
	return s
}

// nodeCenter is where the Graph tab draws id's badge for the fixture at
// w x h cells, in block pixels: the same graph the panel builds.
func nodeCenter(t *testing.T, fixture string, w, h int, id string) (int, int) {
	t.Helper()
	f, err := os.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var st adapter.State
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		fr, err := adapter.DecodeFrame(sc.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		st = st.Apply(fr)
	}
	m, _ := ui.New(ui.Deps{}, ui.Options{}).Update(tea.WindowSizeMsg{Width: w, Height: h})
	r := m.(ui.Model).GraphRegion()
	g := graph.New(time.Now)
	g.Update(st)
	for _, n := range g.Scene(graph.Size{Cols: r.W, Rows: r.H, Cell: raster.Metrics{CellW: 9, CellH: 22}}).Nodes {
		if n.ID == id {
			c := n.Rect.Center()
			return r.X*9 + int(c.X), r.Y*22 + int(c.Y)
		}
	}
	t.Fatalf("no node %s", id)
	return 0, 0
}

// kittyTerm is the panel's terminal output that reads each t=t frame
// file as Kitty does (deleting it), so frames keep flowing.
type kittyTerm struct{ syncBuffer }

var framePath = regexp.MustCompile(`\x1b_Ga=T,[^;]*;([A-Za-z0-9+/=]+)\x1b\\`)

func (k *kittyTerm) Write(p []byte) (int, error) {
	for _, m := range framePath.FindAllSubmatch(p, -1) {
		if path, err := base64.StdEncoding.DecodeString(string(m[1])); err == nil {
			os.Remove(string(path))
		}
	}
	return k.syncBuffer.Write(p)
}

var screenCmd = regexp.MustCompile(`\x1b_Ga=T,f=32,t=t,s=\d+,v=\d+,i=(\d),|\x1b_Ga=d,d=I,i=(\d),q=2\x1b\\|(\x1b_Ga=d,d=A,q=2\x1b\\)|\x1b\[\?(1016)([hl])`)

// screen is what out leaves on the terminal: images up, and whether pixel
// mouse reports are on.
func screen(out string) (images int, pixelMouse bool) {
	up := map[string]bool{}
	for _, m := range screenCmd.FindAllStringSubmatch(out, -1) {
		switch {
		case m[1] != "":
			up[m[1]] = true
		case m[2] != "":
			delete(up, m[2])
		case m[3] != "":
			clear(up)
		case m[4] != "":
			pixelMouse = m[5] == "h"
		}
	}
	return len(up), pixelMouse
}

func methodsOf(s *rexctltest.Server) string {
	var out []string
	for _, r := range s.Requests() {
		out = append(out, r.Method)
	}
	return strings.Join(out, " ")
}

// TestGraphTabProgram runs the real panel on pipes against a fake Rex and
// adapter on the Graph tab: the image goes out through Kitty with pixel
// mouse reports on, a pixel click on a member's badge selects it and a
// second one goes to its pane (minimize, then focus), the image is cleared
// as the panel hides, and quitting leaves no frame files.
func TestGraphTabProgram(t *testing.T) {
	dir := t.TempDir()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "panel", "edited-crew-attention.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "rex-amq-adapter")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat '"+fixture+"'\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	frames := filepath.Join(dir, "frames")
	if err := os.Mkdir(frames, 0o700); err != nil {
		t.Fatal(err)
	}
	rex := graphRex(t)

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inW.Close()
	out := &kittyTerm{}
	errc := make(chan error, 1)
	go func() {
		errc <- runPanel(context.Background(), panelConfig{
			Server: rex.URL, Session: gSession, Block: gPanel, AdapterBin: script, In: inR, Out: out,
			Width: 120, Height: 40, Options: ui.Options{Tab: "graph"},
			Graphics: &ui.Graphics{Dir: frames, PID: 4242},
		})
	}()
	has := func(s string) func() bool {
		return func() bool { raw, _ := out.snapshot(); return strings.Contains(raw, s) }
	}
	waitFor(t, "the Kitty frame", has("\x1b_Ga=T,f=32,t=t,s=1062,v=704,"))
	waitFor(t, "pixel mouse on", has(pixelmouse.Enable))
	if raw, _ := out.snapshot(); !strings.Contains(raw, "\x1b7\x1b[5;2H\x1b_Ga=T") {
		t.Fatalf("the frame is not placed at the region's top-left cell (row 5, column 2)")
	}

	// The first frame can beat the adapter's snapshot; click once the
	// whole stream is in and a frame of it is out.
	waitFor(t, "the stream on screen", func() bool { raw, _ := out.snapshot(); return strings.Contains(ansi.Strip(raw), "3 / 3 messages") })
	_, n := out.snapshot()
	waitFor(t, "a frame of the stream", func() bool {
		raw, _ := out.snapshot()
		return strings.Contains(raw[n:], "\x1b_Ga=T")
	})
	x, y := nodeCenter(t, fixture, 120, 40, "claude-02")
	click := fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x+1, y+1, x+1, y+1)
	inW.WriteString(click)
	time.Sleep(200 * time.Millisecond)
	if m := methodsOf(rex); strings.Contains(m, "session.focus_block") {
		t.Fatalf("the first click went to the pane: %s", m)
	}
	inW.WriteString(click)
	waitFor(t, "the second click to focus claude-02's pane", func() bool {
		r, ok := rex.Last("session.focus_block")
		var p struct {
			BlockID string `json:"block_id"`
		}
		return ok && r.Decode(&p) == nil && p.BlockID == "block:claude-02"
	})
	if m := methodsOf(rex); !strings.Contains(m, "session.set_layer_state session.focus_block") {
		t.Errorf("calls %s: want minimize, then focus", m)
	}
	waitFor(t, "the image cleared as the panel hides", func() bool {
		raw, _ := out.snapshot()
		n, mouse := screen(raw)
		return n == 0 && !mouse
	})

	_, n = out.snapshot()
	inW.WriteString(apc.Encode(apc.Command{Verb: apc.Resume}))
	waitFor(t, "the image back on resume", func() bool {
		raw, _ := out.snapshot()
		return strings.Contains(raw[n:], "\x1b_Ga=T")
	})
	inW.WriteString("Q")
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("panel ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Q did not quit the panel")
	}
	raw, _ := out.snapshot()
	if n, mouse := screen(raw); n != 0 || mouse {
		t.Errorf("after quitting on the Graph tab: %d images up, pixel mouse %v; tail %q", n, mouse, raw[max(0, len(raw)-120):])
	}
	if left, _ := filepath.Glob(filepath.Join(frames, "*.rgba")); len(left) != 0 {
		t.Errorf("frame files left after exit: %v", left)
	}
}

type panicNow struct{}

// panicky is the panel with a fault: it panics on panicNow.
type panicky struct{ tea.Model }

func (p panicky) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(panicNow); ok {
		panic("injected fault")
	}
	m, cmd := p.Model.Update(msg)
	return panicky{m}, cmd
}

// TestGraphTabProgramPanicCleansUp: a panic in Update after the image is
// up ends the panel with the panic as its error, the image cleared, pixel
// mouse off and no frame files left.
func TestGraphTabProgramPanicCleansUp(t *testing.T) {
	if raceDetector {
		// Bubble Tea v2.1.1 closes its cancel reader on a panic shutdown
		// without waiting for the read loop (tea.go:1304 against
		// cancelreader_bsd.go:141): -race flags it about once in ten runs.
		// The panel's own part, endPanel, is covered under -race by
		// TestGraphTabEndPanelKeepsPanic. Remove this skip when Bubble Tea
		// waits for its read loop before closing on a panic.
		t.Skip("Bubble Tea's panic shutdown races its input reader under -race")
	}
	dir := t.TempDir()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "panel", "edited-crew-attention.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "rex-amq-adapter")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat '"+fixture+"'\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	frames := filepath.Join(dir, "frames")
	if err := os.Mkdir(frames, 0o700); err != nil {
		t.Fatal(err)
	}
	rex := graphRex(t)
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inW.Close()
	out := &syncBuffer{} // reads no frame file: one stays behind unless cleaned up
	errc := make(chan error, 1)
	progc := make(chan *tea.Program, 1)
	go func() {
		errc <- runPanel(context.Background(), panelConfig{
			Server: rex.URL, Session: gSession, Block: gPanel, AdapterBin: script, In: inR, Out: out,
			Width: 120, Height: 40, Options: ui.Options{Tab: "graph"},
			Graphics: &ui.Graphics{Dir: frames, PID: 4343},
			started:  func(p *tea.Program) { progc <- p },
			wrap:     func(m tea.Model) tea.Model { return panicky{m} },
		})
	}()
	prog := <-progc
	waitFor(t, "the Kitty frame", func() bool { raw, _ := out.snapshot(); return strings.Contains(raw, "\x1b_Ga=T") })
	prog.Send(panicNow{})
	select {
	case err := <-errc:
		if !errors.Is(err, tea.ErrProgramPanic) {
			t.Errorf("panel ended with %v, want the panic (tea.ErrProgramPanic)", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the panic did not end the panel")
	}
	raw, _ := out.snapshot()
	if n, mouse := screen(raw); n != 0 || mouse {
		t.Errorf("after the panic: %d images up, pixel mouse %v", n, mouse)
	}
	if left, _ := filepath.Glob(filepath.Join(frames, "*.rgba")); len(left) != 0 {
		t.Errorf("frame files left after the panic: %v", left)
	}
}

func TestGraphTabEndPanelKeepsPanic(t *testing.T) {
	live := context.Background()
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	m := ui.New(ui.Deps{}, ui.Options{})
	panicked := fmt.Errorf("%w: %w", tea.ErrProgramKilled, tea.ErrProgramPanic)
	for _, c := range []struct {
		name   string
		parent context.Context
		err    error
		want   error
	}{
		{"panic, caller still running", live, panicked, tea.ErrProgramPanic},
		{"panic while the caller cancels", gone, panicked, tea.ErrProgramPanic},
		{"killed by the caller's cancellation", gone, tea.ErrProgramKilled, nil},
		{"killed with the caller running", live, tea.ErrProgramKilled, tea.ErrProgramKilled},
		{"quit", live, nil, nil},
	} {
		got := endPanel(c.parent, c.err, m)
		if (c.want == nil) != (got == nil) || (c.want != nil && !errors.Is(got, c.want)) {
			t.Errorf("%s: endPanel = %v, want %v", c.name, got, c.want)
		}
	}
}
