package adapter

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Status is how the stream stands.
type Status int

const (
	// Connecting: the first adapter run has not sent its snapshot yet.
	Connecting Status = iota
	// Connected: the current run sent its snapshot; frames are live.
	Connected
	// Reconnecting: the last run ended or failed; another starts after a
	// backoff. The State folded so far is still the last thing known.
	Reconnecting
)

func (s Status) String() string {
	switch s {
	case Connecting:
		return "connecting"
	case Connected:
		return "connected"
	case Reconnecting:
		return "reconnecting"
	}
	return fmt.Sprintf("status(%d)", int(s))
}

// Update is what Stream.Run sends: a FrameUpdate or a StatusUpdate.
type Update interface{ isUpdate() }

// FrameUpdate is a frame read from the adapter, in stream order. Fold it
// into a State.
type FrameUpdate struct{ Frame Frame }

// StatusUpdate is a change of the stream's status. Err says why a run ended,
// and RetryIn how long until the next one starts; both are set only for
// Reconnecting. A run the adapter refused before connecting ends with an
// *EnvelopeError (its code, message, and next action); any other ends with
// the adapter's exit status and the tail of its stderr.
type StatusUpdate struct {
	Status  Status
	Err     error
	RetryIn time.Duration
}

func (FrameUpdate) isUpdate()  {}
func (StatusUpdate) isUpdate() {}

// Default backoff between adapter runs: doubling from MinBackoff to
// MaxBackoff, back to MinBackoff once a run reaches Connected.
const (
	DefaultMinBackoff = 250 * time.Millisecond
	DefaultMaxBackoff = 10 * time.Second
)

// StopGrace is how long a stopped adapter has to exit after SIGTERM (it
// exits 0 on it) before it is killed.
const StopGrace = 2 * time.Second

// Stream runs `<Bin> stream panel [--session Session]` and keeps it running.
type Stream struct {
	// Bin is the adapter binary (ResolveBin).
	Bin string
	// Session is the Rex session to watch; "" is the caller's pane's.
	Session string
	// Env is the adapter's environment; nil inherits this process's.
	Env []string
	// MinBackoff and MaxBackoff bound the wait between runs; zero values
	// take the defaults.
	MinBackoff, MaxBackoff time.Duration

	// start and after are replaced by tests.
	start func(ctx context.Context, bin string, args, env []string) (process, error)
	after func(time.Duration) <-chan time.Time
}

// process is one adapter run.
type process interface {
	// Stdout is the run's NDJSON output; it ends when the run does.
	Stdout() io.Reader
	// Stop asks the run to end (SIGTERM, then a kill after StopGrace).
	Stop()
	// Wait waits for the run to end and says how it ended: nil for exit 0,
	// else an error naming the status and the tail of stderr.
	Wait() error
}

// Args are the adapter's arguments.
func (s *Stream) Args() []string {
	args := []string{"stream", "panel"}
	if s.Session != "" {
		args = append(args, "--session", s.Session)
	}
	return args
}

// ErrStreamEnded is a run whose output ended while the adapter exited 0, as
// on a SIGTERM this process did not send.
var ErrStreamEnded = errors.New("adapter: panel stream ended")

// ErrGap is a frame out of order: not the next of its stream, or before the
// stream's snapshot.
var ErrGap = errors.New("adapter: frame out of order")

