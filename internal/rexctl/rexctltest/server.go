// Package rexctltest is a fake Rex control server (rex.control.v1) for tests:
// a WebSocket on a Unix socket that welcomes a client, answers its requests,
// and sends it events. It is strict: every request is checked against the
// input schema Rex itself publishes (schemas/, from `rex api describe --json`
// on api revision 13), and a request that does not match fails the test, so
// a misspelled field cannot pass against the fake.
package rexctltest

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

// Protocol is the subprotocol the fake accepts.
const Protocol = "rex.control.v1"

// APIRevision is the revision the fake reports in its welcome.
const APIRevision = 13

// ClientID is the id the fake gives every connection.
const ClientID = "client:01a1e4ff-0000-7000-8000-00000000c11e"

// AppClientID is the Rex app the fake lists in client.list.
const AppClientID = "client:01a1e4ff-0000-7000-8000-0000000000a9"

//go:embed schemas/*.json
var schemaFS embed.FS

// Request is a request the fake received.
type Request struct {
	ID      string
	Method  string
	Payload json.RawMessage
}

// Decode unmarshals the request's payload into v.
func (r Request) Decode(v any) error { return json.Unmarshal(r.Payload, v) }

// Error is an error answer.
type Error struct {
	Code    string
	Message string
	Status  int
}

// Handler answers a request: its result, or an error.
type Handler func(Request) (any, *Error)

// Server is a running fake.
type Server struct {
	// URL is the server's address in REX_SERVER form.
	URL string
	// MaxMessageBytes is the limit the welcome reports.
	MaxMessageBytes int

	t        testing.TB
	http     *http.Server
	inputs   map[string]*schema // method -> input schema
	blockArg map[string]*schema // terminal block method -> args schema
	termOpts *schema            // a terminal block's options in a layout

	mu         sync.Mutex
	handlers   map[string]Handler
	conns      map[*conn]bool
	revision   int64
	made       int
	requests   []Request
	hellos     []json.RawMessage
	violations []string
	view       any
}

// conn is one client connection. Like Rex, it numbers the events it sends
// from 1, and a response's seq is the seq of the last event sent before
// the request was answered (0 before any): the client applies an attach's
// view, then the events with a higher seq, even those that arrived first.
type conn struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
	seq     int64 // last event seq; guarded by writeMu
}

// NewServer starts a fake on a Unix socket under a short temporary
// directory (sun_path is 104 bytes, and t.TempDir is often longer); it stops
// when the test ends, and fails the test if any request broke its schema.
func NewServer(t testing.TB) *Server {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rexctl-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		URL: "unix://" + (&url.URL{Path: path}).EscapedPath(), MaxMessageBytes: 1 << 20, t: t,
		handlers: map[string]Handler{}, conns: map[*conn]bool{}, revision: 1,
	}
	if err := s.loadSchemas(); err != nil {
		t.Fatal(err)
	}
	s.http = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 5 * time.Second}
	go s.http.Serve(ln)
	t.Cleanup(func() {
		s.http.Close()
		s.CloseConnections()
		os.RemoveAll(dir)
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, v := range s.violations {
			t.Errorf("rexctltest: %s", v)
		}
	})
	return s
}

func (s *Server) loadSchemas() error {
	s.inputs, s.blockArg = map[string]*schema{}, map[string]*schema{}
	entries, err := schemaFS.ReadDir("schemas")
	if err != nil {
		return err
	}
	for _, e := range entries {
		b, err := schemaFS.ReadFile("schemas/" + e.Name())
		if err != nil {
			return err
		}
		var d struct {
			Name   string  `json:"name"`
			Input  *schema `json:"input_schema"`
			Args   *schema `json:"args_schema"`
			Opts   *schema `json:"options_schema"`
			Invoke struct {
				Method string `json:"method"`
			} `json:"invoke"`
		}
		if err := json.Unmarshal(b, &d); err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
		switch {
		case d.Input != nil:
			s.inputs[d.Name] = d.Input
		case d.Args != nil:
			s.blockArg[strings.TrimPrefix(d.Name, "com.superlogical.terminal.")] = d.Args
		case d.Opts != nil:
			s.termOpts = d.Opts
		}
	}
	return nil
}

