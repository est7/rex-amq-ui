package adapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// wait bounds every channel wait in these tests; none sleeps.
const wait = 5 * time.Second

// fakeProc is an adapter run the test drives: it writes lines to the
// run's stdout and decides how the run exits.
type fakeProc struct {
	args, env []string
	r         *io.PipeReader
	w         *io.PipeWriter
	exitCh    chan error
	stopOnce  sync.Once
	stopped   chan struct{}
}

func (p *fakeProc) Stdout() io.Reader { return p.r }

func (p *fakeProc) Stop() {
	p.stopOnce.Do(func() {
		close(p.stopped)
		p.w.Close()
	})
}

func (p *fakeProc) Wait() error {
	select {
	case err := <-p.exitCh:
		return err
	case <-p.stopped:
		return nil
	}
}

func (p *fakeProc) write(t *testing.T, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if _, err := io.WriteString(p.w, l+"\n"); err != nil {
			t.Fatalf("write to the run: %v", err)
		}
	}
}

// exit ends the run's output, then the run, with err.
func (p *fakeProc) exit(err error) {
	p.w.Close()
	p.exitCh <- err
}

type fakeTimer struct {
	d  time.Duration
	ch chan time.Time
}

type harness struct {
	t      *testing.T
	s      *Stream
	procs  chan *fakeProc
	timers chan fakeTimer
	out    chan Update
	done   chan error
	cancel context.CancelFunc
}

func newHarness(t *testing.T, s *Stream) *harness {
	h := &harness{t: t, s: s, procs: make(chan *fakeProc, 8), timers: make(chan fakeTimer, 8), out: make(chan Update, 64), done: make(chan error, 1)}
	s.start = func(ctx context.Context, bin string, args, env []string) (process, error) {
		r, w := io.Pipe()
		p := &fakeProc{args: append([]string{bin}, args...), env: env, r: r, w: w, exitCh: make(chan error, 1), stopped: make(chan struct{})}
		h.procs <- p
		return p, nil
	}
	s.after = func(d time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		h.timers <- fakeTimer{d, ch}
		return ch
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- s.Run(ctx, h.out) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(wait):
			t.Error("Run did not return after cancel")
		}
	})
	return h
}

func (h *harness) proc() *fakeProc {
	h.t.Helper()
	select {
	case p := <-h.procs:
		return p
	case <-time.After(wait):
		h.t.Fatal("no adapter run started")
	}
	return nil
}

func (h *harness) noProc() {
	h.t.Helper()
	select {
	case p := <-h.procs:
		h.t.Fatalf("a run started too early: %v", p.args)
	default:
	}
}

func (h *harness) timer() fakeTimer {
	h.t.Helper()
	select {
	case tm := <-h.timers:
		return tm
	case <-time.After(wait):
		h.t.Fatal("no backoff started")
	}
	return fakeTimer{}
}

func (h *harness) next() Update {
	h.t.Helper()
	select {
	case u := <-h.out:
		return u
	case <-time.After(wait):
		h.t.Fatal("no update")
	}
	return nil
}

func (h *harness) status(want Status) StatusUpdate {
	h.t.Helper()
	u, ok := h.next().(StatusUpdate)
	if !ok || u.Status != want {
		h.t.Fatalf("got %#v, want status %v", u, want)
	}
	return u
}

func (h *harness) frame() Frame {
	h.t.Helper()
	u := h.next()
	f, ok := u.(FrameUpdate)
	if !ok {
		h.t.Fatalf("got %#v, want a frame", u)
	}
	return f.Frame
}

func frameLine(stream string, seq uint64, typ, extra string) string {
	return fmt.Sprintf(`{"schema":1,"stream":%q,"seq":%d,"type":%q,"at":"2026-11-16T09:00:00Z"%s}`, stream, seq, typ, extra)
}

func snapshotLine(stream, label string) string {
	return frameLine(stream, 1, "snapshot", fmt.Sprintf(`,"scope":{"kind":"session","server":"s","session_id":"session:x"},"agents":[],"messages":[],"panel":{"session_label":%q,"attention":{}}`, label))
}

