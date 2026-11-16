package rexctl

import "encoding/json"

// Bounds is a rectangle as [x, y, w, h] fractions of a window's width and
// height, measured from its top-left corner.
type Bounds [4]float64

func (b Bounds) X() float64 { return b[0] }
func (b Bounds) Y() float64 { return b[1] }
func (b Bounds) W() float64 { return b[2] }
func (b Bounds) H() float64 { return b[3] }

// View is a session's windows, layers, and blocks at one revision
// (session.view, and the view session.attach returns).
type View struct {
	Revision       int64    `json:"revision"`
	SessionID      string   `json:"session_id"`
	Label          string   `json:"label"`
	ActiveWindowID string   `json:"active_window_id,omitempty"`
	Windows        []Window `json:"windows"`
}

// Window is a tab of the session.
type Window struct {
	WindowID       string  `json:"window_id"`
	Label          string  `json:"label"`
	FocusedBlockID string  `json:"focused_block_id,omitempty"`
	ZoomedBlockID  string  `json:"zoomed_block_id,omitempty"`
	Layers         []Layer `json:"layers"`
}

// Layer kinds.
const (
	LayerTiled    = "tiled"
	LayerFloating = "floating"
)

// Layer is a window's tiled layout (the first layer) or a floating layer.
type Layer struct {
	LayerID string      `json:"layer_id"`
	Kind    string      `json:"kind"`
	Bounds  Bounds      `json:"bounds"`
	State   LayerState  `json:"state,omitempty"`
	Layout  *LayoutNode `json:"layout"`
	Blocks  []Block     `json:"blocks"`
}

// Block is a block as a view lists it.
type Block struct {
	BlockID     string `json:"block_id"`
	Label       string `json:"label,omitempty"`
	CreatorName string `json:"creator_name,omitempty"`
	Flavor      string `json:"flavor,omitempty"`
	HostID      string `json:"host_id,omitempty"`
	// Owner is the client that owns the block; "" when none does.
	Owner string `json:"owner,omitempty"`
}

// LayoutNode is a node of a layer's layout: a block, or a split in two.
type LayoutNode struct {
	BlockID string `json:"block_id,omitempty"`
	Rect    Bounds `json:"rect"`
	Split   *Split `json:"split,omitempty"`
}

type Split struct {
	SplitID   string      `json:"split_id"`
	Direction string      `json:"direction"`
	Ratio     float64     `json:"ratio"`
	Before    *LayoutNode `json:"before"`
	After     *LayoutNode `json:"after"`
}

// FocusedBlock is the active window's focused block: the pane the human is
// typing in, or "".
func (v View) FocusedBlock() string {
	for _, w := range v.Windows {
		if w.WindowID == v.ActiveWindowID {
			return w.FocusedBlockID
		}
	}
	return ""
}

// LayerOf finds the window and layer holding block.
func (v View) LayerOf(block string) (Window, Layer, bool) {
	for _, w := range v.Windows {
		for _, l := range w.Layers {
			for _, b := range l.Blocks {
				if b.BlockID == block {
					return w, l, true
				}
			}
		}
	}
	return Window{}, Layer{}, false
}

// TiledBlock is a block of window's tiled layout to measure the window by,
// with its rect: the window's focused block when it is tiled, else the
// layout's first block.
func (v View) TiledBlock(window string) (string, Bounds, bool) {
	for _, w := range v.Windows {
		if w.WindowID != window || len(w.Layers) == 0 || w.Layers[0].Layout == nil {
			continue
		}
		tiled := w.Layers[0].Layout
		if r, ok := tiled.rectOf(w.FocusedBlockID); ok && w.FocusedBlockID != "" {
			return w.FocusedBlockID, r, true
		}
		if id, r, ok := tiled.first(); ok {
			return id, r, true
		}
	}
	return "", Bounds{}, false
}

func (n *LayoutNode) rectOf(block string) (Bounds, bool) {
	if n == nil {
		return Bounds{}, false
	}
	if n.Split == nil {
		return n.Rect, n.BlockID == block
	}
	if r, ok := n.Split.Before.rectOf(block); ok {
		return r, true
	}
	return n.Split.After.rectOf(block)
}

func (n *LayoutNode) first() (string, Bounds, bool) {
	switch {
	case n == nil:
		return "", Bounds{}, false
	case n.Split == nil:
		return n.BlockID, n.Rect, n.BlockID != ""
	}
	if id, r, ok := n.Split.Before.first(); ok {
		return id, r, true
	}
	return n.Split.After.first()
}

// Event is a server event, or a session event with its session.
type Event struct {
	// Seq orders events and responses on one connection: apply an attach's
	// view, then the events whose Seq is higher than its response's.
	Seq int64
	// SessionID is set for a session event.
	SessionID string
	Name      string
	Payload   json.RawMessage
}

// Event names this package reads.
const (
	EventSessionViewChanged = "session_view_changed"
	EventBlockOwnerChanged  = "block_owner_changed"
)

// ViewChanged is session_view_changed: the session's view changed, and is
// now at Revision. It carries no view; fetch one (View) when Revision is
// higher than the view held. Such events can arrive out of order.
type ViewChanged struct {
	Revision int64 `json:"revision"`
}

// OwnerChanged is block_owner_changed. Reason is create, take, request,
// disown, lost, disconnect, or restore; ignore one you do not know.
type OwnerChanged struct {
	BlockID string `json:"block_id"`
	// Owner and Previous are "" when there is no such client.
	Owner    string `json:"owner,omitempty"`
	Previous string `json:"previous,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ViewChanged reads e as session_view_changed.
func (e Event) ViewChanged() (ViewChanged, bool) {
	var v ViewChanged
	return v, e.Name == EventSessionViewChanged && json.Unmarshal(e.Payload, &v) == nil
}

// OwnerChanged reads e as block_owner_changed.
func (e Event) OwnerChanged() (OwnerChanged, bool) {
	var v OwnerChanged
	return v, e.Name == EventBlockOwnerChanged && json.Unmarshal(e.Payload, &v) == nil && v.BlockID != ""
}
