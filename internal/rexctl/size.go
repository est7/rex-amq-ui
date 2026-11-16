package rexctl

import (
	"errors"
	"fmt"
	"math"
)

// ErrCannotMeasure is a tiled block whose size or rect cannot measure its
// window. Leave the layer's size out then: Rex starts it at 80x24.
var ErrCannotMeasure = errors.New("rexctl: cannot measure the window")

// StartSizeFor is the starting size of a terminal in a floating layer at
// bounds, measured from a tiled block of the same window: tile is that
// block's size (TerminalSize) and rect its place in the window's layout
// (View.TiledBlock). Without it a new layer's first frame renders at 80x24
// until the app resizes it.
//
// The window is taken to be tile's cells scaled by rect, so pane borders
// and padding make the result at most a cell or so off; the app sets the
// exact size once it owns the block (DisownBlock).
func StartSizeFor(tile TerminalSize, rect, bounds Bounds) (StartSize, error) {
	if tile.Columns <= 0 || tile.Rows <= 0 || rect.W() <= 0 || rect.H() <= 0 {
		return StartSize{}, fmt.Errorf("%w: a %dx%d block at %v", ErrCannotMeasure, tile.Columns, tile.Rows, rect)
	}
	if bounds.W() <= 0 || bounds.H() <= 0 || bounds.W() > 1 || bounds.H() > 1 {
		return StartSize{}, fmt.Errorf("rexctl: layer bounds %v do not fit a window", bounds)
	}
	s := StartSize{
		Columns: cells(float64(tile.Columns) / rect.W() * bounds.W()),
		Rows:    cells(float64(tile.Rows) / rect.H() * bounds.H()),
	}
	if tile.CellWidthPx > 0 && tile.CellHeightPx > 0 {
		s.CellWidthPx, s.CellHeightPx = tile.CellWidthPx, tile.CellHeightPx
	}
	return s, nil
}

// cells is the whole cells in n, within Rex's 1..65535. The epsilon keeps
// fractions like 0.35/0.6 from rounding a whole number down.
func cells(n float64) int {
	return min(max(int(math.Floor(n+1e-9)), 1), 65535)
}