func TestStreamConnectsOnSnapshotAndForwardsFramesInOrder(t *testing.T) {
	h := newHarness(t, &Stream{Bin: "/x/rex-amq-adapter", Session: "session:x", Env: []string{"PATH=/fake"}})
	h.status(Connecting)
	p := h.proc()
	if want := []string{"/x/rex-amq-adapter", "stream", "panel", "--session", "session:x"}; !slices.Equal(p.args, want) || !slices.Equal(p.env, []string{"PATH=/fake"}) {
		t.Fatalf("run %v env %v", p.args, p.env)
	}
	p.write(t, snapshotLine("s1", "epic"))
	h.status(Connected)
	if f, ok := h.frame().(Snapshot); !ok || f.Panel.SessionLabel != "epic" {
		t.Fatalf("first frame %#v", f)
	}
	p.write(t, frameLine("s1", 2, "heartbeat", ""), frameLine("s1", 3, "telemetry", `,"x":1`))
	if f, ok := h.frame().(Heartbeat); !ok || f.Seq != 2 {
		t.Fatalf("second frame %#v", f)
	}
	if f, ok := h.frame().(Unknown); !ok || f.Seq != 3 {
		t.Fatalf("third frame %#v", f)
	}
	h.noProc()
}

// A run that ends is restarted after a backoff that doubles while runs keep
// failing before their snapshot, is capped, and starts over once a run
// connects. Every Reconnecting says why.
func TestStreamReconnectsWithBackoff(t *testing.T) {
	h := newHarness(t, &Stream{Bin: "a", MinBackoff: 100 * time.Millisecond, MaxBackoff: 300 * time.Millisecond})
	h.status(Connecting)
	p := h.proc()
	p.write(t, snapshotLine("s1", "one"))
	h.status(Connected)
	h.frame()
	p.exit(errors.New("exit status 1: refused: the panel is the human's"))

	for i, want := range []time.Duration{100, 200, 300, 300} {
		u := h.status(Reconnecting)
		if u.Err == nil || !strings.Contains(u.Err.Error(), "the panel is the human's") || u.RetryIn != want*time.Millisecond {
			t.Fatalf("failure %d: %#v", i, u)
		}
		tm := h.timer()
		if tm.d != want*time.Millisecond {
			t.Fatalf("failure %d: backoff %v", i, tm.d)
		}
		h.noProc()
		tm.ch <- time.Now()
		p = h.proc()
		if i < 3 {
			p.exit(errors.New("exit status 1: refused: the panel is the human's"))
		}
	}
	p.write(t, snapshotLine("s2", "two"))
	h.status(Connected)
	if f := h.frame().(Snapshot); f.Stream != "s2" {
		t.Fatalf("snapshot of %s", f.Stream)
	}
	p.exit(nil)
	if u := h.status(Reconnecting); !errors.Is(u.Err, ErrStreamEnded) || u.RetryIn != 100*time.Millisecond {
		t.Fatalf("after a clean exit: %#v", u)
	}
}

// A skipped frame, a frame of another stream, a first frame that is not a
// snapshot, or a line that does not decode stops the run: the State could
// no longer follow it, and the next run's snapshot resynchronizes it.
func TestStreamRestartsARunItCannotFollow(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  error
	}{
		{"gap", []string{snapshotLine("s1", "x"), frameLine("s1", 2, "heartbeat", ""), frameLine("s1", 4, "heartbeat", "")}, ErrGap},
		{"other stream", []string{snapshotLine("s1", "x"), frameLine("s9", 2, "heartbeat", "")}, ErrGap},
		{"no snapshot first", []string{frameLine("s1", 1, "heartbeat", "")}, ErrGap},
		{"malformed", []string{snapshotLine("s1", "x"), `{"schema":1,"stream":"s1","seq":2,"type":"heartbeat","at":"not a time"}`}, nil},
		{"other schema", []string{snapshotLine("s1", "x"), `{"schema":2,"stream":"s1","seq":2,"type":"heartbeat","at":"2026-11-16T09:00:00Z"}`}, ErrSchema},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, &Stream{Bin: "a"})
			h.status(Connecting)
			p := h.proc()
			go func() {
				for _, l := range c.lines {
					if _, err := io.WriteString(p.w, l+"\n"); err != nil {
						return
					}
				}
			}()
			var u StatusUpdate
			for {
				next := h.next()
				if s, ok := next.(StatusUpdate); ok && s.Status == Reconnecting {
					u = s
					break
				}
			}
			select {
			case <-p.stopped:
			case <-time.After(wait):
				t.Fatal("the run was not stopped")
			}
			if u.Err == nil || (c.want != nil && !errors.Is(u.Err, c.want)) {
				t.Fatalf("reconnecting because %v, want %v", u.Err, c.want)
			}
		})
	}
}

