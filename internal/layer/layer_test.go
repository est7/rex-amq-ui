package layer

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/est7/rex-amq-ui/internal/rexctl"
	"github.com/est7/rex-amq-ui/internal/rexctl/rexctltest"
)

const (
	sess   = "session:01a1e4ff-0000-7000-8000-0000000000s1"
	win    = "window:01a1e4ff-0000-7000-8000-0000000000w1"
	paneA  = "block:01a1e4ff-0000-7000-8000-00000000000a"
	paneB  = "block:01a1e4ff-0000-7000-8000-00000000000b"
	panel  = "block:01a1e4ff-0000-7000-8000-0000000000pp"
	player = "layer:01a1e4ff-0000-7000-8000-0000000000pl"
)

// view is a session with one window split between paneA and paneB, and
// the panel's layer when withPanel.
func view(rev int64, focused string, withPanel bool, state rexctl.LayerState, owner string, bounds rexctl.Bounds) rexctl.View {
	tiled := rexctl.Layer{LayerID: "layer:tiled", Kind: rexctl.LayerTiled, Bounds: rexctl.Bounds{0, 0, 1, 1},
		Layout: &rexctl.LayoutNode{Rect: rexctl.Bounds{0, 0, 1, 1}, Split: &rexctl.Split{SplitID: "split:1", Direction: "horizontal", Ratio: 0.5,
			Before: &rexctl.LayoutNode{BlockID: paneA, Rect: rexctl.Bounds{0, 0, 0.5, 1}},
			After:  &rexctl.LayoutNode{BlockID: paneB, Rect: rexctl.Bounds{0.5, 0, 0.5, 1}}}},
		Blocks: []rexctl.Block{{BlockID: paneA, Label: "shell"}, {BlockID: paneB, Label: "shell"}}}
	w := rexctl.Window{WindowID: win, Label: "main", FocusedBlockID: focused, Layers: []rexctl.Layer{tiled}}
	if withPanel {
		w.Layers = append(w.Layers, rexctl.Layer{LayerID: player, Kind: rexctl.LayerFloating, Bounds: bounds, State: state,
			Layout: &rexctl.LayoutNode{BlockID: panel, Rect: rexctl.Bounds{0, 0, 1, 1}},
			Blocks: []rexctl.Block{{BlockID: panel, Label: Label, Owner: owner}}})
	}
	return rexctl.View{Revision: rev, SessionID: sess, Label: "auth", ActiveWindowID: win, Windows: []rexctl.Window{w}}
}

// halfWindow answers size for a pane half the window wide: the window is
// 200x50 cells.
func halfWindow(s *rexctltest.Server) {
	s.Handle("block.method", func(r rexctltest.Request) (any, *rexctltest.Error) {
		var p struct {
			Method string `json:"method"`
			Args   struct {
				Data string `json:"data"`
			} `json:"args"`
		}
		r.Decode(&p)
		if p.Method == "write" {
			return map[string]any{"bytes_written": len(p.Args.Data)}, nil
		}
		return map[string]any{"columns": 100, "rows": 50, "cell_width_px": 9, "cell_height_px": 22, "owner": "client:app"}, nil
	})
}

