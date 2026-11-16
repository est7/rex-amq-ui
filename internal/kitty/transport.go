// Package kitty shows the Graph tab's frames through the Kitty graphics
// protocol and paces them.
//
// Transport hands each frame to the terminal as a temporary file (t=t),
// alternating two image ids so the new frame is up before the old one is
// deleted: no blank frame between them. It returns the escape sequences
// instead of writing them, so the caller can send them through the same
// writer as the rest of the screen and they never interleave with it.
package kitty

import (
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strconv"
)

// DefaultDir is where frame files go. Kitty-protocol terminals read t=t
// files only from a temporary directory and only when the name contains
// "tty-graphics-protocol"; they delete each file once read.
const DefaultDir = "/tmp"

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
}

// NewTransport sends frames through files in dir (DefaultDir in
// production, a test's temp dir in tests) named for process pid.
func NewTransport(dir string, pid int) *Transport {
	return &Transport{dir: dir, pid: pid}
}

func (t *Transport) prefix() string {
	return "tty-graphics-protocol-rex-amq-ui-" + strconv.Itoa(t.pid) + "-"
}

// Frame writes img to a new frame file and returns the bytes that show it
// with its top-left corner at cell at: save the cursor, move to at,
// transmit and place the image under the id not on screen, restore the
// cursor, then delete the image that was on screen. On error nothing is
// shown and the image on screen stays.
func (t *Transport) Frame(img *image.RGBA, at Cell) ([]byte, error) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("kitty: empty %dx%d frame", w, h)
	}
	path := filepath.Join(t.dir, t.prefix()+strconv.FormatUint(t.seq+1, 10)+".rgba")
	if err := os.WriteFile(path, packed(img), 0o600); err != nil {
		return nil, fmt.Errorf("kitty: frame file: %w", err)
	}
	t.seq++
	id := 1
	if t.shown == 1 {
		id = 2
	}
	out := fmt.Appendf(nil, "\x1b7\x1b[%d;%dH\x1b_Ga=T,f=32,t=t,s=%d,v=%d,i=%d,p=1,C=1,q=2;%s\x1b\\\x1b8",
		at.Row+1, at.Col+1, w, h, id, base64.StdEncoding.EncodeToString([]byte(path)))
	if t.shown != 0 {
		out = fmt.Appendf(out, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", t.shown)
	}
	t.shown = id
	return out, nil
}

// ClearAll returns the bytes that delete every image this terminal shows
// (leaving the Graph tab, pausing, minimizing).
func (t *Transport) ClearAll() []byte {
	t.shown = 0
	return []byte("\x1b_Ga=d,d=A,q=2\x1b\\")
}

// Cleanup removes this process's frame files the terminal has not read,
// leaving every other process's alone. Run it at exit.
func (t *Transport) Cleanup() error {
	files, err := filepath.Glob(filepath.Join(t.dir, t.prefix()+"*.rgba"))
	if err != nil {
		return fmt.Errorf("kitty: cleanup: %w", err)
	}
	var errs []error
	for _, f := range files {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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
