// Package pixelmouse turns SGR pixel mouse reports (mode 1016) on and off
// and maps a pointer position to what was drawn under it. Bubble Tea's
// input decoder parses the reports themselves into tea.MouseClickMsg, as
// 0-based pixels (it takes SGR's first coordinate as 1).
//
// Positions are pixels of the terminal block. The Graph image starts
// below the panel's bars, so a hit test subtracts the region's pixel
// origin: the rows above it times the real cell height, which comes from
// Rex's block size, never from CSI 16 t (Rex answers that with a synthetic
// 8x16 before an app owns the block).
package pixelmouse

import "image"

const (
	// Enable turns on button-event tracking (1002: presses, releases,
	// drags), SGR encoding (1006), and pixel coordinates (1016).
	Enable = "\x1b[?1002h\x1b[?1006h\x1b[?1016h"
	// Disable turns them off again, in reverse.
	Disable = "\x1b[?1016l\x1b[?1006l\x1b[?1002l"
)

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