func dial(t *testing.T, s *rexctltest.Server) *rexctl.Client {
	t.Helper()
	c, err := rexctl.Dial(context.Background(), s.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func methods(s *rexctltest.Server) []string {
	var out []string
	for _, r := range s.Requests() {
		out = append(out, r.Method)
	}
	return out
}

func TestOpenCreatesSizedDisownedLayer(t *testing.T) {
	for _, size := range []Size{Drawer, Board} {
		s := rexctltest.NewServer(t)
		halfWindow(s)
		s.SetView(view(3, paneA, false, "", "", rexctl.Bounds{}))
		o, err := Open(context.Background(), dial(t, s), Launch{Session: sess, Board: size == Board, Tab: "members", Command: []string{"/bin/rex-amq-ui", "--adapter-bin", "/x/adapter"}})
		if err != nil || o.Restored || o.BlockID == "" {
			t.Fatalf("%v: %+v %v", size, o, err)
		}
		if got, want := methods(s), []string{"session.attach", "block.method", "session.new_layer", "session.disown_block"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%v: calls %v, want %v", size, got, want)
		}
		r, _ := s.Last("session.new_layer")
		var p struct {
			WindowID string        `json:"window_id"`
			Bounds   rexctl.Bounds `json:"bounds"`
			Focus    bool          `json:"focus"`
			Layout   struct {
				Block struct {
					Label   string `json:"label"`
					Options struct {
						Shell   string   `json:"shell"`
						Command []string `json:"command"`
						Size    struct {
							Columns, Rows int
						} `json:"size"`
					} `json:"options"`
				} `json:"block"`
			} `json:"layout"`
		}
		if err := r.Decode(&p); err != nil {
			t.Fatal(err)
		}
		b := p.Layout.Block
		wantCols, wantRows, wantBounds := 70, 48, rexctl.Bounds{0.64, 0.02, 0.35, 0.96}
		wantArgv := []string{"/bin/rex-amq-ui", "--adapter-bin", "/x/adapter", "--session", sess, "--focus", paneA, "--tab", "members"}
		if size == Board {
			wantCols, wantRows, wantBounds = 176, 44, rexctl.Bounds{0.06, 0.06, 0.88, 0.88}
			wantArgv = append(wantArgv, "--board")
		}
		if p.WindowID != win || p.Bounds != wantBounds || !p.Focus || b.Label != Label || b.Options.Shell != "none" {
			t.Errorf("%v: new_layer %+v", size, p)
		}
		if b.Options.Size.Columns != wantCols || b.Options.Size.Rows != wantRows {
			t.Errorf("%v: start size %dx%d, want %dx%d", size, b.Options.Size.Columns, b.Options.Size.Rows, wantCols, wantRows)
		}
		if !reflect.DeepEqual(b.Options.Command, wantArgv) {
			t.Errorf("%v: argv %q, want %q", size, b.Options.Command, wantArgv)
		}
	}
}

func TestOpenRestoresTheOpenPanel(t *testing.T) {
	s := rexctltest.NewServer(t)
	halfWindow(s)
	s.SetView(view(4, paneA, true, rexctl.LayerMinimized, "client:app", Drawer.Bounds()))
	o, err := Open(context.Background(), dial(t, s), Launch{Session: sess, Board: true, Tab: "mail", Command: []string{"/bin/rex-amq-ui"}})
	if err != nil || !o.Restored || o.BlockID != panel || o.LayerID != player {
		t.Fatalf("%+v %v", o, err)
	}
	if got, want := methods(s), []string{"session.attach", "session.resize_layer", "session.set_layer_state", "block.method"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
	r, _ := s.Last("block.method")
	var w struct {
		BlockID string `json:"block_id"`
		Args    struct {
			Data string `json:"data"`
		} `json:"args"`
	}
	r.Decode(&w)
	if w.BlockID != panel || w.Args.Data != "\x1b_amqui;resume\x1b\\\x1b_amqui;tab:mail\x1b\\" {
		t.Fatalf("wrote %q to %s", w.Args.Data, w.BlockID)
	}
	r, _ = s.Last("session.set_layer_state")
	var st struct {
		State string `json:"state"`
		Focus bool   `json:"focus"`
	}
	r.Decode(&st)
	if st.State != "normal" || !st.Focus {
		t.Fatalf("set_layer_state %+v", st)
	}
}

func TestOpenRejectsUnknownTab(t *testing.T) {
	s := rexctltest.NewServer(t)
	if _, err := Open(context.Background(), dial(t, s), Launch{Session: sess, Tab: "inbox"}); err == nil || len(s.Requests()) != 0 {
		t.Fatalf("err %v, requests %v", err, methods(s))
	}
}

func TestPanelLeavesInOrder(t *testing.T) {
	s := rexctltest.NewServer(t)
	s.SetView(view(5, panel, true, "", "client:app", Drawer.Bounds()))
	p, err := Connect(context.Background(), s.URL, sess, panel, paneB)
	if err != nil {
		t.Fatal(err)
	}
	if st := p.State(); st.Focused != paneB || st.Owner == "" || st.Minimized || st.Size != Drawer {
		t.Fatalf("state %+v: the panel's own focus must not replace the pane it was opened over", st)
	}
	before := len(s.Requests())
	if err := p.Jump(context.Background(), paneA); err != nil {
		t.Fatal(err)
	}
	if got := methods(s)[before:]; !reflect.DeepEqual(got, []string{"session.set_layer_state", "session.focus_block"}) {
		t.Fatalf("jump calls %v", got)
	}

	s.Handle("client.perform", func(rexctltest.Request) (any, *rexctltest.Error) {
		return nil, &rexctltest.Error{Code: "refused", Message: "remote control is off", Status: 403}
	})
	before = len(s.Requests())
	if err := p.SwitchTo(context.Background(), "session:other"); !errors.Is(err, ErrRemoteControlOff) {
		t.Fatalf("refused switch: %v", err)
	}
	if got := methods(s)[before:]; slices.Contains(got, "session.set_layer_state") {
		t.Fatalf("a refused switch minimized the panel: %v", got)
	}
}

func TestWatchFollowsFocusOwnerAndState(t *testing.T) {
	s := rexctltest.NewServer(t)
	s.SetView(view(5, panel, true, "", "", Drawer.Bounds()))
	p, err := Connect(context.Background(), s.URL, sess, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Block() != panel {
		t.Fatalf("found block %q by label", p.Block())
	}
	var mu sync.Mutex
	var got []State
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Watch(ctx, func(st State) { mu.Lock(); got = append(got, st); mu.Unlock() })
		close(done)
	}()
	waitFor := func(what string, ok func(State) bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			hit := len(got) > 0 && ok(got[len(got)-1])
			mu.Unlock()
			if hit {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("%s: states %+v", what, got)
	}
	waitFor("initial", func(st State) bool { return st.Connected && st.Owner == "" && st.Focused == "" })

	s.Emit(sess, rexctl.EventBlockOwnerChanged, map[string]any{"block_id": panel, "owner": "client:app", "reason": "take"})
	waitFor("owner claimed", func(st State) bool { return st.Owner != "" })

	s.SetView(view(6, paneB, true, rexctl.LayerMinimized, "client:app", Board.Bounds()))
	s.Emit(sess, rexctl.EventSessionViewChanged, map[string]any{"revision": 6})
	waitFor("focus, minimized, board", func(st State) bool { return st.Focused == paneB && st.Minimized && st.Size == Board })

	// Back to normal with the panel focused: the pane stays paneB.
	s.SetView(view(7, panel, true, "", "client:app", Board.Bounds()))
	s.Emit(sess, rexctl.EventSessionViewChanged, map[string]any{"revision": 7})
	waitFor("restored", func(st State) bool { return !st.Minimized && st.Focused == paneB })

	// A stale revision is ignored; the owner change after it is a barrier.
	s.SetView(view(3, paneA, true, rexctl.LayerMinimized, "client:app", Drawer.Bounds()))
	s.Emit(sess, rexctl.EventSessionViewChanged, map[string]any{"revision": 3})
	s.Emit(sess, rexctl.EventBlockOwnerChanged, map[string]any{"block_id": panel, "previous": "client:app", "reason": "lost"})
	waitFor("owner lost", func(st State) bool { return st.Owner == "" })
	mu.Lock()
	if last := got[len(got)-1]; last.Minimized || last.Focused != paneB || last.Size != Board {
		t.Fatalf("a stale view was applied: %+v", last)
	}
	mu.Unlock()

	// The connection drops; Watch reconnects, attaches again, rereads.
	s.SetView(view(9, paneA, true, "", "client:app", Drawer.Bounds()))
	s.CloseConnections()
	waitFor("reconnected", func(st State) bool { return st.Connected && st.Focused == paneA && st.Size == Drawer && st.Owner != "" })
	attaches := 0
	for _, m := range methods(s) {
		if m == "session.attach" {
			attaches++
		}
	}
	if attaches < 2 {
		t.Fatalf("reconnect did not attach again: %v", methods(s))
	}
	sawDown := false
	mu.Lock()
	for _, st := range got {
		sawDown = sawDown || !st.Connected
	}
	mu.Unlock()
	if !sawDown {
		t.Fatal("no disconnected state was sent while Rex was away")
	}
	cancel()
	<-done
}

func TestPanelFollowsItsLayerWhenMoved(t *testing.T) {
	s := rexctltest.NewServer(t)
	s.SetView(view(5, panel, true, "", "client:app", Drawer.Bounds()))
	p, err := Connect(context.Background(), s.URL, sess, panel, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	moved := make(chan struct{}, 1)
	go p.Watch(ctx, func(st State) {
		if st.Size == Board {
			moved <- struct{}{}
		}
	})
	// Lua moved the panel to another tab: move_block + float_block make it
	// a new layer.
	v := view(6, panel, true, "", "client:app", Board.Bounds())
	v.Windows[0].Layers[1].LayerID = "layer:moved"
	s.SetView(v)
	s.Emit(sess, rexctl.EventSessionViewChanged, map[string]any{"revision": 6})
	select {
	case <-moved:
	case <-time.After(5 * time.Second):
		t.Fatal("the move was not seen")
	}
	if err := p.Minimize(context.Background()); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Last("session.set_layer_state")
	var got struct {
		LayerID string `json:"layer_id"`
	}
	r.Decode(&got)
	if got.LayerID != "layer:moved" {
		t.Fatalf("minimized %q, the panel's old layer", got.LayerID)
	}
}

func TestOpenKeepsARestoredPanelsSize(t *testing.T) {
	for _, c := range []struct {
		name  string
		at    rexctl.Bounds
		board bool
		want  []string
	}{
		{"zoomed panel, plain open", Board.Bounds(), false, []string{"session.attach", "session.set_layer_state", "block.method"}},
		{"drawer, --board", Drawer.Bounds(), true, []string{"session.attach", "session.resize_layer", "session.set_layer_state", "block.method"}},
		{"board moved a little, --board", rexctl.Bounds{0.05, 0.06, 0.88, 0.88}, true, []string{"session.attach", "session.set_layer_state", "block.method"}},
	} {
		s := rexctltest.NewServer(t)
		halfWindow(s)
		s.SetView(view(4, paneA, true, rexctl.LayerMinimized, "client:app", c.at))
		if _, err := Open(context.Background(), dial(t, s), Launch{Session: sess, Board: c.board, Command: []string{"/bin/rex-amq-ui"}}); err != nil {
			t.Fatal(err)
		}
		if got := methods(s); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: calls %v, want %v", c.name, got, c.want)
		}
	}
}

func TestWatchMakesTheFirstConnection(t *testing.T) {
	p := New("unix:///nonexistent/rex.sock", sess, panel, paneA)
	if err := p.Minimize(context.Background()); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("before any connection: %v", err)
	}
	s := rexctltest.NewServer(t)
	s.SetView(view(5, panel, true, "", "client:app", Drawer.Bounds()))
	p = New(s.URL, sess, panel, paneA)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	up := make(chan State, 8)
	go p.Watch(ctx, func(st State) { up <- st })
	deadline := time.After(5 * time.Second)
	for {
		select {
		case st := <-up:
			if st.Connected {
				if st.Owner != "client:app" || st.Focused != paneA {
					t.Fatalf("state %+v", st)
				}
				if err := p.Minimize(context.Background()); err != nil {
					t.Fatal(err)
				}
				return
			}
		case <-deadline:
			t.Fatal("Watch never connected")
		}
	}
}

func TestOpenBesideClosesABlockItCouldNotPlace(t *testing.T) {
	s := rexctltest.NewServer(t)
	s.SetView(view(5, panel, true, "", "client:app", Drawer.Bounds()))
	const made = "block:01a1e4ff-0000-7000-8000-0000000000ed"
	s.Handle("session.new_block", func(rexctltest.Request) (any, *rexctltest.Error) {
		return map[string]any{"block_id": made, "revision": 5}, nil
	})
	s.Handle("session.move_block", func(rexctltest.Request) (any, *rexctltest.Error) {
		return nil, &rexctltest.Error{Code: "not_found", Message: "no anchor block", Status: 404}
	})
	closed := make(chan string, 1)
	s.Handle("block.close", func(r rexctltest.Request) (any, *rexctltest.Error) {
		var p struct {
			BlockID string `json:"block_id"`
		}
		r.Decode(&p)
		closed <- p.BlockID
		return map[string]any{"revision": 6}, nil
	})
	p, err := Connect(context.Background(), s.URL, sess, panel, "")
	if err != nil {
		t.Fatal(err)
	}
	// The caller's context is already done when placement fails: cleanup
	// must not depend on it.
	ctx, cancel := context.WithCancel(context.Background())
	s.Handle("session.move_block", func(rexctltest.Request) (any, *rexctltest.Error) {
		cancel()
		return nil, &rexctltest.Error{Code: "not_found", Message: "no anchor block", Status: 404}
	})
	err = p.OpenBeside(ctx, "block:gone", "hand-back", []string{"nvim", "/tmp/x.md"})
	if !rexctl.IsCode(err, rexctl.CodeNotFound) && !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v: the placement failure must be reported", err)
	}
	select {
	case got := <-closed:
		if got != made {
			t.Fatalf("closed %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the unplaced block was left running")
	}
}

func TestGraphTabCellSizeFromBlock(t *testing.T) {
	s := rexctltest.NewServer(t)
	halfWindow(s)
	s.SetView(view(3, paneA, true, rexctl.LayerNormal, "client:app", Drawer.Bounds()))
	p, err := Connect(context.Background(), s.URL, sess, panel, paneA)
	if err != nil {
		t.Fatal(err)
	}
	w, h, err := p.CellSize(context.Background())
	if err != nil || w != 9 || h != 22 {
		t.Fatalf("CellSize = %d x %d, %v; want the block's 9 x 22", w, h, err)
	}
	r, _ := s.Last("block.method")
	var call struct {
		BlockID string `json:"block_id"`
		Method  string `json:"method"`
	}
	if err := r.Decode(&call); err != nil || call.BlockID != panel || call.Method != "size" {
		t.Errorf("CellSize asked %+v (%v), want the panel's own block's size", call, err)
	}

	s.Handle("block.method", func(rexctltest.Request) (any, *rexctltest.Error) {
		return map[string]any{"columns": 70, "rows": 48}, nil
	})
	if _, _, err := p.CellSize(context.Background()); err == nil {
		t.Errorf("a size without cell pixels was accepted")
	}
	if _, _, err := New(s.URL, sess, panel, "").CellSize(context.Background()); !errors.Is(err, ErrDisconnected) {
		t.Errorf("CellSize before connecting: %v, want ErrDisconnected", err)
	}
}
