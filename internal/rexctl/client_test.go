package rexctl_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/est7/rex-amq-ui/internal/rexctl"
	"github.com/est7/rex-amq-ui/internal/rexctl/rexctltest"
)

const (
	session = "session:01a1e4ff-0000-7000-8000-00000000005e"
	tiled   = "block:01a1e4ff-0000-7000-8000-0000000000b1"
	right   = "block:01a1e4ff-0000-7000-8000-0000000000b2"
	panel   = "block:01a1e4ff-0000-7000-8000-0000000000b3"
	layer   = "layer:01a1e4ff-0000-7000-8000-0000000000f1"
)

// sampleView is a session.view answer shaped as `rex api describe
// session.view` documents it: two tabs, the first split in two with the
// panel floating over it.
const sampleView = `{"revision":7,"session_id":"` + session + `","label":"auth-refresh","active_window_id":"window:1",
 "windows":[
  {"window_id":"window:1","label":"main","focused_block_id":"` + right + `","layers":[
    {"layer_id":"layer:t1","kind":"tiled","bounds":[0,0,1,1],"layout":{"rect":[0,0,1,1],"split":{"split_id":"split:1","direction":"horizontal","ratio":0.6,
      "before":{"rect":[0,0,0.6,1],"block_id":"` + tiled + `"},"after":{"rect":[0.6,0,0.4,1],"block_id":"` + right + `"}}},
     "blocks":[{"block_id":"` + tiled + `","label":"claude","creator_name":"com.superlogical.terminal","flavor":"com.superlogical.terminal.shell","host_id":"h"},
               {"block_id":"` + right + `","label":"codex","owner":"client:app"}]},
    {"layer_id":"` + layer + `","kind":"floating","bounds":[0.64,0.02,0.35,0.96],"state":"minimized","layout":{"rect":[0.64,0.02,0.35,0.96],"block_id":"` + panel + `"},
     "blocks":[{"block_id":"` + panel + `","label":"rex-amq-ui"}]}]},
  {"window_id":"window:2","label":"review","layers":[{"layer_id":"layer:t2","kind":"tiled","bounds":[0,0,1,1],"layout":null,"blocks":[]}]}]}`

