package layer

import (
	"context"
	"fmt"
	"slices"

	"github.com/est7/rex-amq-ui/internal/apc"
	"github.com/est7/rex-amq-ui/internal/rexctl"
)

// Launch is what Open opens.
type Launch struct {
	// Session is the Rex session to open the panel in.
	Session string
	// Board opens a new panel at Board size, and sets a restored one to
	// it. Without it a new panel is a Drawer and a restored one keeps the
	// size the human left it at.
	Board bool
	// Tab is the tab to show (an apc.Tabs name), or "" for the panel's
	// default.
	Tab string
	// Command is the panel's argv up to its own flags: the rex-amq-ui
	// binary by absolute path, and --adapter-bin when given. Open appends
	// --session, --focus, --tab, and --board.
	Command []string
}

// Opened is the panel Open opened or restored.
type Opened struct {
	LayerID  string `json:"layer_id"`
	BlockID  string `json:"block_id"`
	Restored bool   `json:"restored"`
}

// Open shows the session's panel and returns it. A panel already open in
// the session (a floating block labelled Label) is restored: made a Board
// when l.Board, set normal and focused, then told to resume and to show
// l.Tab. Otherwise a new floating layer at its size's bounds in the active
// window
// runs l.Command, started at the size the window gives those bounds so its
// first frame is not 80x24, and is disowned so Rex.app sizes it from then
// on.
//
// c must be a fresh connection; Open attaches it to the session.
func Open(ctx context.Context, c *rexctl.Client, l Launch) (Opened, error) {
	if l.Tab != "" && !slices.Contains(apc.Tabs, l.Tab) {
		return Opened{}, fmt.Errorf("layer: no tab %q (want one of %v)", l.Tab, apc.Tabs)
	}
	a, err := c.Attach(ctx, l.Session)
	if err != nil {
		return Opened{}, err
	}
	if block, layer, ok := find(a.View, ""); ok {
		return restore(ctx, c, l, block, layer)
	}
	return create(ctx, c, l, a.View)
}

func restore(ctx context.Context, c *rexctl.Client, l Launch, block string, layer rexctl.Layer) (Opened, error) {
	o := Opened{LayerID: layer.LayerID, BlockID: block, Restored: true}
	if l.Board && SizeOf(layer.Bounds) != Board {
		if _, err := c.ResizeLayer(ctx, l.Session, layer.LayerID, Board.Bounds()); err != nil {
			return o, err
		}
	}
	if _, err := c.SetLayerState(ctx, l.Session, layer.LayerID, rexctl.LayerNormal, true); err != nil {
		return o, err
	}
	// The panel paused itself when it was minimized; nothing else tells it
	// it is showing again when it was restored by hand or by this launcher.
	say := apc.Encode(apc.Command{Verb: apc.Resume})
	if l.Tab != "" {
		say += apc.Encode(apc.Command{Verb: apc.Tab, Tab: l.Tab})
	}
	if _, err := c.Write(ctx, l.Session, block, say); err != nil {
		return o, err
	}
	return o, nil
}

func create(ctx context.Context, c *rexctl.Client, l Launch, v rexctl.View) (Opened, error) {
	bounds := l.size().Bounds()
	opts := rexctl.TerminalOptions{Shell: "none", Command: argv(l, v.FocusedBlock())}
	if size, ok := startSize(ctx, c, l.Session, v, bounds); ok {
		opts.Size = &size
	}
	made, err := c.NewLayer(ctx, rexctl.NewLayer{
		SessionID: l.Session, WindowID: v.ActiveWindowID, Bounds: bounds,
		Layout: rexctl.TerminalBlock(Label, opts), Focus: true,
	})
	if err != nil {
		return Opened{}, err
	}
	if len(made.BlockIDs) != 1 {
		return Opened{LayerID: made.LayerID}, fmt.Errorf("layer: new_layer made %d blocks, want 1", len(made.BlockIDs))
	}
	o := Opened{LayerID: made.LayerID, BlockID: made.BlockIDs[0]}
	if _, err := c.DisownBlock(ctx, l.Session, o.BlockID); err != nil {
		return o, fmt.Errorf("layer: opened %s but could not hand its sizing to Rex.app: %w", o.LayerID, err)
	}
	return o, nil
}

func (l Launch) size() Size {
	if l.Board {
		return Board
	}
	return Drawer
}

// argv is the panel's command line. --focus names the pane the human was
// in, which the panel marks until Rex reports another.
func argv(l Launch, focused string) []string {
	args := append(slices.Clone(l.Command), "--session", l.Session)
	if focused != "" {
		args = append(args, "--focus", focused)
	}
	if l.Tab != "" {
		args = append(args, "--tab", l.Tab)
	}
	if l.Board {
		args = append(args, "--board")
	}
	return args
}

// startSize is the size a terminal at bounds starts with, measured from a
// tiled block of the active window; false when nothing measures it, and
// Rex starts the terminal at 80x24 until Rex.app resizes it.
func startSize(ctx context.Context, c *rexctl.Client, session string, v rexctl.View, bounds rexctl.Bounds) (rexctl.StartSize, bool) {
	block, rect, ok := v.TiledBlock(v.ActiveWindowID)
	if !ok {
		return rexctl.StartSize{}, false
	}
	ts, err := c.TerminalSize(ctx, session, block)
	if err != nil {
		return rexctl.StartSize{}, false
	}
	s, err := rexctl.StartSizeFor(ts, rect, bounds)
	return s, err == nil
}
