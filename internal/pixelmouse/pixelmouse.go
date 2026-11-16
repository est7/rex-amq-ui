// Package pixelmouse reads SGR pixel mouse reports (mode 1016) and maps a
// pointer position to what was drawn under it.
//
// Positions are pixels of the terminal block. The Graph image starts
// below the panel's bars, so a hit test subtracts the region's pixel
// origin: the rows above it times the real cell height, which comes from
// Rex's block size, never from CSI 16 t (Rex answers that with a synthetic
// 8x16 before an app owns the block).
package pixelmouse

import (
	"bytes"
	"errors"
	"image"
	"strconv"
)

const (
	// Enable turns on button-event tracking (1002: presses, releases,
	// drags), SGR encoding (1006), and pixel coordinates (1016).
	Enable = "\x1b[?1002h\x1b[?1006h\x1b[?1016h"
	// Disable turns them off again, in reverse.
	Disable = "\x1b[?1016l\x1b[?1006l\x1b[?1002l"
)

var (
	// ErrIncomplete is the start of a report whose rest has not arrived.
	ErrIncomplete = errors.New("pixelmouse: incomplete report")
	// ErrNotMouse is input that is not an SGR mouse report.
	ErrNotMouse = errors.New("pixelmouse: not an SGR mouse report")
)

// coordBase is the coordinate SGR reports for the first pixel. SGR counts
// cells from 1 and terminals keep that for pixels; unconfirmed on Rex,
// where at worst it moves a click by one pixel.
const coordBase = 1

// Mods are the modifier keys held during a mouse event.
type Mods uint8

const (
	Shift Mods = 1 << iota
	Meta
	Ctrl
)

// Event is one mouse report.
type Event struct {
	// Button is 0 (left), 1 (middle), 2 (right), or 3 (none, on motion);
	// for a wheel event, 0 is up and 1 down.
	Button int
	Mods   Mods
	// X and Y are 0-based pixels from the block's top-left corner.
	X, Y    int
	Release bool
	Motion  bool
	Wheel   bool
}

// LeftPress reports whether e is the left button going down.
func (e Event) LeftPress() bool {
	return e.Button == 0 && !e.Release && !e.Motion && !e.Wheel
}

// Parse reads the report CSI < b ; x ; y (M press, m release) at the start
// of b and returns it with the number of bytes it took.
func Parse(b []byte) (Event, int, error) {
	const intro = "\x1b[<"
	if len(b) < len(intro) {
		if bytes.HasPrefix([]byte(intro), b) {
			return Event{}, 0, ErrIncomplete
		}
		return Event{}, 0, ErrNotMouse
	}
	if !bytes.HasPrefix(b, []byte(intro)) {
		return Event{}, 0, ErrNotMouse
	}
	end := bytes.IndexFunc(b[len(intro):], func(r rune) bool { return (r < '0' || r > '9') && r != ';' })
	if end < 0 {
		return Event{}, 0, ErrIncomplete
	}
	end += len(intro)
	if fin := b[end]; fin != 'M' && fin != 'm' {
		return Event{}, 0, ErrNotMouse
	}
	f := bytes.Split(b[len(intro):end], []byte(";"))
	if len(f) != 3 {
		return Event{}, 0, ErrNotMouse
	}
	var v [3]int
	for i, s := range f {
		n, err := strconv.ParseUint(string(s), 10, 31)
		if err != nil {
			return Event{}, 0, ErrNotMouse
		}
		v[i] = int(n)
	}
	if v[1] < coordBase || v[2] < coordBase {
		return Event{}, 0, ErrNotMouse
	}
	code := v[0]
	ev := Event{
		Button:  code & 3,
		Mods:    Mods(code>>2) & (Shift | Meta | Ctrl),
		X:       v[1] - coordBase,
		Y:       v[2] - coordBase,
		Release: b[end] == 'm',
		Motion:  code&32 != 0,
		Wheel:   code&64 != 0,
	}
	return ev, end + 1, nil
}

// Region is where the Graph image sits in the block: its top-left cell
// and the real cell size in pixels.
type Region struct{ Col, Row, CellW, CellH int }

// Target is something clickable, in pixels of the region's own image.
type Target struct {
	ID   string
	Rect image.Rectangle
}

// HitTest is the target under block pixel x,y: the last one listed when
// targets overlap, as it was drawn on top.
func HitTest(x, y int, r Region, targets []Target) (string, bool) {
	p := image.Pt(x-r.Col*r.CellW, y-r.Row*r.CellH)
	for i := len(targets) - 1; i >= 0; i-- {
		if p.In(targets[i].Rect) {
			return targets[i].ID, true
		}
	}
	return "", false
}
