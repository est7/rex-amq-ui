package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	out := &syncBuffer{}
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
	waitFor(t, "the Kitty frame", has("\x1b_Ga=T,f=32,t=t,s=1080,v=748,"))
	waitFor(t, "pixel mouse on", has(pixelmouse.Enable))
	if raw, _ := out.snapshot(); !strings.Contains(raw, "\x1b7\x1b[4;1H\x1b_Ga=T") {
		t.Fatalf("the frame is not placed at the region's top-left cell (row 4, column 1)")
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
	waitFor(t, "the image cleared as the panel hides", has("\x1b_Ga=d,d=A,q=2\x1b\\"+pixelmouse.Disable))

	inW.WriteString(apc.Encode(apc.Command{Verb: apc.Resume}))
	time.Sleep(300 * time.Millisecond)
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
	if !strings.HasSuffix(raw, "\x1b_Ga=d,d=A,q=2\x1b\\"+pixelmouse.Disable) {
		t.Errorf("output ends %q: quitting on the Graph tab must clear the image and turn pixel mouse off last", raw[max(0, len(raw)-80):])
	}
	if left, _ := filepath.Glob(filepath.Join(frames, "*.rgba")); len(left) != 0 {
		t.Errorf("frame files left after exit: %v", left)
	}
}
