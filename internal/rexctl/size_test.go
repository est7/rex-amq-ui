package rexctl_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/est7/rex-amq-ui/internal/rexctl"
	"github.com/est7/rex-amq-ui/internal/rexctl/rexctltest"
)

// A 120x40 pane filling 60% of the window's width is a 200x40 window: the
// drawer (35% x 96%) is 70x38 and the board (88% x 88%) 176x35.
func TestRexctlStartSizeFor(t *testing.T) {
	tile := rexctl.TerminalSize{Columns: 120, Rows: 40, CellWidthPx: 9, CellHeightPx: 22}
	half := rexctl.Bounds{0, 0, 0.6, 1}
	for _, c := range []struct {
		name   string
		tile   rexctl.TerminalSize
		rect   rexctl.Bounds
		bounds rexctl.Bounds
		want   rexctl.StartSize
	}{
		{"drawer", tile, half, rexctl.Bounds{0.64, 0.02, 0.35, 0.96}, rexctl.StartSize{Columns: 70, Rows: 38, CellWidthPx: 9, CellHeightPx: 22}},
		{"board", tile, half, rexctl.Bounds{0.06, 0.06, 0.88, 0.88}, rexctl.StartSize{Columns: 176, Rows: 35, CellWidthPx: 9, CellHeightPx: 22}},
		{"a whole-window pane", rexctl.TerminalSize{Columns: 100, Rows: 30}, rexctl.Bounds{0, 0, 1, 1}, rexctl.Bounds{0, 0, 0.5, 0.5}, rexctl.StartSize{Columns: 50, Rows: 15}},
		{"a cell size with one side unknown is left out", rexctl.TerminalSize{Columns: 100, Rows: 30, CellWidthPx: 9}, rexctl.Bounds{0, 0, 1, 1}, rexctl.Bounds{0, 0, 1, 1}, rexctl.StartSize{Columns: 100, Rows: 30}},
		{"a whole number in floating point stays whole (12/0.7*0.35 is 5.999...)", rexctl.TerminalSize{Columns: 12, Rows: 12}, rexctl.Bounds{0, 0, 0.7, 0.7}, rexctl.Bounds{0, 0, 0.35, 0.35}, rexctl.StartSize{Columns: 6, Rows: 6}},
		{"never under one cell", rexctl.TerminalSize{Columns: 10, Rows: 3}, rexctl.Bounds{0, 0, 1, 1}, rexctl.Bounds{0, 0, 0.01, 0.01}, rexctl.StartSize{Columns: 1, Rows: 1}},
	} {
		got, err := rexctl.StartSizeFor(c.tile, c.rect, c.bounds)
		if err != nil || got != c.want {
			t.Errorf("%s: %+v %v, want %+v", c.name, got, err, c.want)
		}
	}
	for _, c := range []struct {
		name         string
		tile         rexctl.TerminalSize
		rect, bounds rexctl.Bounds
	}{
		{"an unsized block", rexctl.TerminalSize{}, half, rexctl.Bounds{0, 0, 1, 1}},
		{"an empty rect", tile, rexctl.Bounds{}, rexctl.Bounds{0, 0, 1, 1}},
		{"bounds wider than the window", tile, half, rexctl.Bounds{0, 0, 1.5, 1}},
	} {
		if got, err := rexctl.StartSizeFor(c.tile, c.rect, c.bounds); err == nil {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if _, err := rexctl.StartSizeFor(rexctl.TerminalSize{}, half, rexctl.Bounds{0, 0, 1, 1}); !errors.Is(err, rexctl.ErrCannotMeasure) {
		t.Errorf("unsized block: %v", err)
	}
}

// The way the panel opens its layer: measure the active window through the
// pane it has focused, then ask for the layer at that size.
func TestRexctlOpensALayerAtTheMeasuredSize(t *testing.T) {
	s := rexctltest.NewServer(t)
	s.SetView(json.RawMessage(sampleView))
	c := dial(t, s)
	cx := ctx(t)
	a, err := c.Attach(cx, session)
	if err != nil {
		t.Fatal(err)
	}
	block, rect, ok := a.View.TiledBlock(a.View.ActiveWindowID)
	if !ok || block != right || rect != (rexctl.Bounds{0.6, 0, 0.4, 1}) {
		t.Fatalf("tiled block %s at %v (%v)", block, rect, ok)
	}
	if b, r, ok := a.View.TiledBlock("window:2"); ok {
		t.Fatalf("an empty layout measured as %s at %v", b, r)
	}
	tile, err := c.TerminalSize(cx, session, block)
	if err != nil {
		t.Fatal(err)
	}
	drawer := rexctl.Bounds{0.64, 0.02, 0.35, 0.96}
	size, err := rexctl.StartSizeFor(tile, rect, drawer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.NewLayer(cx, rexctl.NewLayer{SessionID: session, Bounds: drawer, Layout: rexctl.TerminalBlock("rex-amq-ui", rexctl.TerminalOptions{Command: []string{"rex-amq-ui"}, Size: &size})}); err != nil {
		t.Fatal(err)
	}
	// The fake's pane is 120x40 at 40% of the width: a 300x40 window.
	samePayload(t, s, "session.new_layer", `{"session_id":"`+session+`","bounds":[0.64,0.02,0.35,0.96],
	 "layout":{"block":{"flavor":"com.superlogical.terminal.shell","label":"rex-amq-ui","options":{"command":["rex-amq-ui"],
	 "size":{"columns":105,"rows":38,"cell_width_px":9,"cell_height_px":22}}}}}`)
}
