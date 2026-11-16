// Package kitty shows the Graph tab's frames through the Kitty graphics
// protocol and paces them.
//
// Transport hands each frame to the terminal as a temporary file (t=t),
// alternating two image ids so the new frame is up before the old one is
// deleted: no blank frame between them. It writes its commands to the
// writer it is given and changes what it believes is on screen only once
// the write succeeded, so the caller decides the order of everything that
// reaches the terminal.
package kitty

import (
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// DefaultDir is where frame files go. Kitty-protocol terminals read t=t
// files only from a temporary directory and only when the name contains
// "tty-graphics-protocol"; they delete each file once read.
const DefaultDir = "/tmp"

// MaxUnread is how many frame files may wait for the terminal to read
// them; a terminal that stops reading costs at most this many files.
const MaxUnread = 3

// ErrBacklog is a frame refused because the terminal has not read the
// last MaxUnread frames.
var ErrBacklog = errors.New("kitty: the terminal has not read the last frames")

// Cell is a terminal cell, 0-based.
type Cell struct{ Col, Row int }

// Transport sends frames for one process. It is not safe for concurrent
// use.
type Transport struct {
	dir string
	pid int
	seq uint64
	// shown is the image id on screen: 1 or 2, 0 when none is.
	shown int
	// unread are the frame files sent and not yet read (deleted) by the
	// terminal, oldest first.
	unread []string
}

// NewTransport sends frames through files in dir (DefaultDir in
// production, a test's temp dir in tests) named for process pid.
func NewTransport(dir string, pid int) *Transport {
	return &Transport{dir: dir, pid: pid}
}

func (t *Transport) prefix() string {
	return "tty-graphics-protocol-rex-amq-ui-" + strconv.Itoa(t.pid) + "-"
}

// Shown reports whether an image of this transport may be on screen.
func (t *Transport) Shown() bool { return t.shown != 0 }

// Show writes img to a new frame file and writes to w, in one Write, the
// commands that show it with its top-left corner at cell at: save the
// cursor, move to at, transmit and place the image under the id not on
// screen, restore the cursor, then delete the image that was on screen.
// It refuses with ErrBacklog while MaxUnread files wait for the terminal.
// On any error nothing is shown, the file is gone, and the image on screen
// stays.
func (t *Transport) Show(w io.Writer, img *image.RGBA, at Cell) error {
	iw, ih := img.Rect.Dx(), img.Rect.Dy()
	if iw <= 0 || ih <= 0 {
		return fmt.Errorf("kitty: empty %dx%d frame", iw, ih)
	}
	t.forgetRead()
	if len(t.unread) >= MaxUnread {
		return ErrBacklog
	}
	t.seq++
	path := filepath.Join(t.dir, t.prefix()+strconv.FormatUint(t.seq, 10)+".rgba")
	if err := os.WriteFile(path, packed(img), 0o600); err != nil {
		return fmt.Errorf("kitty: frame file: %w", err)
	}
	id := 1
	if t.shown == 1 {
		id = 2
	}
	out := fmt.Appendf(nil, "\x1b7\x1b[%d;%dH\x1b_Ga=T,f=32,t=t,s=%d,v=%d,i=%d,p=1,C=1,q=2;%s\x1b\\\x1b8",
		at.Row+1, at.Col+1, iw, ih, id, base64.StdEncoding.EncodeToString([]byte(path)))
	if t.shown != 0 {
		out = fmt.Appendf(out, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", t.shown)
	}
	if _, err := w.Write(out); err != nil {
		return errors.Join(fmt.Errorf("kitty: write: %w", err), removeFile(path))
	}
	t.shown = id
	t.unread = append(t.unread, path)
	return nil
}

// Clear writes to w the command that deletes every image this terminal
// shows (leaving the Graph tab, pausing, minimizing).
func (t *Transport) Clear(w io.Writer) error {
	if _, err := io.WriteString(w, "\x1b_Ga=d,d=A,q=2\x1b\\"); err != nil {
		return fmt.Errorf("kitty: write: %w", err)
	}
	t.shown = 0
	return nil
}

// Cleanup removes this process's frame files the terminal has not read,
// leaving every other process's alone. Run it at exit.
func (t *Transport) Cleanup() error {
	t.unread = nil
	files, err := filepath.Glob(filepath.Join(t.dir, t.prefix()+"*.rgba"))
	if err != nil {
		return fmt.Errorf("kitty: cleanup: %w", err)
	}
	var errs []error
	for _, f := range files {
		errs = append(errs, removeFile(f))
	}
	return errors.Join(errs...)
}

// forgetRead drops the files the terminal has read (and so deleted).
func (t *Transport) forgetRead() {
	kept := t.unread[:0]
	for _, f := range t.unread {
		if _, err := os.Stat(f); err == nil {
			kept = append(kept, f)
		}
	}
	t.unread = kept
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// packed is img's pixels as tight RGBA rows (f=32), copying only when img
// is a sub-image whose rows are further apart.
func packed(img *image.RGBA) []byte {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if img.Stride == 4*w {
		off := img.PixOffset(img.Rect.Min.X, img.Rect.Min.Y)
		return img.Pix[off : off+4*w*h]
	}
	out := make([]byte, 0, 4*w*h)
	for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
		off := img.PixOffset(img.Rect.Min.X, y)
		out = append(out, img.Pix[off:off+4*w]...)
	}
	return out
}
