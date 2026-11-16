package ui

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/est7/rex-amq-ui/internal/layer"
	"github.com/est7/rex-amq-ui/internal/rexctl"
	"github.com/est7/rex-amq-ui/internal/rexctl/rexctltest"
)

const (
	surfSession = "session:s"
	surfWindow  = "window:1"
	surfPanel   = "block:panel"
	surfLayer   = "layer:panel"
)

// surfView is session:s with the fixture's panes tiled in window:1 and,
// when panelAt is set, the panel's layer at those bounds.
func surfView(rev int64, panelAt *rexctl.Bounds) rexctl.View {
	tiled := rexctl.Layer{LayerID: "layer:tiled", Kind: rexctl.LayerTiled, Bounds: rexctl.Bounds{0, 0, 1, 1},
		Layout: &rexctl.LayoutNode{BlockID: "block:claude-01", Rect: rexctl.Bounds{0, 0, 1, 1}},
		Blocks: []rexctl.Block{{BlockID: "block:claude-01"}, {BlockID: "block:claude-02"}}}
	w := rexctl.Window{WindowID: surfWindow, FocusedBlockID: "block:claude-01", Layers: []rexctl.Layer{tiled}}
	if panelAt != nil {
		w.FocusedBlockID = surfPanel
		w.Layers = append(w.Layers, rexctl.Layer{LayerID: surfLayer, Kind: rexctl.LayerFloating, Bounds: *panelAt,
			Layout: &rexctl.LayoutNode{BlockID: surfPanel, Rect: rexctl.Bounds{0, 0, 1, 1}},
			Blocks: []rexctl.Block{{BlockID: surfPanel, Label: layer.Label, Owner: "client:app"}}})
	}
	return rexctl.View{Revision: rev, SessionID: surfSession, ActiveWindowID: surfWindow, Windows: []rexctl.Window{w}}
}