func dial(t *testing.T, s *rexctltest.Server) *rexctl.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := rexctl.Dial(ctx, s.URL)
	if err != nil {
		t.Fatalf("dial %s: %v", s.URL, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// attached dials s and attaches to session, as Rex requires before any of
// the session's methods.
func attached(t *testing.T, s *rexctltest.Server) *rexctl.Client {
	t.Helper()
	c := dial(t, s)
	if _, err := c.Attach(ctx(t), session); err != nil {
		t.Fatalf("attach: %v", err)
	}
	return c
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

// payloadOf is the last request of method, as plain JSON values.
func payloadOf(t *testing.T, s *rexctltest.Server, method string) map[string]any {
	t.Helper()
	r, ok := s.Last(method)
	if !ok {
		t.Fatalf("no %s request", method)
	}
	var p map[string]any
	if err := json.Unmarshal(r.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func jsonValue(t *testing.T, s string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func samePayload(t *testing.T, s *rexctltest.Server, method, want string) {
	t.Helper()
	if got, w := payloadOf(t, s, method), jsonValue(t, want); !reflect.DeepEqual(got, w) {
		gb, _ := json.Marshal(got)
		t.Errorf("%s payload\n got %s\nwant %s", method, gb, want)
	}
}

func nextEvent(t *testing.T, c *rexctl.Client) rexctl.Event {
	t.Helper()
	select {
	case e, ok := <-c.Events():
		if !ok {
			t.Fatal("events closed")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return rexctl.Event{}
}

func TestRexctlHandshake(t *testing.T) {
	s := rexctltest.NewServer(t)
	c := dial(t, s)
	if w := c.Welcome(); w.ClientID != rexctltest.ClientID || w.APIRevision != 13 || w.MaxMessageBytes != 1<<20 {
		t.Fatalf("welcome %+v", w)
	}
	hellos := s.Hellos()
	if len(hellos) != 1 {
		t.Fatalf("%d hellos", len(hellos))
	}
	if got := jsonValue(t, string(hellos[0])); !reflect.DeepEqual(got, jsonValue(t, `{"type":"hello","api_revision":13,"client":{"kind":"other","name":"rex-amq-ui"}}`)) {
		t.Fatalf("hello %s", hellos[0])
	}
}

// A response's seq is the seq of the last event the connection was sent
// when the server read the answer (live Rex: 0 before any event, then the
// last event's; run dir live-probe-seq.txt). An event at or below an
// attach's seq is already in its view; one above it is newer, even when it
// arrives before the attach's answer.
func TestRexctlAttachReadsTheViewAndOrdersEvents(t *testing.T) {
	s := rexctltest.NewServer(t)
	s.SetView(json.RawMessage(sampleView))
	s.Handle("session.attach", func(r rexctltest.Request) (any, *rexctltest.Error) {
		s.Emit(session, "session_view_changed", map[string]any{"revision": 8})
		return map[string]any{"client_id": rexctltest.ClientID, "view": json.RawMessage(sampleView)}, nil
	})
	c := dial(t, s)
	s.EmitServer("client_connected", map[string]any{"client_id": "client:other"})
	older := nextEvent(t, c)
	a, err := c.Attach(ctx(t), session)
	if err != nil {
		t.Fatal(err)
	}
	samePayload(t, s, "session.attach", `{"session_id":"`+session+`"}`)
	v := a.View
	if a.ClientID != rexctltest.ClientID || v.Revision != 7 || v.Label != "auth-refresh" || len(v.Windows) != 2 || v.FocusedBlock() != right {
		t.Fatalf("attached %+v", a)
	}
	w, l, ok := v.LayerOf(panel)
	if !ok || w.WindowID != "window:1" || l.LayerID != layer || l.Kind != rexctl.LayerFloating || l.State != rexctl.LayerMinimized || l.Bounds != (rexctl.Bounds{0.64, 0.02, 0.35, 0.96}) {
		t.Fatalf("panel layer %+v in %s (%v)", l, w.WindowID, ok)
	}
	if _, l, _ := v.LayerOf(right); l.State != "" || l.Blocks[1].Owner != "client:app" {
		t.Fatalf("tiled layer %+v", l)
	}
	if older.Name != "client_connected" || older.Seq != 1 || a.Seq != older.Seq {
		t.Fatalf("event before the attach %+v, attach seq %d", older, a.Seq)
	}
	e := nextEvent(t, c)
	vc, ok := e.ViewChanged()
	if !ok || vc.Revision != 8 || e.SessionID != session || e.Seq <= a.Seq {
		t.Fatalf("event %+v (seq %d, attach seq %d)", e, e.Seq, a.Seq)
	}
	if _, ok := e.OwnerChanged(); ok {
		t.Fatal("a view change read as an owner change")
	}

	s.Emit(session, "block_owner_changed", map[string]any{"block_id": panel, "owner": "client:app", "previous": rexctltest.ClientID, "reason": "disown"})
	s.EmitServer("session_created", map[string]any{"session": map[string]any{"session_id": "session:new"}})
	oc, ok := nextEvent(t, c).OwnerChanged()
	if !ok || oc != (rexctl.OwnerChanged{BlockID: panel, Owner: "client:app", Previous: rexctltest.ClientID, Reason: "disown"}) {
		t.Fatalf("owner change %+v", oc)
	}
	if e := nextEvent(t, c); e.Name != "session_created" || e.SessionID != "" {
		t.Fatalf("server event %+v", e)
	}

	got, err := c.View(ctx(t), session)
	if err != nil || got.Revision != 7 || got.Windows[1].Layers[0].Layout != nil {
		t.Fatalf("view %+v %v", got, err)
	}
}

// Opening the panel's floating layer and handing it to the app, then each
// layer and pane operation the panel makes. Every payload is checked by the
// fake against Rex's own input schema and here against the exact request.
func TestRexctlLayerOperations(t *testing.T) {
	s := rexctltest.NewServer(t)
	c := attached(t, s)
	cx := ctx(t)
	made, err := c.NewLayer(cx, rexctl.NewLayer{
		SessionID: session, WindowID: "window:1", Bounds: rexctl.Bounds{0.64, 0.02, 0.35, 0.96},
		Layout: rexctl.TerminalBlock("rex-amq-ui", rexctl.TerminalOptions{Shell: "none", Command: []string{"/usr/local/bin/rex-amq-ui", "--session", session},
			Size: &rexctl.StartSize{Columns: 70, Rows: 46, CellWidthPx: 9, CellHeightPx: 22}}),
	})
	if err != nil || made.LayerID == "" || len(made.BlockIDs) != 1 || made.Revision == 0 {
		t.Fatalf("new layer %+v %v", made, err)
	}
	samePayload(t, s, "session.new_layer", `{"session_id":"`+session+`","window_id":"window:1","bounds":[0.64,0.02,0.35,0.96],
	 "layout":{"block":{"flavor":"com.superlogical.terminal.shell","label":"rex-amq-ui","options":{"shell":"none","command":["/usr/local/bin/rex-amq-ui","--session","`+session+`"],
	 "size":{"columns":70,"rows":46,"cell_width_px":9,"cell_height_px":22}}}}}`)

	owners, err := c.DisownBlock(cx, session, made.BlockIDs[0])
	if err != nil || len(owners) != 1 || owners[0] != (rexctl.BlockOwner{BlockID: made.BlockIDs[0]}) {
		t.Fatalf("disown %+v %v", owners, err)
	}
	samePayload(t, s, "session.disown_block", `{"session_id":"`+session+`","block_ids":["`+made.BlockIDs[0]+`"]}`)

	if _, err := c.SetLayerState(cx, session, layer, rexctl.LayerMinimized, false); err != nil {
		t.Fatal(err)
	}
	samePayload(t, s, "session.set_layer_state", `{"session_id":"`+session+`","layer_id":"`+layer+`","state":"minimized"}`)
	rev, err := c.SetLayerState(cx, session, layer, rexctl.LayerNormal, true)
	if err != nil || rev == 0 {
		t.Fatal(rev, err)
	}
	samePayload(t, s, "session.set_layer_state", `{"session_id":"`+session+`","layer_id":"`+layer+`","state":"normal","focus":true}`)

	if _, err := c.ResizeLayer(cx, session, layer, rexctl.Bounds{0.06, 0.06, 0.88, 0.88}); err != nil {
		t.Fatal(err)
	}
	samePayload(t, s, "session.resize_layer", `{"session_id":"`+session+`","layer_id":"`+layer+`","bounds":[0.06,0.06,0.88,0.88]}`)

	if _, err := c.FocusBlock(cx, session, right); err != nil {
		t.Fatal(err)
	}
	samePayload(t, s, "session.focus_block", `{"session_id":"`+session+`","block_id":"`+right+`"}`)

	moved, err := c.MoveBlock(cx, rexctl.MoveBlock{SessionID: session, BlockID: panel, AnchorBlockID: tiled})
	if err != nil || moved.CreatedSplitID == "" {
		t.Fatalf("move %+v %v", moved, err)
	}
	samePayload(t, s, "session.move_block", `{"session_id":"`+session+`","block_id":"`+panel+`","anchor_block_id":"`+tiled+`"}`)
	if _, err := c.MoveBlock(cx, rexctl.MoveBlock{SessionID: session, BlockID: panel, AnchorBlockID: tiled, Direction: "vertical", Side: "before", Ratio: 0.3}); err != nil {
		t.Fatal(err)
	}
	samePayload(t, s, "session.move_block", `{"session_id":"`+session+`","block_id":"`+panel+`","anchor_block_id":"`+tiled+`","direction":"vertical","side":"before","ratio":0.3}`)

	floated, err := c.FloatBlock(cx, session, panel, rexctl.Bounds{0.64, 0.02, 0.35, 0.96})
	if err != nil || floated.LayerID == "" || floated.Revision == 0 {
		t.Fatalf("float %+v %v", floated, err)
	}
	samePayload(t, s, "session.float_block", `{"session_id":"`+session+`","block_id":"`+panel+`","bounds":[0.64,0.02,0.35,0.96]}`)
}

func TestRexctlBlockSizeAndWrite(t *testing.T) {
	s := rexctltest.NewServer(t)
	c := attached(t, s)
	size, err := c.TerminalSize(ctx(t), session, panel)
	if err != nil || size != (rexctl.TerminalSize{Columns: 120, Rows: 40, CellWidthPx: 9, CellHeightPx: 22}) {
		t.Fatalf("size %+v %v", size, err)
	}
	samePayload(t, s, "block.method", `{"session_id":"`+session+`","block_id":"`+panel+`","method":"size","args":{}}`)
	s.Handle("block.method", func(rexctltest.Request) (any, *rexctltest.Error) {
		return map[string]any{"columns": 70, "rows": 46, "cell_width_px": 9, "cell_height_px": 22, "owner": "client:app", "desired": []any{}}, nil
	})
	if size, err := c.TerminalSize(ctx(t), session, panel); err != nil || size.Owner != "client:app" || size.Columns != 70 {
		t.Fatalf("owned size %+v %v", size, err)
	}
	s.Handle("block.method", nil)

	n, err := c.Write(ctx(t), session, panel, "\x1b_amqui;pause\x1b\\")
	if err != nil || n != len("\x1b_amqui;pause\x1b\\") {
		t.Fatal(n, err)
	}
	samePayload(t, s, "block.method", `{"session_id":"`+session+`","block_id":"`+panel+`","method":"write","args":{"data":"\u001b_amqui;pause\u001b\\"}}`)
}

func TestRexctlSelectSessionThroughTheApp(t *testing.T) {
	s := rexctltest.NewServer(t)
	c := dial(t, s)
	apps, err := c.AppClients(ctx(t))
	if err != nil || len(apps) != 1 || apps[0].ClientID != rexctltest.AppClientID || apps[0].Info.Kind != rexctl.ClientKindApp {
		t.Fatalf("apps %+v %v", apps, err)
	}
	samePayload(t, s, "client.list", `{"kinds":["app"]}`)
	if err := c.SelectSession(ctx(t), apps[0].ClientID, "session:epic"); err != nil {
		t.Fatal(err)
	}
	samePayload(t, s, "client.perform", `{"client_id":"`+rexctltest.AppClientID+`","action":"session.select","args":{"session_id":"session:epic"}}`)

	s.Handle("client.perform", func(rexctltest.Request) (any, *rexctltest.Error) {
		return nil, &rexctltest.Error{Code: "refused", Message: "Remote Control is off", Status: 403}
	})
	err = c.SelectSession(ctx(t), apps[0].ClientID, "session:epic")
	var re *rexctl.Error
	if !rexctl.IsCode(err, rexctl.CodeRefused) || !errors.As(err, &re) || re.Method != "client.perform" || re.Status != 403 || !strings.Contains(err.Error(), "Remote Control is off") {
		t.Fatalf("refusal %v", err)
	}
}

// A connection that drops fails the calls waiting on it and closes the
// event channel; later calls fail at once.
func TestRexctlConnectionLoss(t *testing.T) {
	s := rexctltest.NewServer(t)
	c := attached(t, s)
	s.Handle("session.view", func(rexctltest.Request) (any, *rexctltest.Error) {
		s.CloseConnections()
		return nil, nil
	})
	if _, err := c.View(ctx(t), session); !errors.Is(err, rexctl.ErrClosed) {
		t.Fatalf("call during the drop: %v", err)
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed")
	}
	if !errors.Is(c.Err(), rexctl.ErrClosed) {
		t.Fatalf("Err %v", c.Err())
	}
	if _, ok := <-c.Events(); ok {
		t.Fatal("events open after the drop")
	}
	if _, err := c.FocusBlock(ctx(t), session, right); !errors.Is(err, rexctl.ErrClosed) {
		t.Fatalf("call after the drop: %v", err)
	}
}

func TestRexctlErrorsAndLimits(t *testing.T) {
	s := rexctltest.NewServer(t)
	s.Handle("session.focus_block", func(rexctltest.Request) (any, *rexctltest.Error) {
		return nil, &rexctltest.Error{Code: "not_found", Message: "block not found", Status: 404}
	})
	c := attached(t, s)
	if _, err := c.FocusBlock(ctx(t), session, "block:gone"); !rexctl.IsCode(err, rexctl.CodeNotFound) {
		t.Fatalf("focus of a gone block: %v", err)
	}
	before := len(s.Requests())
	if _, err := c.Write(ctx(t), session, panel, strings.Repeat("x", 2<<20)); !errors.Is(err, rexctl.ErrRequestTooBig) {
		t.Fatalf("oversized write: %v", err)
	}
	if len(s.Requests()) != before {
		t.Fatal("an oversized request was sent; the server would close the connection")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.View(cancelled, session); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call: %v", err)
	}
	if _, err := c.View(ctx(t), session); err != nil {
		t.Fatalf("the connection did not survive a cancelled call: %v", err)
	}

	// Cancelled while waiting for its answer: the call returns, the late
	// answer is dropped, and the connection still serves the next call.
	release := make(chan struct{})
	s.Handle("session.resize_layer", func(rexctltest.Request) (any, *rexctltest.Error) {
		<-release
		return map[string]any{"revision": 1}, nil
	})
	waiting, cancelWait := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.ResizeLayer(waiting, session, layer, rexctl.Bounds{0, 0, 1, 1})
		done <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if _, ok := s.Last("session.resize_layer"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the resize never reached the server")
		}
	}
	cancelWait()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("call cancelled while waiting: %v", err)
	}
	close(release)
	if _, err := c.FocusBlock(ctx(t), session, right); !rexctl.IsCode(err, rexctl.CodeNotFound) {
		t.Fatalf("the connection did not survive a call cancelled while waiting: %v", err)
	}
}

func TestRexctlSocketPath(t *testing.T) {
	p, err := rexctl.SocketPath("unix:///Users/a/Library/Application%20Support/rex/server.sock")
	if err != nil || p != "/Users/a/Library/Application Support/rex/server.sock" {
		t.Fatal(p, err)
	}
	if _, err := rexctl.SocketPath("ws://localhost:1234"); !errors.Is(err, rexctl.ErrNotUnixURL) {
		t.Fatal(err)
	}
	if _, err := rexctl.SocketPath("unix:///" + strings.Repeat("a", 200)); !errors.Is(err, rexctl.ErrNameTooLong) {
		t.Fatal(err)
	}
	if _, err := rexctl.Dial(ctx(t), "unix:///tmp/rexctl-no-such-dir/s.sock"); err == nil {
		t.Fatal("dialed a socket that does not exist")
	}
}

// A server that stops reading blocks the request being written. A call
// behind it returns when its own context ends, is never sent, and the
// connection serves the next call once the server reads again.
func TestRexctlCallsDoNotWaitForABlockedWriter(t *testing.T) {
	s := rexctltest.NewServer(t)
	c := attached(t, s)
	resume := s.PauseReading()
	defer resume()
	// The fake's reader may already be waiting in a read: this request gets
	// through, and the reads stop after it.
	if _, err := c.FocusBlock(ctx(t), session, right); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 900<<10)
	wrote := make(chan error, 1)
	go func() {
		_, err := c.Write(context.Background(), session, panel, big)
		wrote <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); !rexctl.Writing(c); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the large write never started")
		}
	}

	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.FocusBlock(short, session, "block:cancelled")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("a call behind a blocked write returned %v after %v", err, time.Since(start))
	}

	resume()
	if err := <-wrote; err != nil {
		t.Fatalf("the blocked write: %v", err)
	}
	if _, err := c.View(ctx(t), session); err != nil {
		t.Fatalf("the connection after the blocked write: %v", err)
	}
	for _, r := range s.Requests() {
		if r.Method == "session.focus_block" && strings.Contains(string(r.Payload), "block:cancelled") {
			t.Fatal("the cancelled call was sent")
		}
	}
}