// Handle answers method with h instead of the fake's default.
func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

// SetView is the view session.attach and session.view answer with.
func (s *Server) SetView(v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.view = v
}

// Requests are the requests received so far, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Last is the last request of method.
func (s *Server) Last(method string) (Request, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.requests) - 1; i >= 0; i-- {
		if s.requests[i].Method == method {
			return s.requests[i], true
		}
	}
	return Request{}, false
}

// Hellos are the hello messages received, one per connection.
func (s *Server) Hellos() []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]json.RawMessage(nil), s.hellos...)
}

// Emit sends every connection a session event.
func (s *Server) Emit(session, name string, payload any) {
	s.broadcast(func(seq int64) any {
		return map[string]any{"type": "event", "seq": seq, "payload": map[string]any{"type": "session_event",
			"payload": map[string]any{"session_id": session, "event": map[string]any{"type": name, "payload": payload}}}}
	})
}

// EmitServer sends every connection a server event.
func (s *Server) EmitServer(name string, payload any) {
	s.broadcast(func(seq int64) any {
		return map[string]any{"type": "event", "seq": seq, "payload": map[string]any{"type": name, "payload": payload}}
	})
}

// CloseConnections drops every connection, as a server that goes away.
func (s *Server) CloseConnections() {
	s.mu.Lock()
	conns := s.conns
	s.conns = map[*conn]bool{}
	s.mu.Unlock()
	for c := range conns {
		c.ws.CloseNow()
	}
}

func (s *Server) broadcast(msg func(seq int64) any) {
	s.mu.Lock()
	var conns []*conn
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.sendEvent(msg)
	}
}

func (s *Server) violate(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.violations = append(s.violations, fmt.Sprintf(format, args...))
}

func (c *conn) send(v any) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.write(v)
}

// sendEvent numbers an event and sends it.
func (c *conn) sendEvent(msg func(seq int64) any) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.seq++
	c.write(msg(c.seq))
}

// watermark is the seq of the last event sent.
func (c *conn) watermark() int64 {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.seq
}

func (c *conn) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.ws.Write(ctx, websocket.MessageText, b)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/control" {
		s.violate("connection to %s, not /api/control", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Protocol}})
	if err != nil {
		return
	}
	if ws.Subprotocol() != Protocol {
		s.violate("connection without the %s subprotocol", Protocol)
		ws.Close(websocket.StatusPolicyViolation, "subprotocol")
		return
	}
	ws.SetReadLimit(int64(s.MaxMessageBytes))
	c := &conn{ws: ws}
	ctx := context.Background()
	_, hello, err := ws.Read(ctx)
	if err != nil {
		return
	}
	var h struct {
		Type        string `json:"type"`
		APIRevision *int   `json:"api_revision"`
	}
	if json.Unmarshal(hello, &h) != nil || h.Type != "hello" || h.APIRevision == nil {
		s.violate("first message is not a hello with api_revision: %s", hello)
		ws.Close(websocket.StatusPolicyViolation, "hello")
		return
	}
	s.mu.Lock()
	s.hellos = append(s.hellos, hello)
	s.conns[c] = true
	s.mu.Unlock()
	c.send(map[string]any{"type": "welcome", "protocol": Protocol, "api_revision": APIRevision,
		"limits": map[string]any{"max_message_bytes": s.MaxMessageBytes}, "client": map[string]any{"id": ClientID, "reconnect_token": "token"}})
	for {
		_, msg, err := ws.Read(ctx)
		if err != nil {
			s.mu.Lock()
			delete(s.conns, c)
			s.mu.Unlock()
			return
		}
		s.request(c, msg)
	}
}