// The adapter sends a hand-back whole, so a line can be far longer than
// bufio.Scanner's default 64 KiB.
func TestStreamReadsLongLines(t *testing.T) {
	h := newHarness(t, &Stream{Bin: "a"})
	h.status(Connecting)
	p := h.proc()
	body := strings.Repeat("x", 300<<10)
	line := frameLine("s1", 1, "snapshot", fmt.Sprintf(`,"scope":{"kind":"session","server":"s"},"agents":[],"messages":[],"panel":{"session_label":"l","attention":{},"view":{"schema":1,"hand_back":{"by":"a","at":"2026-11-16T09:00:00Z","body":%q}}}`, body))
	go io.WriteString(p.w, line+"\n")
	h.status(Connected)
	if f := h.frame().(Snapshot); len(f.Panel.View.HandBack.Body) != len(body) {
		t.Fatalf("hand-back of %d bytes", len(f.Panel.View.HandBack.Body))
	}
}

func TestStreamStopsTheRunWhenCancelled(t *testing.T) {
	h := newHarness(t, &Stream{Bin: "a"})
	h.status(Connecting)
	p := h.proc()
	p.write(t, snapshotLine("s1", "x"))
	h.status(Connected)
	h.frame()
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
		h.done <- nil // for the cleanup
	case <-time.After(wait):
		t.Fatal("Run did not return")
	}
	select {
	case <-p.stopped:
	default:
		t.Fatal("the run was left running")
	}
	h.noProc()
}

func TestStreamCancelledDuringBackoffReturns(t *testing.T) {
	h := newHarness(t, &Stream{Bin: "a"})
	h.status(Connecting)
	h.proc().exit(errors.New("exit status 2"))
	h.status(Reconnecting)
	h.timer()
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
		h.done <- nil
	case <-time.After(wait):
		t.Fatal("Run did not return from its backoff")
	}
	h.noProc()
}

// The adapter's v2 command for the human's panel, with and without a
// session.
func TestStreamPanelArgv(t *testing.T) {
	if got := (&Stream{}).Args(); !slices.Equal(got, []string{"stream", "panel"}) {
		t.Fatal(got)
	}
	if got := (&Stream{Session: "session:x"}).Args(); !slices.Equal(got, []string{"stream", "panel", "--session", "session:x"}) {
		t.Fatal(got)
	}
}

// envelopeAdapter writes an adapter that prints the recorded envelope
// fixture (testdata/envelope, from rex-amq-adapter stream panel) and exits
// with code, as the v2 adapter does when it refuses a run before connecting.
func envelopeAdapter(t *testing.T, fixture string, code int) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "testdata", "envelope", fixture))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "rex-amq-adapter")
	body := fmt.Sprintf("#!/bin/sh\ncat '%s'\nexit %d\n", abs, code)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// firstEnd runs s until its first run ends and returns that Reconnecting or
// Refused.
func firstEnd(t *testing.T, s *Stream) StatusUpdate {
	t.Helper()
	out := make(chan Update, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, out) }()
	defer func() { cancel(); <-done }()
	for {
		select {
		case u := <-out:
			switch u := u.(type) {
			case StatusUpdate:
				if u.Status == Reconnecting || u.Status == Refused {
					return u
				}
				if u.Status == Connected {
					t.Fatal("a refused run connected")
				}
			case FrameUpdate:
				t.Fatalf("a refused run sent a frame: %#v", u.Frame)
			}
		case <-time.After(wait):
			t.Fatal("the run did not end")
		}
	}
}

