// Package layer drives the panel's floating layer in Rex: opening it (or
// restoring the one already open) from a launcher, and, from the panel
// inside it, zooming it, minimizing it, jumping to a member's pane or
// another session, and following what the human focuses.
//
// It owns the facts about the layer: its label, its two sizes, and the
// order of the calls that leave it (minimize before focusing a pane, switch
// sessions before minimizing). It works through internal/rexctl and never
// starts the rex CLI.
package layer

import (
	"errors"

	"github.com/est7/rex-amq-ui/internal/rexctl"
)

// Label is the label of the panel's terminal block. Open finds a panel
// already open in a session by it: one panel per session.
const Label = "rex-amq-ui"

// Size is the panel's size: a drawer on the right, or a board in the
// middle.
type Size int

const (
	Drawer Size = iota
	Board
)

func (s Size) String() string {
	if s == Board {
		return "Board"
	}
	return "Drawer"
}

// Bounds is where a layer of size s sits in its window.
func (s Size) Bounds() rexctl.Bounds {
	if s == Board {
		return rexctl.Bounds{0.06, 0.06, 0.88, 0.88}
	}
	return rexctl.Bounds{0.64, 0.02, 0.35, 0.96}
}

// Other is the size z switches to.
func (s Size) Other() Size {
	if s == Board {
		return Drawer
	}
	return Board
}

// SizeOf is the size whose bounds b is closest to: a layer wider than
// half its window is a board.
func SizeOf(b rexctl.Bounds) Size {
	if b.W() > 0.5 {
		return Board
	}
	return Drawer
}

var (
	// ErrRemoteControlOff is a session switch the Rex app refused: it
	// performs session.select for other clients only with Remote Control
	// on.
	ErrRemoteControlOff = errors.New("enable Rex Settings > Rex Server > Remote Control")
	// ErrNoApp is a session switch with no Rex app connected to perform it.
	ErrNoApp = errors.New("no Rex app is connected to switch sessions")
	// ErrNoPanel is a session view without the panel's block in a floating
	// layer.
	ErrNoPanel = errors.New("layer: the panel's block is not in a floating layer")
	// ErrDisconnected is a call while the control connection is down; the
	// panel reconnects on its own (Watch).
	ErrDisconnected = errors.New("layer: not connected to Rex")
)

// find locates the panel in v: block when given, else the block labelled
// Label, preferring the active window's. It returns the block and its
// floating layer.
func find(v rexctl.View, block string) (string, rexctl.Layer, bool) {
	if block != "" {
		_, l, ok := v.LayerOf(block)
		return block, l, ok && l.Kind == rexctl.LayerFloating
	}
	var found string
	var layer rexctl.Layer
	for _, w := range v.Windows {
		for _, l := range w.Layers {
			if l.Kind != rexctl.LayerFloating {
				continue
			}
			for _, b := range l.Blocks {
				if b.Label != Label {
					continue
				}
				if found == "" || w.WindowID == v.ActiveWindowID {
					found, layer = b.BlockID, l
				}
			}
		}
	}
	return found, layer, found != ""
}