func (s *Server) request(c *conn, msg []byte) {
	var m struct {
		Type    string          `json:"type"`
		ID      string          `json:"id"`
		Method  string          `json:"method"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(msg, &m); err != nil || m.Type != "request" || m.ID == "" || m.Method == "" {
		s.violate("not a request: %s", msg)
		return
	}
	req := Request{ID: m.ID, Method: m.Method, Payload: m.Payload}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	h := s.handlers[m.Method]
	s.mu.Unlock()

	// The state the answer reads is taken now: an event the handler sends
	// comes after it, with a higher seq, though it arrives first.
	seq := c.watermark()
	result, rerr := s.answer(req, h)
	out := map[string]any{"type": "response", "id": m.ID, "seq": seq}
	if rerr != nil {
		out["error"] = map[string]any{"code": rerr.Code, "message": rerr.Message, "status": rerr.Status}
	} else if result != nil {
		out["payload"] = result
	}
	c.send(out)
}

func (s *Server) answer(req Request, h Handler) (any, *Error) {
	if in, ok := s.inputs[req.Method]; ok {
		if err := s.checkInput(req, in); err != nil {
			s.violate("%s: %v: %s", req.Method, err, req.Payload)
			return nil, &Error{Code: "bad_request", Message: err.Error(), Status: 400}
		}
	} else if h == nil {
		return nil, &Error{Code: "method_not_found", Message: "no method " + req.Method, Status: 404}
	}
	if h != nil {
		return h(req)
	}
	return s.byDefault(req)
}

func (s *Server) checkInput(req Request, in *schema) error {
	v, err := decode(req.Payload)
	if err != nil {
		return err
	}
	if err := in.check("payload", v); err != nil {
		return err
	}
	if req.Method == "session.new_layer" {
		return s.checkLayout(v)
	}
	if req.Method != "block.method" {
		return nil
	}
	m, _ := v.(map[string]any)
	name, _ := m["method"].(string)
	args, ok := s.blockArg[name]
	if !ok {
		return fmt.Errorf("block method %q is not one the fake knows", name)
	}
	a, present := m["args"]
	if !present {
		a = map[string]any{}
	}
	return args.check("payload.args", a)
}

// checkLayout checks the options of a terminal block in a new layer's
// layout against the terminal flavor's options_schema.
func (s *Server) checkLayout(v any) error {
	layout, _ := v.(map[string]any)["layout"].(map[string]any)
	block, ok := layout["block"].(map[string]any)
	if !ok || block["flavor"] != "com.superlogical.terminal.shell" {
		return nil
	}
	opts, present := block["options"]
	if !present {
		return nil
	}
	return s.termOpts.check("payload.layout.block.options", opts)
}

func decode(b json.RawMessage) (any, error) {
	if len(b) == 0 {
		return nil, errors.New("no payload")
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	var v any
	return v, d.Decode(&v)
}

// byDefault answers as Rex would for a request it accepts, from the
// methods' output schemas and response examples.
func (s *Server) byDefault(req Request) (any, *Error) {
	var p map[string]any
	_ = json.Unmarshal(req.Payload, &p)
	s.mu.Lock()
	defer s.mu.Unlock()
	rev := func() int64 { s.revision++; return s.revision }
	made := func(kind string) string {
		s.made++
		return fmt.Sprintf("%s:01a1e4ff-0000-7000-8000-%012d", kind, s.made)
	}
	session, _ := p["session_id"].(string)
	view := s.view
	if view == nil {
		view = map[string]any{"revision": s.revision, "session_id": session, "label": "fake", "windows": []any{}}
	}
	switch req.Method {
	case "session.attach":
		return map[string]any{"client_id": ClientID, "view": view}, nil
	case "session.view":
		return view, nil
	case "session.new_layer":
		return map[string]any{"layer_id": made("layer"), "block_ids": []string{made("block")}, "revision": rev()}, nil
	case "session.disown_block":
		var blocks []map[string]any
		ids, _ := p["block_ids"].([]any)
		for _, id := range ids {
			blocks = append(blocks, map[string]any{"block_id": id})
		}
		return map[string]any{"blocks": blocks}, nil
	case "session.set_layer_state", "session.resize_layer", "session.focus_block":
		return map[string]any{"revision": rev()}, nil
	case "session.move_block":
		return map[string]any{"created_split_id": made("split"), "revision": rev()}, nil
	case "session.float_block":
		return map[string]any{"layer_id": made("layer"), "revision": rev()}, nil
	case "block.method":
		args, _ := p["args"].(map[string]any)
		switch p["method"] {
		case "size":
			return map[string]any{"columns": 120, "rows": 40, "cell_width_px": 9, "cell_height_px": 22, "owner": nil, "desired": []any{}}, nil
		case "write":
			data, _ := args["data"].(string)
			return map[string]any{"bytes_written": len(data)}, nil
		}
		return nil, &Error{Code: "method_not_found", Message: "the fake has no default for this block method", Status: 404}
	case "client.list":
		return map[string]any{"clients": []any{
			map[string]any{"client_id": AppClientID, "connection_state": "connected", "info": map[string]any{"kind": "app", "name": "Rex", "platform": "macos"}},
		}}, nil
	case "client.perform":
		return map[string]any{"performed_by": p["client_id"]}, nil
	}
	return nil, &Error{Code: "method_not_found", Message: "the fake has no default for " + req.Method, Status: 404}
}

// schema is the part of JSON Schema that Rex's input schemas use.
type schema struct {
	Type                 any                `json:"type"`
	Properties           map[string]*schema `json:"properties"`
	Required             []string           `json:"required"`
	AdditionalProperties *bool              `json:"additionalProperties"`
	Items                *schema            `json:"items"`
	AnyOf                []*schema          `json:"anyOf"`
	Enum                 []any              `json:"enum"`
	MinItems             *int               `json:"minItems"`
	MaxItems             *int               `json:"maxItems"`
}

func (sc *schema) check(path string, v any) error {
	if sc == nil {
		return nil
	}
	if len(sc.AnyOf) > 0 {
		var errs []string
		for _, alt := range sc.AnyOf {
			err := alt.check(path, v)
			if err == nil {
				return nil
			}
			errs = append(errs, err.Error())
		}
		return fmt.Errorf("%s matches none of anyOf: %s", path, strings.Join(errs, "; "))
	}
	if !sc.typeOK(v) {
		return fmt.Errorf("%s: %s is not of type %v", path, kindOf(v), sc.Type)
	}
	if len(sc.Enum) > 0 && v != nil {
		ok := false
		for _, e := range sc.Enum {
			if fmt.Sprint(e) == fmt.Sprint(v) {
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("%s: %v is not one of %v", path, v, sc.Enum)
		}
	}
	switch v := v.(type) {
	case map[string]any:
		for _, r := range sc.Required {
			if _, ok := v[r]; !ok {
				return fmt.Errorf("%s: missing required %q", path, r)
			}
		}
		for k, fv := range v {
			ps, known := sc.Properties[k]
			if !known {
				if sc.AdditionalProperties != nil && !*sc.AdditionalProperties {
					return fmt.Errorf("%s: unknown field %q", path, k)
				}
				continue
			}
			if err := ps.check(path+"."+k, fv); err != nil {
				return err
			}
		}
	case []any:
		if sc.MinItems != nil && len(v) < *sc.MinItems || sc.MaxItems != nil && len(v) > *sc.MaxItems {
			return fmt.Errorf("%s: %d items", path, len(v))
		}
		for i, e := range v {
			if err := sc.Items.check(fmt.Sprintf("%s[%d]", path, i), e); err != nil {
				return err
			}
		}
	case string:
		if !utf8.ValidString(v) {
			return fmt.Errorf("%s: not UTF-8", path)
		}
	}
	return nil
}

func (sc *schema) typeOK(v any) bool {
	var types []string
	switch t := sc.Type.(type) {
	case nil:
		return true
	case string:
		types = []string{t}
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				types = append(types, s)
			}
		}
	}
	k := kindOf(v)
	for _, t := range types {
		if t == k || t == "number" && k == "integer" {
			return true
		}
	}
	return false
}

func kindOf(v any) string {
	switch v := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		if _, err := v.Int64(); err == nil {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}
