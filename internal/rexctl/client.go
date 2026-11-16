// Package rexctl is a client of Rex's control API (rex.control.v1): a
// WebSocket on the server's Unix socket carrying JSON requests, responses,
// and events. It talks to the server directly instead of starting the rex
// CLI for every call.
//
// The wire shapes follow `rex api describe` on Rex 0.1.0-preview.18 (api
// revision 13; the method schemas are kept in rexctltest/schemas) and the
// handshake of rexmod's client (internal/editor/rex/client.go).
package rexctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const (
	// Protocol is the WebSocket subprotocol of the control API.
	Protocol = "rex.control.v1"
	// APIRevision is the revision of the API this client was written
	// against, sent in hello.
	APIRevision = 13
	// ClientName is how this client names itself to Rex.
	ClientName = "rex-amq-ui"
)

// writeTimeout bounds sending one request; a socket that cannot take a
// request in this long is broken, and the connection ends.
const writeTimeout = 10 * time.Second

var (
	ErrNotUnixURL    = errors.New("rexctl: server address is not a unix:// URL")
	ErrNameTooLong   = errors.New("rexctl: socket path too long")
	ErrNotWelcome    = errors.New("rexctl: server did not welcome the connection")
	ErrClosed        = errors.New("rexctl: connection closed")
	ErrRequestTooBig = errors.New("rexctl: request larger than the server accepts")
)

// Error codes Rex answers with.
const (
	CodeBadRequest        = "bad_request"
	CodeConflict          = "conflict"
	CodeNotFound          = "not_found"
	CodeSessionNotFound   = "session_not_found"
	CodeMethodNotFound    = "method_not_found"
	CodeRefused           = "refused"
	CodeNotSupported      = "not_supported"
	CodeClientUnavailable = "client_unavailable"
	CodeTimeout           = "timeout"
)

// Error is a request Rex answered with an error.
type Error struct {
	Method  string
	Code    string
	Message string
	Status  int
}

func (e *Error) Error() string {
	return fmt.Sprintf("rexctl: %s: %s: %s", e.Method, e.Code, e.Message)
}

// IsCode reports whether err is a Rex error with code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Welcome is what the server said when it accepted the connection.
type Welcome struct {
	ClientID        string
	APIRevision     int64
	MaxMessageBytes int
}

// Client is one control connection. It is safe for concurrent use.
type Client struct {
	ws      *websocket.Conn
	welcome Welcome
	stop    context.CancelFunc // ends the read loop
	done    chan struct{}
	events  chan Event
	writes  chan writeJob // to the writer loop, which alone writes requests
	writing atomic.Bool   // a request is being written to the socket

	mu      sync.Mutex
	nextID  uint64
	pending map[string]chan response
	queue   []Event // events read and not yet taken from the channel
	wake    chan struct{}
	err     error // why the connection ended; nil while open
}

// writeJob is a request handed to the writer loop. done receives the
// write's outcome, or the job's context error when it was skipped.
type writeJob struct {
	ctx  context.Context
	msg  []byte
	done chan error
}

type response struct {
	seq     int64
	payload json.RawMessage
	err     *Error
}

// Dial connects to the server at serverURL (REX_SERVER, a unix:// URL),
// exchanges hello and welcome, and starts reading.
func Dial(ctx context.Context, serverURL string) (*Client, error) {
	path, err := SocketPath(serverURL)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}}}
	// Any host works on the Unix socket.
	ws, _, err := websocket.Dial(ctx, "ws://localhost/api/control", &websocket.DialOptions{HTTPClient: hc, Subprotocols: []string{Protocol}})
	if err != nil {
		return nil, fmt.Errorf("rexctl: connect to %s: %w", path, err)
	}
	if ws.Subprotocol() != Protocol {
		ws.CloseNow()
		return nil, fmt.Errorf("%w: subprotocol %q", ErrNotWelcome, ws.Subprotocol())
	}
	// The welcome says the real limit; until then accept what a view of a
	// large session needs.
	ws.SetReadLimit(64 << 20)
	w, err := handshake(ctx, ws)
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	ws.SetReadLimit(int64(w.MaxMessageBytes))
	rctx, stop := context.WithCancel(context.Background())
	c := &Client{ws: ws, welcome: w, stop: stop, done: make(chan struct{}), events: make(chan Event), writes: make(chan writeJob),
		nextID: 1, pending: map[string]chan response{}, wake: make(chan struct{}, 1)}
	go c.read(rctx)
	go c.pump()
	go c.writer()
	return c, nil
}

