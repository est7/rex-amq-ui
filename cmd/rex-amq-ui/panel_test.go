package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/est7/rex-amq-ui/internal/apc"
	"github.com/est7/rex-amq-ui/internal/layer"
	"github.com/est7/rex-amq-ui/internal/rexctl"
	"github.com/est7/rex-amq-ui/internal/rexctl/rexctltest"
	"github.com/est7/rex-amq-ui/internal/ui"
)

// syncBuffer is the panel's terminal output.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) snapshot() (raw string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String(), s.b.Len()
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestPanelProgram runs the real panel (Bubble Tea program, APC input
// filter, adapter stream) on pipes against a fake adapter that sends its
// snapshot, then the rest of its stream once the test says so.
func TestPanelProgram(t *testing.T) {
	// The panel forces TrueColor: an environment that asks for no colour
	// must not strip its backgrounds.
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	dir := t.TempDir()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "panel", "edited-crew-attention.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	gate, done := filepath.Join(dir, "go"), filepath.Join(dir, "done")
	script := filepath.Join(dir, "rex-amq-adapter")
	body := "#!/bin/sh\nhead -n 1 '" + fixture + "'\nwhile [ ! -e '" + gate + "' ]; do sleep 0.02; done\n" +
		"tail -n +2 '" + fixture + "'\ntouch '" + done + "'\nexec sleep 60\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inW.Close()
	out := &syncBuffer{}
	errc := make(chan error, 1)
	progc := make(chan *tea.Program, 1)
	go func() {
		errc <- runPanel(context.Background(), panelConfig{
			Session: "session:s", AdapterBin: script, In: inR, Out: out, Width: 70, Height: 48,
			Options: ui.Options{}, started: func(p *tea.Program) { progc <- p },
		})
	}()
	prog := <-progc
	shows := func(s string) func() bool {
		return func() bool { raw, _ := out.snapshot(); return strings.Contains(ansi.Strip(raw), s) }
	}
	waitFor(t, "the snapshot on screen", shows("auth-refresh"))
	waitFor(t, "the claude-02 turn error alert", shows("hit a turn error"))

	inW.WriteString(apc.Encode(apc.Command{Verb: apc.Pause}))
	time.Sleep(300 * time.Millisecond) // the pause lands; a last frame may flush
	_, before := out.snapshot()
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the rest of the stream", func() bool { _, err := os.Stat(done); return err == nil })
	// Rex resizes a hidden layer's block when its window changes size.
	prog.Send(tea.WindowSizeMsg{Width: 80, Height: 40})
	time.Sleep(400 * time.Millisecond)
	if _, after := out.snapshot(); after != before {
		raw, _ := out.snapshot()
		t.Fatalf("the paused panel wrote %d bytes: %q", after-before, raw[before:])
	}
	if shows("is waiting on you")() {
		t.Fatal("frames reached the screen while paused")
	}

	inW.WriteString(apc.Encode(apc.Command{Verb: apc.Resume}))
	waitFor(t, "resume to draw the frames folded while paused", shows("is waiting on you"))

	// A sequence split across writes, then keys: tab:members, Q.
	inW.WriteString("\x1b_amqui;tab:mem")
	time.Sleep(100 * time.Millisecond)
	inW.WriteString("bers\x1b\\")
	waitFor(t, "tab:members", shows("Retired 1"))

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
	if strings.Contains(raw, "amqui") || strings.Contains(raw, "\x1b_") {
		t.Fatal("APC bytes reached the panel's output")
	}
	if !strings.Contains(raw, "48;2;") {
		t.Fatal("no truecolor background in the output")
	}
	seen, bad := erases(raw)
	if bad != nil {
		t.Fatalf("erases on the default background: %v", bad)
	}
	t.Logf("line erases the renderer used, all under a set background: %v", seen)
}

// sgrBackground applies an SGR's parameters to bg (whether a background
// colour is set), refusing what it does not model: reverse video, and
// extended colours whose arguments are not 5;n or 2;r;g;b in 0..255.
func sgrBackground(params string, bg *bool) error {
	ps := strings.Split(params, ";")
	num := func(s string) int {
		n, err := strconv.Atoi(s)
		if err != nil {
			return -1
		}
		return n
	}
	for i := 0; i < len(ps); i++ {
		p := num(ps[i])
		if ps[i] == "" {
			p = 0
		}
		switch {
		case p == 0 || p == 49:
			*bg = false
		case p >= 40 && p <= 47, p >= 100 && p <= 107:
			*bg = true
		case p == 38 || p == 48 || p == 58:
			rest := ps[i+1:]
			args := 0
			switch {
			case len(rest) >= 2 && rest[0] == "5":
				args = 2
			case len(rest) >= 4 && rest[0] == "2":
				args = 4
			default:
				return fmt.Errorf("SGR %q: extended colour without 5;n or 2;r;g;b", params)
			}
			for _, a := range rest[1:args] {
				if v := num(a); v < 0 || v > 255 {
					return fmt.Errorf("SGR %q: colour argument %q", params, a)
				}
			}
			if p == 48 {
				*bg = true
			}
			i += args
		case p == 7:
			return fmt.Errorf("SGR %q: reverse video is not modelled", params)
		case p >= 1 && p <= 9, p >= 21 && p <= 29, p >= 30 && p <= 37, p == 39, p == 59, p >= 90 && p <= 97:
		default:
			return fmt.Errorf("SGR %q: unmodelled parameter %q", params, ps[i])
		}
	}
	return nil
}

func TestErasesCheckDetectsDefaultBackground(t *testing.T) {
	if _, bad := erases("\x1b[48;2;1;2;3mab\x1b[38;2;49;0;0mc\x1b[>4;1m\x1b[K"); bad != nil {
		t.Fatalf("an erase under a background flagged: %v", bad)
	}
	for _, raw := range []string{"ab\x1b[K", "\x1b[48;2;1;2;3mab\x1b[m\x1b[5X", "\x1b[48;5;3ma\x1b[49m\x1b[K",
		"\x1b[10m\x1b[K",                   // a font, not a background
		"\x1b[48;2;1;2;3m\x1b[7m\x1b[K",    // reverse video
		"\x1b[48;2;1;2m\x1b[K",             // truncated colour
		"\x1b[48;2;1;2;300m\x1b[K",         // out of range
		"\x1b[48;2;1;2;3m\x1b[53m\x1b[K"} { // unmodelled

		if _, bad := erases(raw); bad == nil {
			t.Errorf("%q: not detected", raw)
		}
	}
}

var csi = regexp.MustCompile(`\x1b\[([0-9;:?]*)([@-~])`)

// erases checks that the renderer erases within a line (EL K, ECH X) only
// while a background colour is set: the cells it clears then keep the
// panel's background on a terminal with bce (Rex's TERM, xterm-256color,
// declares it). It returns the erase sequences seen and those that erase
// on the default background.
func erases(raw string) (seen map[string]int, bad []string) {
	seen = map[string]int{}
	bg := false
	for _, m := range csi.FindAllStringSubmatch(raw, -1) {
		params, final := m[1], m[2]
		switch final {
		case "m":
			if strings.ContainsAny(params[:min(len(params), 1)], "<=>?") {
				continue // a private mode (XTMODKEYS), not SGR
			}
			if err := sgrBackground(params, &bg); err != nil {
				bad = append(bad, err.Error())
			}
		case "K", "X":
			seen["CSI "+params+final]++
			if !bg {
				bad = append(bad, "CSI "+params+final)
			}
		}
	}
	return seen, bad
}

func TestOpenExitCodes(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	rex := env(map[string]string{"REX_SERVER": "unix:///nonexistent/rex.sock", "REX_SESSION": "session:s"})
	for _, c := range []struct {
		name string
		args []string
		env  func(string) string
		want int
	}{
		{"help", []string{"--help"}, rex, 0},
		{"unknown flag", []string{"--nope"}, rex, 2},
		{"unknown tab", []string{"--tab", "inbox"}, rex, 2},
		{"no server", nil, env(map[string]string{"REX_SESSION": "session:s"}), 2},
		{"no session", nil, env(map[string]string{"REX_SERVER": "unix:///x.sock"}), 2},
		{"Rex unreachable", []string{"--tab", "mail"}, rex, 1},
	} {
		var out, errb bytes.Buffer
		if got := runOpen(c.args, c.env, t.TempDir(), &out, &errb); got != c.want || out.Len() != 0 {
			t.Errorf("%s: exit %d stdout %q, want exit %d and no stdout", c.name, got, out.String(), c.want)
		}
	}
}

func TestOpenUsesPersistedSizeHermetically(t *testing.T) {
	openView := func(withPanel bool, bounds rexctl.Bounds) rexctl.View {
		tiled := rexctl.Layer{LayerID: "layer:tiled", Kind: rexctl.LayerTiled, Bounds: rexctl.Bounds{0, 0, 1, 1},
			Layout: &rexctl.LayoutNode{BlockID: "block:a", Rect: rexctl.Bounds{0, 0, 1, 1}},
			Blocks: []rexctl.Block{{BlockID: "block:a"}}}
		w := rexctl.Window{WindowID: "window:1", FocusedBlockID: "block:a", Layers: []rexctl.Layer{tiled}}
		if withPanel {
			w.Layers = append(w.Layers, rexctl.Layer{LayerID: "layer:p", Kind: rexctl.LayerFloating, Bounds: bounds,
				State:  rexctl.LayerMinimized,
				Layout: &rexctl.LayoutNode{BlockID: "block:p", Rect: rexctl.Bounds{0, 0, 1, 1}},
				Blocks: []rexctl.Block{{BlockID: "block:p", Label: layer.Label, Owner: "client:app"}}})
		}
		return rexctl.View{Revision: 1, SessionID: "session:s", ActiveWindowID: "window:1", Windows: []rexctl.Window{w}}
	}
	handleBlockMethod := func(s *rexctltest.Server) {
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
			return map[string]any{"columns": 200, "rows": 50, "cell_width_px": 9, "cell_height_px": 22, "owner": "client:app"}, nil
		})
	}
	envFor := func(url string) func(string) string {
		kv := map[string]string{"REX_SERVER": url, "REX_SESSION": "session:s"}
		return func(k string) string { return kv[k] }
	}

	t.Run("new layer uses persisted board size", func(t *testing.T) {
		cache := t.TempDir()
		if err := layer.SaveSize(cache, layer.Board); err != nil {
			t.Fatal(err)
		}
		s := rexctltest.NewServer(t)
		handleBlockMethod(s)
		s.SetView(openView(false, rexctl.Bounds{}))
		var out, errb bytes.Buffer
		if code := runOpen(nil, envFor(s.URL), cache, &out, &errb); code != 0 {
			t.Fatalf("exit %d stderr %q", code, errb.String())
		}
		r, ok := s.Last("session.new_layer")
		if !ok {
			t.Fatal("expected session.new_layer")
		}
		var p struct {
			Bounds rexctl.Bounds `json:"bounds"`
		}
		r.Decode(&p)
		if p.Bounds != layer.Board.Bounds() {
			t.Fatalf("new_layer bounds %v, want Board %v", p.Bounds, layer.Board.Bounds())
		}
	})

	t.Run("restored layer keeps current bounds even when state.json says board", func(t *testing.T) {
		cache := t.TempDir()
		if err := layer.SaveSize(cache, layer.Board); err != nil {
			t.Fatal(err)
		}
		s := rexctltest.NewServer(t)
		handleBlockMethod(s)
		s.SetView(openView(true, layer.Drawer.Bounds()))
		var out, errb bytes.Buffer
		if code := runOpen(nil, envFor(s.URL), cache, &out, &errb); code != 0 {
			t.Fatalf("exit %d stderr %q", code, errb.String())
		}
		if _, resized := s.Last("session.resize_layer"); resized {
			t.Fatal("restoring without --board resized an already-open layer")
		}
	})

	t.Run("explicit --board overrides persisted drawer size", func(t *testing.T) {
		cache := t.TempDir()
		if err := layer.SaveSize(cache, layer.Drawer); err != nil {
			t.Fatal(err)
		}
		s := rexctltest.NewServer(t)
		handleBlockMethod(s)
		s.SetView(openView(false, rexctl.Bounds{}))
		var out, errb bytes.Buffer
		if code := runOpen([]string{"--board"}, envFor(s.URL), cache, &out, &errb); code != 0 {
			t.Fatalf("exit %d stderr %q", code, errb.String())
		}
		r, _ := s.Last("session.new_layer")
		var p struct {
			Bounds rexctl.Bounds `json:"bounds"`
		}
		r.Decode(&p)
		if p.Bounds != layer.Board.Bounds() {
			t.Fatalf("new_layer bounds %v, want Board %v", p.Bounds, layer.Board.Bounds())
		}
	})

	t.Run("bare panel startup resolves persisted size", func(t *testing.T) {
		cache := t.TempDir()
		if got := startPanelSize(false, cache); got != layer.Drawer {
			t.Fatalf("missing state: got %v, want Drawer", got)
		}
		if err := layer.SaveSize(cache, layer.Board); err != nil {
			t.Fatal(err)
		}
		if got := startPanelSize(false, cache); got != layer.Board {
			t.Fatalf("persisted board: got %v, want Board", got)
		}
		if err := layer.SaveSize(cache, layer.Drawer); err != nil {
			t.Fatal(err)
		}
		if got := startPanelSize(true, cache); got != layer.Board {
			t.Fatalf("explicit --board: got %v, want Board", got)
		}
	})
}