// fullWindow answers size as a 200x50 window filled by one pane.
func fullWindow(s *rexctltest.Server) {
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

func calls(s *rexctltest.Server, from int) []string {
	var out []string
	for _, r := range s.Requests()[from:] {
		out = append(out, r.Method)
	}
	return out
}

// TestSurface drives the panel's surface end to end against the fake
// Rex: the launcher's bounds and start sizes, zoom, leaving for a pane,
// the refused session switch, and opening twice.
func TestSurface(t *testing.T) {
	ctx := context.Background()
	t.Run("bounds: drawer 35% and board 88% of a 200x50 window", func(t *testing.T) {
		for _, c := range []struct {
			size       layer.Size
			cols, rows int
			bounds     rexctl.Bounds
		}{
			{layer.Drawer, 70, 48, rexctl.Bounds{0.64, 0.02, 0.35, 0.96}},
			{layer.Board, 176, 44, rexctl.Bounds{0.06, 0.06, 0.88, 0.88}},
		} {
			s := rexctltest.NewServer(t)
			fullWindow(s)
			s.SetView(surfView(1, nil))
			cl, err := rexctl.Dial(ctx, s.URL)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := layer.Open(ctx, cl, layer.Launch{Session: surfSession, Board: c.size == layer.Board, Command: []string{"/bin/rex-amq-ui"}}); err != nil {
				t.Fatal(err)
			}
			cl.Close()
			r, _ := s.Last("session.new_layer")
			var p struct {
				Bounds rexctl.Bounds `json:"bounds"`
				Layout struct {
					Block struct {
						Options struct {
							Size struct {
								Columns int `json:"columns"`
								Rows    int `json:"rows"`
							} `json:"size"`
						} `json:"options"`
					} `json:"block"`
				} `json:"layout"`
			}
			r.Decode(&p)
			sz := p.Layout.Block.Options.Size
			if p.Bounds != c.bounds || sz.Columns != c.cols || sz.Rows != c.rows {
				t.Errorf("%v: bounds %v size %dx%d, want %v %dx%d", c.size, p.Bounds, sz.Columns, sz.Rows, c.bounds, c.cols, c.rows)
			}
			if w, h := refSize(t, c.size); w != c.cols || h != c.rows {
				t.Errorf("%v: the goldens' reference size %dx%d is not the launcher's", c.size, w, h)
			}
		}
	})

	connect := func(t *testing.T) (*rexctltest.Server, *layer.Panel, Model) {
		t.Helper()
		s := rexctltest.NewServer(t)
		fullWindow(s)
		drawer := layer.Drawer.Bounds()
		s.SetView(surfView(2, &drawer))
		p, err := layer.Connect(ctx, s.URL, surfSession, surfPanel, "block:claude-01")
		if err != nil {
			t.Fatal(err)
		}
		w, h := refSize(t, layer.Drawer)
		m := build(t, "edited-crew-attention", w, h, Deps{Layer: p}, Options{})
		m = step(t, m, p.State())
		return s, p, m
	}

	t.Run("zoom toggles the layer between drawer and board", func(t *testing.T) {
		s, _, m := connect(t)
		from := len(s.Requests())
		m = press(t, m, "z")
		m = press(t, m, "z")
		var bounds []rexctl.Bounds
		for _, r := range s.Requests()[from:] {
			var p struct {
				LayerID string        `json:"layer_id"`
				Bounds  rexctl.Bounds `json:"bounds"`
			}
			r.Decode(&p)
			if r.Method != "session.resize_layer" || p.LayerID != surfLayer {
				t.Fatalf("call %s %s", r.Method, r.Payload)
			}
			bounds = append(bounds, p.Bounds)
		}
		if want := []rexctl.Bounds{layer.Board.Bounds(), layer.Drawer.Bounds()}; !reflect.DeepEqual(bounds, want) {
			t.Fatalf("bounds %v", bounds)
		}
		if m.size != layer.Drawer {
			t.Fatalf("size %v", m.size)
		}
	})

	t.Run("enter on a member minimizes, then focuses its pane", func(t *testing.T) {
		s, _, m := connect(t)
		m = step(t, m, mustTab("members"))
		from := len(s.Requests())
		m = press(t, m, "2", "enter")
		if got := calls(s, from); !reflect.DeepEqual(got, []string{"session.set_layer_state", "session.focus_block"}) {
			t.Fatalf("calls %v", got)
		}
		var st struct {
			State string `json:"state"`
		}
		var fb struct {
			BlockID string `json:"block_id"`
		}
		r0 := s.Requests()[from]
		r1 := s.Requests()[from+1]
		json.Unmarshal(r0.Payload, &st)
		json.Unmarshal(r1.Payload, &fb)
		if st.State != "minimized" || fb.BlockID != "block:claude-02" || !m.Paused() {
			t.Fatalf("state %q block %q paused %v", st.State, fb.BlockID, m.Paused())
		}
	})

	t.Run("a refused session switch leaves the panel up with the hint", func(t *testing.T) {
		s := rexctltest.NewServer(t)
		drawer := layer.Drawer.Bounds()
		s.SetView(surfView(2, &drawer))
		s.Handle("client.perform", func(rexctltest.Request) (any, *rexctltest.Error) {
			return nil, &rexctltest.Error{Code: "refused", Message: "the app does not allow remote control", Status: 403}
		})
		p, err := layer.Connect(ctx, s.URL, surfSession, surfPanel, "")
		if err != nil {
			t.Fatal(err)
		}
		w, h := refSize(t, layer.Drawer)
		m := build(t, "edited-personal-cockpit", w, h, Deps{Layer: p}, Options{})
		from := len(s.Requests())
		m = press(t, m, "enter")
		if got := calls(s, from); !reflect.DeepEqual(got, []string{"client.list", "client.perform"}) {
			t.Fatalf("calls %v", got)
		}
		r, _ := s.Last("client.perform")
		var perf struct {
			Action string `json:"action"`
			Args   struct {
				SessionID string `json:"session_id"`
			} `json:"args"`
		}
		r.Decode(&perf)
		foot := lines(m.Frame())[h-1]
		if perf.Action != "session.select" || perf.Args.SessionID != "session:s" || m.Paused() ||
			!strings.Contains(foot, "enable Rex Settings > Rex Server > Remote Control") {
			t.Fatalf("perform %+v paused %v footer %q", perf, m.Paused(), foot)
		}
	})

	t.Run("open twice: one layer, the second call restores it", func(t *testing.T) {
		s := rexctltest.NewServer(t)
		fullWindow(s)
		s.SetView(surfView(1, nil))
		s.Handle("session.new_layer", func(r rexctltest.Request) (any, *rexctltest.Error) {
			var p struct {
				Bounds rexctl.Bounds `json:"bounds"`
			}
			r.Decode(&p)
			s.SetView(surfView(2, &p.Bounds))
			return map[string]any{"layer_id": surfLayer, "block_ids": []string{surfPanel}, "revision": 2}, nil
		})
		var got []layer.Opened
		for range 2 {
			cl, err := rexctl.Dial(ctx, s.URL)
			if err != nil {
				t.Fatal(err)
			}
			o, err := layer.Open(ctx, cl, layer.Launch{Session: surfSession, Command: []string{"/bin/rex-amq-ui"}})
			cl.Close()
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, o)
		}
		news := 0
		for _, r := range s.Requests() {
			if r.Method == "session.new_layer" {
				news++
			}
		}
		if news != 1 || got[0].Restored || !got[1].Restored || got[0].BlockID != got[1].BlockID {
			t.Fatalf("new_layer %d times, opened %+v", news, got)
		}
	})
}
