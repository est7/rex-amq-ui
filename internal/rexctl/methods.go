package rexctl

import (
	"context"
	"encoding/json"
)

// Attached is session.attach's answer: this connection's client id and the
// session's view, read after the connection started receiving its events.
type Attached struct {
	ClientID string `json:"client_id"`
	View     View   `json:"view"`
	// Seq is the response's place among this connection's events: apply
	// View, then the events whose Seq is higher.
	Seq int64 `json:"-"`
}

// Attach attaches the connection to session: it receives the session's
// events from now on, and may call the session's methods.
func (c *Client) Attach(ctx context.Context, session string) (Attached, error) {
	var a Attached
	seq, err := c.call(ctx, "session.attach", map[string]any{"session_id": session}, &a)
	a.Seq = seq
	return a, err
}

// View reads the session's current view.
func (c *Client) View(ctx context.Context, session string) (View, error) {
	var v View
	_, err := c.call(ctx, "session.view", map[string]any{"session_id": session}, &v)
	return v, err
}

// TerminalBlock is a layout of one terminal block (flavor
// com.superlogical.terminal.shell).
func TerminalBlock(label string, opts TerminalOptions) map[string]any {
	return map[string]any{"block": map[string]any{"flavor": TerminalFlavor, "label": label, "options": opts}}
}

// TerminalFlavor is the flavor of a terminal running a shell or a command.
const TerminalFlavor = "com.superlogical.terminal.shell"