// A run the adapter's guard refuses before connecting (exit 6, recorded
// from an agent's pane) ends with the envelope's code, message, and next
// action, not as a frame of an unsupported schema.
func TestPreConnectEnvelope(t *testing.T) {
	u := firstEnd(t, &Stream{Bin: envelopeAdapter(t, "recorded-guard-denied.ndjson", 6), Env: []string{"PATH=/usr/bin:/bin"}})
	if u.Status != Refused {
		t.Fatalf("status %v, want refused", u.Status)
	}
	var refusal *EnvelopeError
	if !errors.As(u.Err, &refusal) {
		t.Fatalf("ended with %v, want an *EnvelopeError", u.Err)
	}
	if refusal.Code != "guard_denied" || !strings.Contains(refusal.Message, "this command is the human's") || refusal.NextAction != "" || refusal.Retryable {
		t.Fatalf("refusal %+v", refusal)
	}
	if errors.Is(u.Err, ErrSchema) {
		t.Fatalf("the refusal was read as a frame: %v", u.Err)
	}
	if msg := u.Err.Error(); !strings.Contains(msg, "guard_denied") || !strings.Contains(msg, "this command is the human's") {
		t.Fatalf("status text %q", msg)
	}
}

// A usage error before connecting (exit 2) carries its next action, the
// command the adapter suggests, into the status.
func TestPreConnectUsage(t *testing.T) {
	u := firstEnd(t, &Stream{Bin: envelopeAdapter(t, "recorded-usage.ndjson", 2), Env: []string{"PATH=/usr/bin:/bin"}})
	if u.Status != Refused {
		t.Fatalf("status %v, want refused", u.Status)
	}
	var refusal *EnvelopeError
	if !errors.As(u.Err, &refusal) {
		t.Fatalf("ended with %v, want an *EnvelopeError", u.Err)
	}
	const next = "rex-amq-adapter stream panel --help"
	if refusal.Code != "usage" || refusal.Message != "unknown flag --bogus" || refusal.NextAction != next {
		t.Fatalf("refusal %+v", refusal)
	}
	if msg := u.Err.Error(); !strings.Contains(msg, "usage: unknown flag --bogus") || !strings.Contains(msg, next) {
		t.Fatalf("status text %q", msg)
	}
}

// A failure envelope with no error code is not taken for a refusal it
// cannot name.
func TestPreConnectMalformedEnvelope(t *testing.T) {
	h := newHarness(t, &Stream{Bin: "a"})
	h.status(Connecting)
	p := h.proc()
	go io.WriteString(p.w, `{"ok":false,"v":2,"data":null}`+"\n")
	u := h.status(Reconnecting)
	var refusal *EnvelopeError
	if !errors.Is(u.Err, errEnvelopeMalformed) || errors.As(u.Err, &refusal) {
		t.Fatalf("ended with %v", u.Err)
	}
}

// A run that connected and then lost its adapter ends with the exit status,
// and the next run's snapshot connects again.
func TestStreamDisconnectAfterConnecting(t *testing.T) {
	h := newHarness(t, &Stream{Bin: "a"})
	h.status(Connecting)
	p := h.proc()
	p.write(t, snapshotLine("s1", "x"))
	h.status(Connected)
	h.frame()
	p.exit(errors.New("exit status 1: rex: connection refused"))
	u := h.status(Reconnecting)
	var refusal *EnvelopeError
	if u.Err == nil || errors.As(u.Err, &refusal) || !strings.Contains(u.Err.Error(), "connection refused") {
		t.Fatalf("ended with %v", u.Err)
	}
	h.timer().ch <- time.Now()
	p = h.proc()
	p.write(t, snapshotLine("s2", "x"))
	h.status(Connected)
}

// After the snapshot, the stream is frames only: an envelope-shaped line is
// a frame that does not decode (schema 0), and the run is restarted as for
// any malformed frame.
func TestStreamEnvelopeAfterConnectingIsAMalformedFrame(t *testing.T) {
	h := newHarness(t, &Stream{Bin: "a"})
	h.status(Connecting)
	p := h.proc()
	p.write(t, snapshotLine("s1", "x"))
	h.status(Connected)
	h.frame()
	go io.WriteString(p.w, `{"ok":false,"v":2,"data":null,"error":{"code":"internal","message":"x"}}`+"\n")
	u := h.status(Reconnecting)
	var refusal *EnvelopeError
	if !errors.Is(u.Err, ErrSchema) || errors.As(u.Err, &refusal) {
		t.Fatalf("ended with %v", u.Err)
	}
}