// Run keeps the adapter running and sends what it reads to out until ctx
// ends; then it stops the current run and returns nil. A run that exits,
// fails to start, writes a line that does not decode, or skips a frame is
// stopped and restarted after a backoff, and its replacement's snapshot
// resynchronizes the State. Run blocks on out.
func (s *Stream) Run(ctx context.Context, out chan<- Update) error {
	start, after := s.start, s.after
	if start == nil {
		start = startExec
	}
	if after == nil {
		after = time.After
	}
	minB, maxB := s.MinBackoff, s.MaxBackoff
	if minB <= 0 {
		minB = DefaultMinBackoff
	}
	if maxB < minB {
		maxB = max(DefaultMaxBackoff, minB)
	}
	send := func(u Update) bool {
		select {
		case out <- u:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !send(StatusUpdate{Status: Connecting}) {
		return nil
	}
	backoff := minB
	for {
		connected, err := s.runOnce(ctx, start, send)
		if ctx.Err() != nil {
			return nil
		}
		if connected {
			backoff = minB
		}
		if !send(StatusUpdate{Status: Reconnecting, Err: err, RetryIn: backoff}) {
			return nil
		}
		select {
		case <-after(backoff):
		case <-ctx.Done():
			return nil
		}
		backoff = min(backoff*2, maxB)
	}
}

// runOnce is one adapter run, from its start to its end: whether it reached
// Connected, and why it ended.
func (s *Stream) runOnce(ctx context.Context, start func(context.Context, string, []string, []string) (process, error), send func(Update) bool) (bool, error) {
	p, err := start(ctx, s.Bin, s.Args(), s.Env)
	if err != nil {
		return false, err
	}
	unhook := context.AfterFunc(ctx, p.Stop)
	defer unhook()
	connected, readErr := follow(p.Stdout(), send)
	if readErr != io.EOF {
		// The run is still writing, or this side cannot follow it.
		p.Stop()
	}
	waitErr := p.Wait()
	switch {
	case readErr != io.EOF:
		return connected, readErr
	case waitErr != nil:
		return connected, waitErr
	}
	return connected, ErrStreamEnded
}

// follow reads one run's frames and sends them on, until its output ends
// (io.EOF), a line does not decode, a frame is out of order, or send fails.
// It sends Connected before the run's snapshot. Before that snapshot, a
// failure envelope ends the run with its *EnvelopeError; after it, the same
// line is a frame that does not decode.
func follow(r io.Reader, send func(Update) bool) (connected bool, err error) {
	// Not bufio.Scanner: a snapshot carries a hand-back whole, and a line
	// has no bound.
	br := bufio.NewReader(r)
	var stream string
	var seq uint64
	for {
		line, readErr := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if stream == "" {
				if refusal, ok, err := decodeEnvelope(line); ok {
					if err != nil {
						return connected, err
					}
					return connected, refusal
				}
			}
			f, err := DecodeFrame(line)
			if err != nil {
				return connected, err
			}
			h := f.Head()
			_, isSnap := f.(Snapshot)
			switch {
			case stream == "" && !isSnap:
				return connected, fmt.Errorf("%w: the run began with a %s frame, not a snapshot", ErrGap, h.Type)
			case stream != "" && (h.Stream != stream || h.Seq != seq+1):
				return connected, fmt.Errorf("%w: frame %s/%d after %s/%d", ErrGap, h.Stream, h.Seq, stream, seq)
			}
			stream, seq = h.Stream, h.Seq
			if !connected {
				if !send(StatusUpdate{Status: Connected}) {
					return connected, context.Canceled
				}
				connected = true
			}
			if !send(FrameUpdate{Frame: f}) {
				return connected, context.Canceled
			}
		}
		if readErr != nil {
			return connected, readErr
		}
	}
}

// stderrTail keeps the last tailMax bytes written to it.
type stderrTail struct {
	mu  sync.Mutex
	buf []byte
}

const tailMax = 2048

func (t *stderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - tailMax; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

func (t *stderrTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// execProcess is a run of the adapter binary.
type execProcess struct {
	cmd    *exec.Cmd
	stdout io.Reader
	stderr *stderrTail
	stop   context.CancelFunc
}

func startExec(ctx context.Context, bin string, args, env []string) (process, error) {
	ctx, stop := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = StopGrace
	tail := &stderrTail{}
	cmd.Stderr = tail
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stop()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stop()
		return nil, fmt.Errorf("adapter: start %s: %w", bin, err)
	}
	return &execProcess{cmd: cmd, stdout: stdout, stderr: tail, stop: stop}, nil
}

func (p *execProcess) Stdout() io.Reader { return p.stdout }
func (p *execProcess) Stop()             { p.stop() }

func (p *execProcess) Wait() error {
	err := p.cmd.Wait()
	p.stop()
	if err == nil {
		return nil
	}
	if tail := p.stderr.String(); tail != "" {
		return fmt.Errorf("adapter: %w: %s", err, tail)
	}
	return fmt.Errorf("adapter: %w", err)
}