// TerminalOptions are a terminal block's options (its flavor's
// options_schema); zero fields are left out.
type TerminalOptions struct {
	// Shell runs Command, or "none" to run Command directly.
	Shell       string   `json:"shell,omitempty"`
	Command     []string `json:"command,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	PathPrepend []string `json:"path_prepend,omitempty"`
	// Size is the terminal's starting size; nil starts at 80x24.
	Size *StartSize `json:"size,omitempty"`
}

// StartSize is a terminal's starting size. The cell size is sent only when
// both of its sides are known, as the schema requires.
type StartSize struct {
	Columns, Rows             int
	CellWidthPx, CellHeightPx int
}

func (s StartSize) MarshalJSON() ([]byte, error) {
	m := map[string]int{"columns": s.Columns, "rows": s.Rows}
	if s.CellWidthPx > 0 && s.CellHeightPx > 0 {
		m["cell_width_px"], m["cell_height_px"] = s.CellWidthPx, s.CellHeightPx
	}
	return json.Marshal(m)
}

// NewLayer is session.new_layer's request.
type NewLayer struct {
	SessionID string
	// WindowID is the window to add the layer to; "" is the active one.
	WindowID string
	Bounds   Bounds
	// Layout is the blocks to create (TerminalBlock).
	Layout any
	// Focus makes the window active and focuses the first new block.
	Focus bool
}

// LayerMade is session.new_layer's answer.
type LayerMade struct {
	LayerID  string   `json:"layer_id"`
	BlockIDs []string `json:"block_ids"`
	Revision int64    `json:"revision"`
}

// NewLayer creates blocks in a new floating layer at the front of a window.
// The caller owns the new blocks, and sizes them, until it disowns them
// (DisownBlock).
func (c *Client) NewLayer(ctx context.Context, r NewLayer) (LayerMade, error) {
	p := map[string]any{"session_id": r.SessionID, "bounds": r.Bounds, "layout": r.Layout}
	if r.WindowID != "" {
		p["window_id"] = r.WindowID
	}
	if r.Focus {
		p["focus"] = true
	}
	var out LayerMade
	_, err := c.call(ctx, "session.new_layer", p, &out)
	return out, err
}

// BlockOwner is a block and the client that owns it ("" for none).
type BlockOwner struct {
	BlockID string `json:"block_id"`
	Owner   string `json:"owner,omitempty"`
}

// DisownBlock gives up this client's claim on blocks, so the app sizes them.
func (c *Client) DisownBlock(ctx context.Context, session string, blocks ...string) ([]BlockOwner, error) {
	var out struct {
		Blocks []BlockOwner `json:"blocks"`
	}
	_, err := c.call(ctx, "session.disown_block", map[string]any{"session_id": session, "block_ids": blocks}, &out)
	return out.Blocks, err
}

// LayerState is how a floating layer is shown.
type LayerState string

const (
	LayerNormal    LayerState = "normal"
	LayerMinimized LayerState = "minimized"
)

type revision struct {
	Revision int64 `json:"revision"`
}

// SetLayerState minimizes a floating layer or returns it to normal; focus,
// only with normal, also raises it and focuses its last focused block. It
// returns the session's revision after the call.
func (c *Client) SetLayerState(ctx context.Context, session, layer string, state LayerState, focus bool) (int64, error) {
	p := map[string]any{"session_id": session, "layer_id": layer, "state": state}
	if focus {
		p["focus"] = true
	}
	var out revision
	_, err := c.call(ctx, "session.set_layer_state", p, &out)
	return out.Revision, err
}

// ResizeLayer moves or resizes a floating layer.
func (c *Client) ResizeLayer(ctx context.Context, session, layer string, b Bounds) (int64, error) {
	var out revision
	_, err := c.call(ctx, "session.resize_layer", map[string]any{"session_id": session, "layer_id": layer, "bounds": b}, &out)
	return out.Revision, err
}

// FocusBlock focuses a block and makes its window active.
func (c *Client) FocusBlock(ctx context.Context, session, block string) (int64, error) {
	var out revision
	_, err := c.call(ctx, "session.focus_block", map[string]any{"session_id": session, "block_id": block}, &out)
	return out.Revision, err
}

// MoveBlock is session.move_block's request: place Block in a new split
// beside Anchor. Empty Direction and Side, and a zero Ratio, take Rex's
// defaults (horizontal, after, 0.5).
type MoveBlock struct {
	SessionID, BlockID, AnchorBlockID string
	Direction                         string // horizontal | vertical
	Side                              string // before | after
	Ratio                             float64
}

// BlockMoved is session.move_block's answer.
type BlockMoved struct {
	CreatedSplitID string `json:"created_split_id"`
	RemovedSplitID string `json:"removed_split_id,omitempty"`
	Revision       int64  `json:"revision"`
}

// MoveBlock moves a block into a new split beside another, in any window.
func (c *Client) MoveBlock(ctx context.Context, r MoveBlock) (BlockMoved, error) {
	p := map[string]any{"session_id": r.SessionID, "block_id": r.BlockID, "anchor_block_id": r.AnchorBlockID}
	if r.Direction != "" {
		p["direction"] = r.Direction
	}
	if r.Side != "" {
		p["side"] = r.Side
	}
	if r.Ratio != 0 {
		p["ratio"] = r.Ratio
	}
	var out BlockMoved
	_, err := c.call(ctx, "session.move_block", p, &out)
	return out, err
}

// LayerFloated is session.float_block's answer.
type LayerFloated struct {
	LayerID  string `json:"layer_id"`
	Revision int64  `json:"revision"`
}

// FloatBlock moves a block out of its window's layout into a new floating
// layer at b.
func (c *Client) FloatBlock(ctx context.Context, session, block string, b Bounds) (LayerFloated, error) {
	var out LayerFloated
	_, err := c.call(ctx, "session.float_block", map[string]any{"session_id": session, "block_id": block, "bounds": b}, &out)
	return out, err
}

// BlockMethod calls a method a block provides by its short name (size,
// write), decoding its result into result when not nil.
func (c *Client) BlockMethod(ctx context.Context, session, block, method string, args, result any) error {
	p := map[string]any{"session_id": session, "block_id": block, "method": method}
	if args != nil {
		p["args"] = args
	}
	_, err := c.call(ctx, "block.method", p, result)
	return err
}

// TerminalSize is a terminal block's size (its size method).
type TerminalSize struct {
	Columns      int `json:"columns"`
	Rows         int `json:"rows"`
	CellWidthPx  int `json:"cell_width_px"`
	CellHeightPx int `json:"cell_height_px"`
	// Owner is the client whose size the terminal uses; "" when no client
	// owns the block.
	Owner string `json:"owner"`
}

// TerminalSize reads a terminal block's size and owner.
func (c *Client) TerminalSize(ctx context.Context, session, block string) (TerminalSize, error) {
	var out struct {
		TerminalSize
		Owner *string `json:"owner"`
	}
	err := c.BlockMethod(ctx, session, block, "size", map[string]any{}, &out)
	if out.Owner != nil {
		out.TerminalSize.Owner = *out.Owner
	}
	return out.TerminalSize, err
}

// Write types data into a terminal block as if typed, returning the bytes
// queued.
func (c *Client) Write(ctx context.Context, session, block, data string) (int, error) {
	var out struct {
		BytesWritten int `json:"bytes_written"`
	}
	err := c.BlockMethod(ctx, session, block, "write", map[string]any{"data": data}, &out)
	return out.BytesWritten, err
}

// ClientInfo is a connected client (client.list).
type ClientInfo struct {
	ClientID        string   `json:"client_id"`
	ConnectionState string   `json:"connection_state,omitempty"`
	SessionIDs      []string `json:"session_ids,omitempty"`
	Info            struct {
		Kind     string `json:"kind,omitempty"`
		Name     string `json:"name,omitempty"`
		Label    string `json:"label,omitempty"`
		Platform string `json:"platform,omitempty"`
		Version  string `json:"version,omitempty"`
	} `json:"info"`
}

// ClientKindApp is the kind of a Rex app (Rex.app), which performs
// session.select.
const ClientKindApp = "app"

// AppClients lists the connected Rex apps, in the order they connected.
func (c *Client) AppClients(ctx context.Context) ([]ClientInfo, error) {
	var out struct {
		Clients []ClientInfo `json:"clients"`
	}
	if _, err := c.call(ctx, "client.list", map[string]any{"kinds": []string{ClientKindApp}}, &out); err != nil {
		return nil, err
	}
	apps := out.Clients[:0]
	for _, cl := range out.Clients {
		if cl.ConnectionState == "" || cl.ConnectionState == "connected" {
			apps = append(apps, cl)
		}
	}
	return apps, nil
}

// Perform is client.perform's request: ask ClientID to perform Action.
type Perform struct {
	ClientID string
	Action   string
	Args     any
	// SessionID and BlockID are passed to the client unchecked.
	SessionID, BlockID string
	// TimeoutMS is how long to wait for its answer; 0 is Rex's 10 s.
	TimeoutMS int
}

// Performed is client.perform's answer.
type Performed struct {
	PerformedBy string          `json:"performed_by"`
	Result      json.RawMessage `json:"result,omitempty"`
}

// Perform asks another client to perform one of its actions and waits for
// its answer. A client that declines fails with CodeRefused; see also
// CodeNotSupported, CodeClientUnavailable, CodeTimeout, CodeNotFound.
func (c *Client) Perform(ctx context.Context, r Perform) (Performed, error) {
	p := map[string]any{"client_id": r.ClientID, "action": r.Action}
	if r.Args != nil {
		p["args"] = r.Args
	}
	if r.SessionID != "" {
		p["session_id"] = r.SessionID
	}
	if r.BlockID != "" {
		p["block_id"] = r.BlockID
	}
	if r.TimeoutMS > 0 {
		p["timeout_ms"] = r.TimeoutMS
	}
	var out Performed
	_, err := c.call(ctx, "client.perform", p, &out)
	return out, err
}

// SelectSession asks the Rex app app to show session (its session.select
// action). Without Remote Control on, the app refuses (CodeRefused).
func (c *Client) SelectSession(ctx context.Context, app, session string) error {
	_, err := c.Perform(ctx, Perform{ClientID: app, Action: "session.select", Args: map[string]any{"session_id": session}})
	return err
}
