package pixelmouse_test

import (
	"errors"
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

func TestPixelMouseParse(t *testing.T) {
	cases := []struct {
		in   string
		want pixelmouse.Event
		n    int
	}{
		// SGR reports count from 1; Event is 0-based pixels.
		{"\x1b[<0;123;456M", pixelmouse.Event{Button: 0, X: 122, Y: 455}, 13},
		{"\x1b[<0;123;456mrest", pixelmouse.Event{Button: 0, X: 122, Y: 455, Release: true}, 13},
		{"\x1b[<2;1;1M", pixelmouse.Event{Button: 2, X: 0, Y: 0}, 9},
		{"\x1b[<32;10;20M", pixelmouse.Event{Button: 0, X: 9, Y: 19, Motion: true}, 12},
		{"\x1b[<65;10;20M", pixelmouse.Event{Button: 1, X: 9, Y: 19, Wheel: true}, 12},
		{"\x1b[<20;2000;1500M", pixelmouse.Event{Button: 0, X: 1999, Y: 1499, Mods: pixelmouse.Shift | pixelmouse.Ctrl}, 16},
		{"\x1b[<8;5;5M", pixelmouse.Event{Button: 0, X: 4, Y: 4, Mods: pixelmouse.Meta}, 9},
	}
	for _, c := range cases {
		ev, n, err := pixelmouse.Parse([]byte(c.in))
		if err != nil || ev != c.want || n != c.n {
			t.Errorf("Parse(%q) = %+v, %d, %v; want %+v, %d", c.in, ev, n, err, c.want, c.n)
		}
	}
	for _, in := range []string{"\x1b[<0;12", "\x1b[<", "\x1b["} {
		if _, _, err := pixelmouse.Parse([]byte(in)); !errors.Is(err, pixelmouse.ErrIncomplete) {
			t.Errorf("Parse(%q) err %v, want ErrIncomplete", in, err)
		}
	}
	for _, in := range []string{"\x1b[A", "x", "\x1b[<0;a;1M", "\x1b[<0;1M", "\x1b[<0;1;2;3M", "\x1b[<0;0;5M", "\x1b[<0;99999999999;5M"} {
		if _, _, err := pixelmouse.Parse([]byte(in)); !errors.Is(err, pixelmouse.ErrNotMouse) {
			t.Errorf("Parse(%q) err %v, want ErrNotMouse", in, err)
		}
	}
	ev, _, _ := pixelmouse.Parse([]byte("\x1b[<0;5;5M"))
	if !ev.LeftPress() {
		t.Errorf("left press not reported as one")
	}
	for _, in := range []string{"\x1b[<0;5;5m", "\x1b[<32;5;5M", "\x1b[<64;5;5M", "\x1b[<2;5;5M"} {
		if ev, _, _ := pixelmouse.Parse([]byte(in)); ev.LeftPress() {
			t.Errorf("%q counted as a left press", in)
		}
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

func TestPixelMouseEventAt(t *testing.T) {
	ev, _, err := pixelmouse.Parse([]byte(fmt.Sprintf("\x1b[<0;%d;%dM", 2*23+151, 3*49+61)))
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := pixelmouse.HitTest(ev.X, ev.Y, pixelmouse.Region{Col: 2, Row: 3, CellW: 23, CellH: 49}, badge); !ok || id != "claude-98" {
		t.Errorf("parsed click at %d,%d missed the badge", ev.X, ev.Y)
	}
}
