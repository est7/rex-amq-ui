package pixelmouse_test

import (
	"fmt"
	"image"
	"testing"

	"github.com/est7/rex-amq-ui/internal/pixelmouse"
)

func TestPixelMouseModes(t *testing.T) {
	if pixelmouse.Enable != "\x1b[?1002h\x1b[?1006h\x1b[?1016h" {
		t.Errorf("Enable %q: want button-event tracking, SGR encoding, then pixel coordinates", pixelmouse.Enable)
	}
	if pixelmouse.Disable != "\x1b[?1016l\x1b[?1006l\x1b[?1002l" {
		t.Errorf("Disable %q: want the reverse", pixelmouse.Disable)
	}
}

// A node badge drawn at (100,50)-(200,105) in the Graph region's own
// pixels, with three rows of tab bar above the region.
var badge = []pixelmouse.Target{{ID: "claude-98", Rect: image.Rect(100, 50, 200, 105)}}

func TestPixelMouseHitTest(t *testing.T) {
	for _, cell := range []struct{ w, h int }{{9, 22}, {23, 49}} {
		t.Run(fmt.Sprintf("%dx%d", cell.w, cell.h), func(t *testing.T) {
			r := pixelmouse.Region{Col: 2, Row: 3, CellW: cell.w, CellH: cell.h}
			ox, oy := 2*cell.w, 3*cell.h
			cases := []struct {
				x, y int
				hit  bool
			}{
				{ox + 100, oy + 50, true},
				{ox + 199, oy + 104, true},
				{ox + 150, oy + 49, false},
				{ox + 200, oy + 80, false},
				// Without subtracting the rows above, this click on the
				// badge's top edge in screen pixels would miss.
				{ox + 150, oy + 50, true},
				{150, 50, false},
			}
			for _, c := range cases {
				id, ok := pixelmouse.HitTest(c.x, c.y, r, badge)
				if ok != c.hit || (ok && id != "claude-98") {
					t.Errorf("HitTest(%d,%d) = %q %v, want hit %v", c.x, c.y, id, ok, c.hit)
				}
			}
		})
	}
}

func TestPixelMouseHitTestWrongCellSizeMisses(t *testing.T) {
	// Rex answers CSI 16 t with a synthetic 8x16 before an app owns the
	// block. A click near the badge's bottom edge at the real 9x22 lands below it when
	// the region origin is computed from 8x16.
	x, y := 2*9+150, 3*22+100
	if _, ok := pixelmouse.HitTest(x, y, pixelmouse.Region{Col: 2, Row: 3, CellW: 9, CellH: 22}, badge); !ok {
		t.Fatal("real cell size misses the badge")
	}
	if _, ok := pixelmouse.HitTest(x, y, pixelmouse.Region{Col: 2, Row: 3, CellW: 8, CellH: 16}, badge); ok {
		t.Errorf("8x16 still hits: the test does not tell the sizes apart")
	}
}

func TestPixelMouseHitTestTopmostWins(t *testing.T) {
	ts := []pixelmouse.Target{{ID: "under", Rect: image.Rect(0, 0, 50, 50)}, {ID: "over", Rect: image.Rect(25, 25, 75, 75)}}
	if id, _ := pixelmouse.HitTest(30, 30, pixelmouse.Region{CellW: 9, CellH: 22}, ts); id != "over" {
		t.Errorf("overlap hit %q, want the target drawn last", id)
	}
}
