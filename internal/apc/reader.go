package apc

import (
	"errors"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// EscWait is how long a lone ESC at the end of a read waits for the rest of
// a sequence before it counts as the Esc key: the same window terminal
// input decoders give an escape sequence (Bubble Tea's is 50 ms).
const EscWait = 40 * time.Millisecond

// Reader is the terminal's input with this channel's sequences taken out.
// It passes for the terminal itself (Fd, Name, Write, Close), so Bubble Tea
// still puts the terminal in raw mode and restores it, and waits on its
// descriptor before reading.
//
// Only one split can leak: a sequence whose first byte, ESC, arrives alone
// and more than EscWait before the rest. Rex writes each sequence in one
// write, so in practice it arrives whole.
type Reader struct {
	f *os.File
	// On receives each command, on the reading goroutine. Set it before the
	// first Read.
	On func(Command)
	// Wait reports whether f has more to read within d; nil polls f's
	// descriptor.
	Wait func(d time.Duration) bool

	p    Parser
	buf  [4096]byte
	left []byte // filtered bytes a short Read could not take
}

// NewReader filters f, usually os.Stdin.
func NewReader(f *os.File) *Reader { return &Reader{f: f} }

func (r *Reader) Fd() uintptr                 { return r.f.Fd() }
func (r *Reader) Name() string                { return r.f.Name() }
func (r *Reader) Write(b []byte) (int, error) { return r.f.Write(b) }
func (r *Reader) Close() error                { return r.f.Close() }

// Read reads input with this channel's sequences taken out. It may return
// no bytes and no error when a read held only a sequence, or part of one.
func (r *Reader) Read(b []byte) (int, error) {
	if len(r.left) > 0 {
		n := copy(b, r.left)
		r.left = r.left[n:]
		return n, nil
	}
	var out []byte
	for {
		n, err := r.f.Read(r.buf[:])
		pass, cmds := r.p.Feed(r.buf[:n])
		out = append(out, pass...)
		for _, c := range cmds {
			if r.On != nil {
				r.On(c)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
				out = append(out, r.p.Flush()...)
			}
			return r.give(b, out), err
		}
		if !r.p.HoldsEsc() {
			return r.give(b, out), nil
		}
		if !r.wait(EscWait) {
			return r.give(b, append(out, r.p.Flush()...)), nil
		}
		// More follows the ESC at once: read it before deciding.
	}
}

func (r *Reader) give(b, out []byte) int {
	n := copy(b, out)
	r.left = append(r.left[:0], out[n:]...)
	return n
}

func (r *Reader) wait(d time.Duration) bool {
	if r.Wait != nil {
		return r.Wait(d)
	}
	fds := []unix.PollFd{{Fd: int32(r.f.Fd()), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, int(d.Milliseconds()))
		if err == unix.EINTR {
			continue
		}
		return err == nil && n > 0
	}
}