func handshake(ctx context.Context, ws *websocket.Conn) (Welcome, error) {
	hello, err := json.Marshal(map[string]any{"type": "hello", "api_revision": APIRevision, "client": map[string]any{"kind": "other", "name": ClientName}})
	if err != nil {
		return Welcome{}, err
	}
	if err := ws.Write(ctx, websocket.MessageText, hello); err != nil {
		return Welcome{}, fmt.Errorf("rexctl: send hello: %w", err)
	}
	_, b, err := ws.Read(ctx)
	if err != nil {
		return Welcome{}, fmt.Errorf("rexctl: read welcome: %w", err)
	}
	var m struct {
		Type        string `json:"type"`
		Protocol    string `json:"protocol"`
		APIRevision int64  `json:"api_revision"`
		Limits      struct {
			MaxMessageBytes int `json:"max_message_bytes"`
		} `json:"limits"`
		Client struct {
			ID string `json:"id"`
		} `json:"client"`
	}
	if err := json.Unmarshal(b, &m); err != nil || m.Type != "welcome" || m.Protocol != Protocol {
		return Welcome{}, fmt.Errorf("%w: %.200s", ErrNotWelcome, b)
	}
	// RFC-level floor: the server accepts at least 64 KiB.
	if m.Limits.MaxMessageBytes < 65536 {
		return Welcome{}, fmt.Errorf("%w: max_message_bytes %d", ErrNotWelcome, m.Limits.MaxMessageBytes)
	}
	return Welcome{ClientID: m.Client.ID, APIRevision: m.APIRevision, MaxMessageBytes: m.Limits.MaxMessageBytes}, nil
}

// SocketPath is the percent-decoded path of a unix:// URL.
func SocketPath(serverURL string) (string, error) {
	const scheme = "unix://"
	if !strings.HasPrefix(serverURL, scheme) {
		return "", ErrNotUnixURL
	}
	p, err := url.PathUnescape(serverURL[len(scheme):])
	if err != nil {
		return "", err
	}
	// sockaddr_un.sun_path is 104 bytes on macOS, with its terminator.
	if len(p) > 103 {
		return "", ErrNameTooLong
	}
	return p, nil
}

// Welcome is what the server said when it accepted the connection.
func (c *Client) Welcome() Welcome { return c.welcome }

// Events are the server's and attached sessions' events, in order. A
// consumer that falls behind does not stall responses: events queue without
// bound until read. The channel closes when the connection ends.
func (c *Client) Events() <-chan Event { return c.events }

// Done is closed when the connection ends; Err then says why.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err is why the connection ended, or nil while it is open.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close ends the connection. Calls in flight fail with ErrClosed.
func (c *Client) Close() error {
	err := c.ws.Close(websocket.StatusNormalClosure, "")
	c.stop()
	<-c.done
	if errors.Is(err, net.ErrClosed) {
		err = nil
	}
	return err
}

// read dispatches what the server sends until the connection ends, then
// fails every call still waiting.
func (c *Client) read(ctx context.Context) {
	var cause error
	for {
		_, b, err := c.ws.Read(ctx)
		if err != nil {
			cause = err
			break
		}
		if err := c.dispatch(b); err != nil {
			cause = err
			c.ws.CloseNow()
			break
		}
	}
	c.mu.Lock()
	c.err = fmt.Errorf("%w: %v", ErrClosed, cause)
	for id, ch := range c.pending {
		ch <- response{err: nil, seq: -1}
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.done)
	c.signal()
}

// dispatch hands a response to its call, or queues an event. A message
// that is not JSON ends the connection; one of a type this client does not
// know is skipped.
func (c *Client) dispatch(b []byte) error {
	var m struct {
		Type    string          `json:"type"`
		ID      string          `json:"id"`
		Seq     int64           `json:"seq"`
		Payload json.RawMessage `json:"payload"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Status  int    `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("rexctl: bad message from the server: %w", err)
	}
	switch m.Type {
	case "response":
		r := response{seq: m.Seq, payload: m.Payload}
		if m.Error != nil {
			r.err = &Error{Code: m.Error.Code, Message: m.Error.Message, Status: m.Error.Status}
		}
		c.mu.Lock()
		ch, ok := c.pending[m.ID]
		delete(c.pending, m.ID)
		c.mu.Unlock()
		if ok {
			ch <- r
		}
	case "event":
		e, err := parseEvent(m.Seq, m.Payload)
		if err != nil {
			return err
		}
		c.mu.Lock()
		c.queue = append(c.queue, e)
		c.mu.Unlock()
		c.signal()
	}
	return nil
}