// The real process runner: a script stands in for the adapter. A run that
// prints usage and exits 2 (an adapter without panel) reports both; a run
// that streams is stopped with SIGTERM when Run's context ends.
func TestStreamRunsARealProcess(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join("..", "..", "testdata", "panel", "recorded-bare.ndjson")
	abs, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "rex-amq-adapter")
	body := "#!/bin/sh\n" +
		"[ \"$1 $2\" = \"stream panel\" ] || { echo \"usage: rex-amq-adapter <command>\" >&2; exit 2; }\n" +
		"head -n 1 '" + abs + "'\n" +
		"trap 'exit 0' TERM\n" +
		"while :; do sleep 0.1; done\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	out := make(chan Update, 16)
	ctx, cancel := context.WithCancel(context.Background())
	s := &Stream{Bin: script, Env: []string{"PATH=/usr/bin:/bin"}}
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, out) }()
	expect := func(want Status) {
		t.Helper()
		for {
			select {
			case u := <-out:
				if su, ok := u.(StatusUpdate); ok {
					if su.Status != want {
						t.Fatalf("status %v (%v), want %v", su.Status, su.Err, want)
					}
					return
				}
			case <-time.After(wait):
				t.Fatalf("no %v", want)
			}
		}
	}
	expect(Connecting)
	expect(Connected)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(wait):
		t.Fatal("Run did not stop the script")
	}

	// A binary without panel: the run's arguments are replaced so the
	// script takes the usage branch, as an adapter that predates panel does.
	out = make(chan Update, 16)
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	old := &Stream{Bin: script, Env: []string{"PATH=/usr/bin:/bin"}, start: func(ctx context.Context, bin string, _, env []string) (process, error) {
		return startExec(ctx, bin, []string{"panel"}, env)
	}}
	go func() { done <- old.Run(ctx, out) }()
	expect(Connecting)
	u := (<-out).(StatusUpdate)
	if u.Status != Reconnecting || u.Err == nil || !strings.Contains(u.Err.Error(), "exit status 2") || !strings.Contains(u.Err.Error(), "usage: rex-amq-adapter") {
		t.Fatalf("%#v", u)
	}
	cancel()
	<-done
}

// A refusal the adapter marks retryable:false (both recorded fixtures) is
// final: Run reports it as Refused and returns, and starts no other run with
// the same arguments (spec §3.4: a usage error is not retried unchanged).
func TestPreConnectRefusalIsNotRetried(t *testing.T) {
	for _, fixture := range []string{"recorded-guard-denied.ndjson", "recorded-usage.ndjson"} {
		t.Run(fixture, func(t *testing.T) {
			line, err := os.ReadFile(filepath.Join("..", "..", "testdata", "envelope", fixture))
			if err != nil {
				t.Fatal(err)
			}
			h := newHarness(t, &Stream{Bin: "a"})
			h.status(Connecting)
			p := h.proc()
			go func() {
				io.WriteString(p.w, string(line))
				p.exit(errors.New("exit status 6"))
			}()
			u := h.status(Refused)
			var refusal *EnvelopeError
			if !errors.As(u.Err, &refusal) || refusal.Retryable || u.RetryIn != 0 {
				t.Fatalf("refused with %#v", u)
			}
			select {
			case err := <-h.done:
				if err != nil {
					t.Fatalf("Run returned %v", err)
				}
				h.done <- nil // for the cleanup
			case <-time.After(wait):
				t.Fatal("Run kept going after a final refusal")
			}
			select {
			case tm := <-h.timers:
				t.Fatalf("a backoff of %v started", tm.d)
			default:
			}
			h.noProc()
		})
	}
}

// A refusal marked retryable:true is retried after a backoff, like any run
// that ended.
func TestPreConnectRetryableRefusalIsRetried(t *testing.T) {
	h := newHarness(t, &Stream{Bin: "a"})
	h.status(Connecting)
	p := h.proc()
	go func() {
		io.WriteString(p.w, `{"ok":false,"v":2,"data":null,"error":{"code":"rex_unavailable","message":"rex is not running","retryable":true,"next_action":null}}`+"\n")
		p.exit(errors.New("exit status 1"))
	}()
	u := h.status(Reconnecting)
	var refusal *EnvelopeError
	if !errors.As(u.Err, &refusal) || refusal.Code != "rex_unavailable" || !refusal.Retryable || u.RetryIn == 0 {
		t.Fatalf("reconnecting with %#v", u)
	}
	h.timer().ch <- time.Now()
	h.proc()
}
