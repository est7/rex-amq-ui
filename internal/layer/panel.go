package layer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/est7/rex-amq-ui/internal/rexctl"
)

// State is what the panel knows of its layer and the human's focus. Watch
// sends a new State whenever one of its fields changes.
type State struct {
	// Focused is the pane the human was last in outside the panel, "" when
	// not known yet.
	Focused string
	// Owned: a Rex app owns the panel's block, so the terminal answers
	// colour queries (OSC 10/11) with the app's real colours.
	Owned bool
	// Minimized: the panel's layer is minimized.
	Minimized bool
	// Size is the size the layer's bounds are closest to.
	Size Size
	// Connected: the control connection is up.
	Connected bool
}

// Panel is the panel's own layer, driven from inside it. Its methods are
// safe for concurrent use.
type Panel struct {
	server, session, block string

	mu    sync.Mutex
	c     *rexctl.Client // nil while disconnected
	layer string
	state State
	rev   int64
}

// Connect dials the server (REX_SERVER), attaches to session, and finds
// the panel's layer from its block: block (REX_BLOCK), or the block
// labelled Label when "". focused is the pane the human was in when the
// panel opened (`open` passes it as --focus); it stands until Rex reports
// focus on another pane.
func Connect(ctx context.Context, server, session, block, focused string) (*Panel, error) {
	p := &Panel{server: server, session: session, block: block}
	p.state.Focused = focused
	c, err := rexctl.Dial(ctx, server)
	if err != nil {
		return nil, err
	}
	if err := p.adopt(ctx, c); err != nil {
		c.Close()
		return nil, err
	}
	return p, nil
}

// adopt attaches c, reads the panel's place from the view, and makes c the
// panel's connection.
func (p *Panel) adopt(ctx context.Context, c *rexctl.Client) error {
	a, err := c.Attach(ctx, p.session)
	if err != nil {
		return err
	}
	block, layer, ok := find(a.View, p.block)
	if !ok {
		return fmt.Errorf("%w: block %q in session %s", ErrNoPanel, p.block, p.session)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.c, p.block, p.layer, p.rev = c, block, layer.LayerID, a.View.Revision
	p.state = p.next(a.View)
	return nil
}

// next is the state v shows, keeping what v cannot say from the last one.
// Called with mu held.
func (p *Panel) next(v rexctl.View) State {
	s := p.state
	s.Connected = true
	if f := v.FocusedBlock(); f != "" && f != p.block {
		s.Focused = f
	}
	if _, l, ok := v.LayerOf(p.block); ok {
		s.Minimized = l.State == rexctl.LayerMinimized
		s.Size = SizeOf(l.Bounds)
		for _, b := range l.Blocks {
			if b.BlockID == p.block {
				s.Owned = b.Owner != ""
			}
		}
	}
	return s
}

// State is the panel's state now.
func (p *Panel) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// Block is the panel's own block.
func (p *Panel) Block() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.block
}

func (p *Panel) client() (*rexctl.Client, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.c == nil {
		return nil, "", ErrDisconnected
	}
	return p.c, p.layer, nil
}

// Resize sets the layer to size s.
func (p *Panel) Resize(ctx context.Context, s Size) error {
	c, layer, err := p.client()
	if err != nil {
		return err
	}
	_, err = c.ResizeLayer(ctx, p.session, layer, s.Bounds())
	return err
}

// Minimize minimizes the layer; Rex gives focus back to the pane the human
// used before it.
func (p *Panel) Minimize(ctx context.Context) error {
	c, layer, err := p.client()
	if err != nil {
		return err
	}
	_, err = c.SetLayerState(ctx, p.session, layer, rexctl.LayerMinimized, false)
	return err
}

// Jump takes the human to block: it minimizes the layer first, since
// minimizing a focused layer moves focus on its own, then focuses block
// (and its window).
func (p *Panel) Jump(ctx context.Context, block string) error {
	if err := p.Minimize(ctx); err != nil {
		return err
	}
	c, _, err := p.client()
	if err != nil {
		return err
	}
	_, err = c.FocusBlock(ctx, p.session, block)
	return err
}