func parseEvent(seq int64, payload json.RawMessage) (Event, error) {
	var outer struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(payload, &outer); err != nil || outer.Type == "" {
		return Event{}, fmt.Errorf("rexctl: bad event: %.200s", payload)
	}
	if outer.Type != "session_event" {
		return Event{Seq: seq, Name: outer.Type, Payload: outer.Payload}, nil
	}
	var inner struct {
		SessionID string `json:"session_id"`
		Event     struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		} `json:"event"`
	}
	if err := json.Unmarshal(outer.Payload, &inner); err != nil || inner.Event.Type == "" {
		return Event{}, fmt.Errorf("rexctl: bad session event: %.200s", payload)
	}
	return Event{Seq: seq, SessionID: inner.SessionID, Name: inner.Event.Type, Payload: inner.Event.Payload}, nil
}

func (c *Client) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// pump moves queued events to the events channel, so a slow reader of
// events never blocks the read loop (and with it every response). Once the
// connection ends it closes the channel; events not taken by then are
// dropped, since a new connection has to attach and read a fresh view.
func (c *Client) pump() {
	defer close(c.events)
	for {
		c.mu.Lock()
		var e Event
		have := len(c.queue) > 0
		if have {
			e, c.queue = c.queue[0], c.queue[1:]
		}
		c.mu.Unlock()
		if !have {
			select {
			case <-c.wake:
				continue
			case <-c.done:
				return
			}
		}
		select {
		case c.events <- e:
		case <-c.done:
			return
		}
	}
}

// Call sends a request for a method this package does not wrap and waits
// for its answer, decoding its payload into result when not nil.
func (c *Client) Call(ctx context.Context, method string, payload, result any) error {
	_, err := c.call(ctx, method, payload, result)
	return err
}

// call sends a request and waits for its response, decoding its payload
// into result when not nil. It returns the response's seq.
func (c *Client) call(ctx context.Context, method string, payload, result any) (int64, error) {
	ch := make(chan response, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return 0, err
	}
	id := strconv.FormatUint(c.nextID, 10)
	c.nextID++
	c.mu.Unlock()

	b, err := encodeRequest(id, method, payload)
	if err != nil {
		return 0, err
	}
	// The server closes the connection on a request over its limit.
	if len(b) > c.welcome.MaxMessageBytes {
		return 0, fmt.Errorf("%w: %s is %d bytes, the limit %d", ErrRequestTooBig, method, len(b), c.welcome.MaxMessageBytes)
	}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return 0, err
	}
	c.pending[id] = ch
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}
	if err := c.send(ctx, method, b); err != nil {
		forget()
		return 0, err
	}
	select {
	case r := <-ch:
		if r.seq < 0 {
			return 0, c.Err()
		}
		if r.err != nil {
			r.err.Method = method
			return r.seq, r.err
		}
		if result != nil && len(r.payload) > 0 {
			if err := json.Unmarshal(r.payload, result); err != nil {
				return r.seq, fmt.Errorf("rexctl: decode %s result: %w", method, err)
			}
		}
		return r.seq, nil
	case <-ctx.Done():
		forget()
		return 0, ctx.Err()
	}
}

// send hands a request to the writer loop and waits until it is written,
// or until ctx ends or the connection does. A request whose ctx ends before
// the writer takes it up is never sent; one already being written finishes
// under writeTimeout while the caller returns.
func (c *Client) send(ctx context.Context, method string, msg []byte) error {
	job := writeJob{ctx: ctx, msg: msg, done: make(chan error, 1)}
	select {
	case c.writes <- job:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.Err()
	}
	select {
	case err := <-job.done:
		switch {
		case err == nil:
			return nil
		case ctx.Err() != nil:
			return ctx.Err()
		}
		return fmt.Errorf("%w: send %s: %v", ErrClosed, method, err)
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.Err()
	}
}

// writer writes requests one at a time until the connection ends. Each
// write gets its own timeout, not its caller's context: websocket closes
// the whole connection when a write's context ends, and one cancelled call
// must not end the rest.
func (c *Client) writer() {
	for {
		var job writeJob
		select {
		case job = <-c.writes:
		case <-c.done:
			return
		}
		if err := job.ctx.Err(); err != nil {
			job.done <- err
			continue
		}
		wctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		c.writing.Store(true)
		err := c.ws.Write(wctx, websocket.MessageText, job.msg)
		c.writing.Store(false)
		cancel()
		job.done <- err
	}
}

func encodeRequest(id, method string, payload any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	req := map[string]any{"type": "request", "id": id, "method": method}
	if payload != nil {
		req["payload"] = payload
	}
	if err := e.Encode(req); err != nil {
		return nil, fmt.Errorf("rexctl: encode %s: %w", method, err)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
