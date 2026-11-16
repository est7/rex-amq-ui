package rexctltest_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/est7/rex-amq-ui/internal/rexctl"
	"github.com/est7/rex-amq-ui/internal/rexctl/rexctltest"
)

// recorder is a test that only records what the fake reports.
type recorder struct {
	testing.TB
	mu   sync.Mutex
	errs []string
}

func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, format)
	_ = args
}

// The fake holds requests to Rex's published input schemas: a misspelled
// field, a missing required one, a wrong type, or an unknown block method
// is refused and fails the test that sent it.
func TestRexctltestRefusesRequestsOffSchema(t *testing.T) {
	cases := []struct {
		method  string
		payload any
		want    string
	}{
		{"session.focus_block", map[string]any{"session_id": "s", "blockid": "b"}, "missing required"},
		{"session.focus_block", map[string]any{"session_id": "s", "block_id": "b", "extra": 1}, "unknown field"},
		{"session.resize_layer", map[string]any{"session_id": "s", "layer_id": "l", "bounds": []float64{0, 0, 1}}, "anyOf"},
		{"session.set_layer_state", map[string]any{"session_id": "s", "layer_id": "l", "state": 3}, "not of type"},
		{"block.method", map[string]any{"session_id": "s", "block_id": "b", "method": "write", "args": map[string]any{"text": "x"}}, "missing required"},
		{"block.method", map[string]any{"session_id": "s", "block_id": "b", "method": "explode"}, "not one the fake knows"},
		{"session.new_layer", map[string]any{"session_id": "s", "bounds": []float64{0, 0, 1, 1},
			"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"size": map[string]any{"colums": 80, "rows": 24}}}}}, "payload.layout.block.options.size"},
	}
	for _, c := range cases {
		rec := &recorder{TB: t}
		s := rexctltest.NewServer(rec)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cl, err := rexctl.Dial(ctx, s.URL)
		if err != nil {
			t.Fatal(err)
		}
		err = cl.Call(ctx, c.method, c.payload, nil)
		cl.Close()
		cancel()
		if !rexctl.IsCode(err, rexctl.CodeBadRequest) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v: %v, want bad_request mentioning %q", c.method, c.payload, err, c.want)
		}
	}
}

// Every request the client's methods send in the rexctl tests also passes
// here with a correct payload: the fake is not refusing everything.
func TestRexctltestAcceptsRequestsOnSchema(t *testing.T) {
	s := rexctltest.NewServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cl, err := rexctl.Dial(ctx, s.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if _, err := cl.Attach(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	if err := cl.Call(ctx, "session.focus_block", map[string]any{"session_id": "s", "block_id": "b"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := cl.Call(ctx, "session.resize_layer", map[string]any{"session_id": "s", "layer_id": "l", "bounds": map[string]any{"x": 0, "y": 0, "w": 1, "h": 1}}, nil); err != nil {
		t.Fatal(err)
	}
}

const attachSession = "session:01a1e4ff-0000-7000-8000-0000000000a1"

func dialFake(t *testing.T, s *rexctltest.Server) *rexctl.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := rexctl.Dial(ctx, s.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func shortCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// nextName is the name of the next event, or "" when none comes in time.
func nextName(c *rexctl.Client, wait time.Duration) string {
	select {
	case e := <-c.Events():
		return e.Name
	case <-time.After(wait):
		return ""
	}
}

// As session.attach's notes say: a session's methods fail with conflict on
// a connection not attached to it, and its events start on attach. An
// attachment belongs to its connection.
func TestRexctltestRequiresAttach(t *testing.T) {
	s := rexctltest.NewServer(t)
	c := dialFake(t, s)
	for name, call := range map[string]func() error{
		"view":   func() error { _, err := c.View(shortCtx(t), attachSession); return err },
		"focus":  func() error { _, err := c.FocusBlock(shortCtx(t), attachSession, "block:b"); return err },
		"disown": func() error { _, err := c.DisownBlock(shortCtx(t), attachSession, "block:b"); return err },
		"size":   func() error { _, err := c.TerminalSize(shortCtx(t), attachSession, "block:b"); return err },
		"layer": func() error {
			_, err := c.SetLayerState(shortCtx(t), attachSession, "layer:l", rexctl.LayerNormal, false)
			return err
		},
	} {
		if err := call(); !rexctl.IsCode(err, rexctl.CodeConflict) {
			t.Errorf("%s before attach: %v, want conflict", name, err)
		}
	}
	// A session event before the attach is not sent; a server event is.
	s.Emit(attachSession, "session_view_changed", map[string]any{"revision": 2})
	s.EmitServer("marker-1", map[string]any{})
	if got := nextName(c, 5*time.Second); got != "marker-1" {
		t.Fatalf("first event %q, want marker-1: a session event reached an unattached connection", got)
	}
	if _, err := c.Attach(shortCtx(t), attachSession); err != nil {
		t.Fatal(err)
	}
	if _, err := c.FocusBlock(shortCtx(t), attachSession, "block:b"); err != nil {
		t.Fatalf("focus after attach: %v", err)
	}
	s.Emit("session:other", "session_view_changed", map[string]any{"revision": 9})
	s.Emit(attachSession, "session_view_changed", map[string]any{"revision": 3})
	if got := nextName(c, 5*time.Second); got != "session_view_changed" {
		t.Fatalf("after attach: %q", got)
	}
	s.EmitServer("marker-2", map[string]any{})
	if got := nextName(c, 5*time.Second); got != "marker-2" {
		t.Fatalf("an event of another session reached the connection: %q", got)
	}

	c2 := dialFake(t, s)
	if _, err := c2.FocusBlock(shortCtx(t), attachSession, "block:b"); !rexctl.IsCode(err, rexctl.CodeConflict) {
		t.Fatalf("a new connection inherited the attachment: %v", err)
	}
	s.Emit(attachSession, "session_view_changed", map[string]any{"revision": 4})
	s.EmitServer("marker-3", map[string]any{})
	if got := nextName(c2, 5*time.Second); got != "marker-3" {
		t.Fatalf("an unattached connection got %q", got)
	}
}

// block.method does not wait for earlier requests on its connection:
// a size call answers while a layer request ahead of it is still running.
func TestRexctltestBlockMethodOvertakesABlockedRequest(t *testing.T) {
	s := rexctltest.NewServer(t)
	release := make(chan struct{})
	s.Handle("session.resize_layer", func(rexctltest.Request) (any, *rexctltest.Error) {
		<-release
		return map[string]any{"revision": 2}, nil
	})
	c := dialFake(t, s)
	if _, err := c.Attach(shortCtx(t), attachSession); err != nil {
		t.Fatal(err)
	}
	resized := make(chan error, 1)
	go func() {
		_, err := c.ResizeLayer(context.Background(), attachSession, "layer:l", rexctl.Bounds{0, 0, 1, 1})
		resized <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if _, ok := s.Last("session.resize_layer"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the resize never reached the fake")
		}
	}
	if _, err := c.TerminalSize(shortCtx(t), attachSession, "block:b"); err != nil {
		t.Fatalf("size behind a blocked resize: %v", err)
	}
	select {
	case err := <-resized:
		t.Fatalf("the resize finished before it was released: %v", err)
	default:
	}
	close(release)
	if err := <-resized; err != nil {
		t.Fatal(err)
	}
}