// SwitchTo asks the Rex app to show session, then minimizes the panel. On
// ErrRemoteControlOff or ErrNoApp the panel stays up to say so.
func (p *Panel) SwitchTo(ctx context.Context, session string) error {
	c, _, err := p.client()
	if err != nil {
		return err
	}
	apps, err := c.AppClients(ctx)
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		return ErrNoApp
	}
	if err := c.SelectSession(ctx, apps[0].ClientID, session); err != nil {
		if rexctl.IsCode(err, rexctl.CodeRefused) {
			return ErrRemoteControlOff
		}
		return err
	}
	return p.Minimize(ctx)
}

// OpenBeside runs argv in a new terminal block labelled label, split in
// before anchor (a pane of a window's layout), and hands its sizing to
// Rex.app.
func (p *Panel) OpenBeside(ctx context.Context, anchor, label string, argv []string) error {
	c, _, err := p.client()
	if err != nil {
		return err
	}
	var made struct {
		BlockID string `json:"block_id"`
	}
	err = c.Call(ctx, "session.new_block", map[string]any{
		"session_id": p.session, "flavor": rexctl.TerminalFlavor, "label": label,
		"options": rexctl.TerminalOptions{Shell: "none", Command: argv},
	}, &made)
	if err != nil {
		return err
	}
	if _, err := c.MoveBlock(ctx, rexctl.MoveBlock{SessionID: p.session, BlockID: made.BlockID, AnchorBlockID: anchor, Side: "before"}); err != nil {
		return err
	}
	_, err = c.DisownBlock(ctx, p.session, made.BlockID)
	return err
}

// Redial bounds the wait between reconnects.
const (
	minRedial = 250 * time.Millisecond
	maxRedial = 5 * time.Second
)

// Watch follows the session and sends the panel's State each time it
// changes, starting with the state now. When the connection ends (closed,
// or its events overflowed) it dials again, attaches, and rereads the view,
// sending a disconnected State meanwhile. It returns when ctx ends.
func (p *Panel) Watch(ctx context.Context, send func(State)) {
	sent := p.State()
	send(sent)
	emit := func() {
		if s := p.State(); s != sent {
			sent = s
			send(s)
		}
	}
	wait := minRedial
	for ctx.Err() == nil {
		c, _, err := p.client()
		if err == nil {
			p.follow(ctx, c, emit)
			c.Close()
			p.mu.Lock()
			p.c = nil
			p.state.Connected = false
			p.mu.Unlock()
			emit()
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		c, err = rexctl.Dial(ctx, p.server)
		if err == nil {
			if err = p.adopt(ctx, c); err != nil {
				c.Close()
			}
		}
		if err != nil {
			wait = min(wait*2, maxRedial)
			continue
		}
		wait = minRedial
		emit()
	}
}

// follow applies c's events, emitting each change, until the connection or
// ctx ends.
func (p *Panel) follow(ctx context.Context, c *rexctl.Client, emit func()) {
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-c.Events():
			if !ok {
				return
			}
			if !p.apply(ctx, c, e) {
				return
			}
			emit()
		}
	}
}

// apply folds one event in, rereading the view when it changed; false when
// the connection is no longer usable.
func (p *Panel) apply(ctx context.Context, c *rexctl.Client, e rexctl.Event) bool {
	if o, ok := e.OwnerChanged(); ok {
		p.mu.Lock()
		if o.BlockID == p.block {
			p.state.Owned = o.Owner != ""
		}
		p.mu.Unlock()
	}
	if vc, ok := e.ViewChanged(); ok {
		p.mu.Lock()
		stale := vc.Revision > p.rev
		p.mu.Unlock()
		if stale {
			v, err := c.View(ctx, p.session)
			if err != nil {
				return !errors.Is(err, rexctl.ErrClosed)
			}
			p.mu.Lock()
			if v.Revision > p.rev {
				p.rev = v.Revision
				p.state = p.next(v)
			}
			p.mu.Unlock()
		}
	}
	return true
}
