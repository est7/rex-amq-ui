package kitty_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/est7/rex-amq-ui/internal/kitty"
)

const pid = 4242

func frame(w, h int, fill byte) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = fill + byte(i%7)
	}
	return img
}

// transmit is the a=T command for path at w x h with image id.
func transmit(path string, w, h, id int) string {
	return fmt.Sprintf("\x1b_Ga=T,f=32,t=t,s=%d,v=%d,i=%d,p=1,C=1,q=2;%s\x1b\\", w, h, id, base64.StdEncoding.EncodeToString([]byte(path)))
}

func del(id int) string { return fmt.Sprintf("\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id) }

var pathRE = regexp.MustCompile(`;([A-Za-z0-9+/=]+)\x1b\\`)

// sentPath decodes the file path the a=T command in out names.
func sentPath(t *testing.T, out []byte) string {
	t.Helper()
	m := pathRE.FindSubmatch(out)
	if m == nil {
		t.Fatalf("no transmit payload in %q", out)
	}
	p, err := base64.StdEncoding.DecodeString(string(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	return string(p)
}

// show is tr.Show into a buffer, returning what it wrote.
func show(t *testing.T, tr *kitty.Transport, img *image.RGBA, at kitty.Cell) string {
	t.Helper()
	var b bytes.Buffer
	if err := tr.Show(&b, img, at); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// read is the terminal reading the frame file out names, as Kitty does
// for t=t: it deletes the file.
func read(t *testing.T, out string) {
	t.Helper()
	if err := os.Remove(sentPath(t, []byte(out))); err != nil {
		t.Fatal(err)
	}
}

func TestKittyGraphFirstFrame(t *testing.T) {
	dir := t.TempDir()
	tr := kitty.NewTransport(dir, pid)
	img := frame(5, 3, 10)
	out := show(t, tr, img, kitty.Cell{Col: 2, Row: 4})
	path := filepath.Join(dir, fmt.Sprintf("tty-graphics-protocol-rex-amq-ui-%d-1.rgba", pid))
	want := "\x1b7\x1b[5;3H" + transmit(path, 5, 3, 1) + "\x1b8"
	if out != want {
		t.Fatalf("first frame\n got %q\nwant %q\n(save cursor, move to the region's top-left cell, transmit image 1, restore; nothing to delete yet)", out, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("frame file: %v", err)
	}
	if !bytes.Equal(data, img.Pix) {
		t.Errorf("frame file holds %d bytes not equal to the image's RGBA", len(data))
	}
	if !tr.Shown() {
		t.Errorf("Shown false after a written frame")
	}
}

func TestKittyGraphDoubleBuffers(t *testing.T) {
	dir := t.TempDir()
	tr := kitty.NewTransport(dir, pid)
	at := kitty.Cell{Col: 0, Row: 2}
	read(t, show(t, tr, frame(4, 4, 1), at))
	var ids []int
	for i := range 4 {
		out := show(t, tr, frame(4, 4, byte(i)), at)
		id, old := 2, 1
		if i%2 == 1 {
			id, old = 1, 2
		}
		p := sentPath(t, []byte(out))
		want := "\x1b7\x1b[3;1H" + transmit(p, 4, 4, id) + "\x1b8" + del(old)
		if out != want {
			t.Fatalf("frame %d\n got %q\nwant %q\n(new image first, then delete the old one: no blank frame)", i+2, out, want)
		}
		if want := fmt.Sprintf("tty-graphics-protocol-rex-amq-ui-%d-%d.rgba", pid, i+2); filepath.Base(p) != want {
			t.Errorf("frame file %s, want %s: every frame gets a new sequence number", filepath.Base(p), want)
		}
		read(t, out)
		ids = append(ids, id)
	}
	if !slices.Equal(ids, []int{2, 1, 2, 1}) {
		t.Errorf("image ids %v, want alternating 2 1 2 1", ids)
	}
}

func TestKittyGraphClearAll(t *testing.T) {
	tr := kitty.NewTransport(t.TempDir(), pid)
	read(t, show(t, tr, frame(2, 2, 0), kitty.Cell{}))
	var b bytes.Buffer
	if err := tr.Clear(&b); err != nil || b.String() != "\x1b_Ga=d,d=A,q=2\x1b\\" {
		t.Fatalf("Clear wrote %q, %v", b.String(), err)
	}
	if tr.Shown() {
		t.Errorf("Shown after Clear")
	}
	out := show(t, tr, frame(2, 2, 0), kitty.Cell{})
	if strings.Contains(out, "a=d") || !strings.Contains(out, ",i=1,") {
		t.Errorf("first frame after Clear %q, want image 1 with nothing to delete", out)
	}
}

// failing is a terminal whose writes fail.
type failing struct{}

func (failing) Write([]byte) (int, error) { return 0, errors.New("terminal gone") }

func TestKittyGraphFailedWriteCommitsNothing(t *testing.T) {
	dir := t.TempDir()
	tr := kitty.NewTransport(dir, pid)
	if err := tr.Show(failing{}, frame(2, 2, 0), kitty.Cell{}); err == nil {
		t.Fatal("a frame the terminal never got reported success")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.rgba")); len(left) != 0 {
		t.Errorf("the unsent frame's file stayed: %v", left)
	}
	if tr.Shown() {
		t.Errorf("Shown after a failed write")
	}
	read(t, show(t, tr, frame(2, 2, 0), kitty.Cell{}))
	if err := tr.Clear(failing{}); err == nil || !tr.Shown() {
		t.Errorf("a failed Clear: err %v, Shown %v; want the error and the image still counted as up", err, tr.Shown())
	}
	if _, err := os.Stat(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("unexpected")
	}
	gone := kitty.NewTransport(filepath.Join(dir, "missing"), pid)
	if err := gone.Show(io.Discard, frame(2, 2, 0), kitty.Cell{}); err == nil || gone.Shown() {
		t.Errorf("a frame into a missing directory: err %v, Shown %v", err, gone.Shown())
	}
}

func TestKittyGraphBacklogBounded(t *testing.T) {
	dir := t.TempDir()
	tr := kitty.NewTransport(dir, pid)
	var outs []string
	for range kitty.MaxUnread {
		outs = append(outs, show(t, tr, frame(2, 2, 0), kitty.Cell{}))
	}
	var b bytes.Buffer
	err := tr.Show(&b, frame(2, 2, 0), kitty.Cell{})
	if !errors.Is(err, kitty.ErrBacklog) || b.Len() != 0 {
		t.Fatalf("frame past %d unread: err %v, wrote %q; want ErrBacklog and nothing", kitty.MaxUnread, err, b.String())
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.rgba")); len(files) != kitty.MaxUnread {
		t.Errorf("%d frame files with a terminal that never reads, want at most %d", len(files), kitty.MaxUnread)
	}
	read(t, outs[0])
	if out := show(t, tr, frame(2, 2, 0), kitty.Cell{}); out == "" {
		t.Errorf("no frame after the terminal read one")
	}
}

func TestKittyGraphCleanupOwnFilesOnly(t *testing.T) {
	dir := t.TempDir()
	tr := kitty.NewTransport(dir, pid)
	for range 3 {
		show(t, tr, frame(2, 2, 0), kitty.Cell{})
	}
	others := []string{
		fmt.Sprintf("tty-graphics-protocol-rex-amq-ui-%d-1.rgba", pid+1),
		fmt.Sprintf("tty-graphics-protocol-rex-amq-ui-%d1-1.rgba", pid),
		"tty-graphics-protocol-other-app-1.rgba",
		"notes.txt",
	}
	for _, o := range others {
		if err := os.WriteFile(filepath.Join(dir, o), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	own, _ := filepath.Glob(filepath.Join(dir, fmt.Sprintf("tty-graphics-protocol-rex-amq-ui-%d-*.rgba", pid)))
	if len(own) != 3 {
		t.Fatalf("before Cleanup %d frame files of this process, want 3", len(own))
	}
	if err := tr.Cleanup(); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range ents {
		left = append(left, e.Name())
	}
	slices.Sort(left)
	slices.Sort(others)
	if !slices.Equal(left, others) {
		t.Errorf("after Cleanup %v, want only other processes' files %v", left, others)
	}
}

func TestKittyGraphPacksSubImage(t *testing.T) {
	big := frame(10, 10, 3)
	sub := big.SubImage(image.Rect(2, 3, 6, 5)).(*image.RGBA)
	out := show(t, kitty.NewTransport(t.TempDir(), pid), sub, kitty.Cell{})
	if !strings.Contains(out, "s=4,v=2,") {
		t.Errorf("command %q, want s=4,v=2", out)
	}
	data, err := os.ReadFile(sentPath(t, []byte(out)))
	if err != nil {
		t.Fatal(err)
	}
	var want []byte
	for y := 3; y < 5; y++ {
		want = append(want, big.Pix[big.PixOffset(2, y):big.PixOffset(6, y)]...)
	}
	if !bytes.Equal(data, want) {
		t.Errorf("sub-image file is not its rows packed tightly")
	}
}

func TestKittyGraphRejectsEmptyImage(t *testing.T) {
	if err := kitty.NewTransport(t.TempDir(), pid).Show(io.Discard, image.NewRGBA(image.Rect(0, 0, 0, 5)), kitty.Cell{}); err == nil {
		t.Errorf("a 0x5 frame was accepted")
	}
}

type anim struct{ on bool }

func (a *anim) NeedsAnimation() bool { return a.on }

var start = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func TestSchedulerIdleNeverTicks(t *testing.T) {
	s := kitty.NewScheduler(30)
	if _, _, ok := s.Kick(&anim{}, start); ok {
		t.Errorf("idle model started a frame loop")
	}
}

func TestSchedulerPacesFrames(t *testing.T) {
	s := kitty.NewScheduler(30)
	a := &anim{on: true}
	tick, d, ok := s.Kick(a, start)
	if !ok || d != 0 {
		t.Fatalf("Kick = %v %v, want a loop starting now", d, ok)
	}
	now := start
	for i := range 5 {
		render, nt, next, again := s.Fire(tick, a, now)
		if !render || !again || next != s.Interval() {
			t.Fatalf("frame %d: render %v again %v next %v, want a frame and the next in %v", i, render, again, next, s.Interval())
		}
		tick = nt
		now = now.Add(next)
	}
	if fps := float64(time.Second) / float64(s.Interval()); fps < 24 || fps > 30.01 {
		t.Errorf("%.1f fps, want 24-30", fps)
	}
}

func TestSchedulerFPSClamped(t *testing.T) {
	if got := kitty.NewScheduler(120).Interval(); got != time.Second/30 {
		t.Errorf("120 fps interval %v, want 1/30 s", got)
	}
	if got := kitty.NewScheduler(5).Interval(); got != time.Second/24 {
		t.Errorf("5 fps interval %v, want 1/24 s", got)
	}
}

func TestSchedulerStopsWhenSettled(t *testing.T) {
	s := kitty.NewScheduler(30)
	a := &anim{on: true}
	tick, _, _ := s.Kick(a, start)
	_, tick, _, _ = s.Fire(tick, a, start)
	a.on = false
	render, _, _, again := s.Fire(tick, a, start.Add(s.Interval()))
	if !render || again {
		t.Fatalf("settling tick: render %v again %v, want one last frame and no further tick", render, again)
	}
	if _, _, ok := s.Kick(a, start.Add(time.Second)); ok {
		t.Errorf("settled model restarted the loop")
	}
	a.on = true
	if _, d, ok := s.Kick(a, start.Add(s.Interval()+time.Millisecond)); !ok || d != s.Interval()-time.Millisecond {
		t.Errorf("Kick 1ms after the last frame: %v %v, want a loop waiting out the rest of the interval", d, ok)
	}
}

func TestSchedulerOneLoopAtATime(t *testing.T) {
	s := kitty.NewScheduler(30)
	a := &anim{on: true}
	if _, _, ok := s.Kick(a, start); !ok {
		t.Fatal("first Kick did not start the loop")
	}
	if _, _, ok := s.Kick(a, start); ok {
		t.Errorf("second Kick started a second loop: frames would double")
	}
}

func TestSchedulerOwns(t *testing.T) {
	s := kitty.NewScheduler(30)
	a := &anim{on: true}
	cur, _, _ := s.Kick(a, start)
	if !s.Owns(cur) {
		t.Fatalf("the running loop's tick is not its own")
	}
	s.Stop()
	next, _, _ := s.Kick(a, start)
	if s.Owns(cur) || !s.Owns(next) {
		t.Errorf("Owns(old)=%v Owns(new)=%v after a restart, want false true", s.Owns(cur), s.Owns(next))
	}
}

func TestSchedulerEachTickFiresOnce(t *testing.T) {
	s := kitty.NewScheduler(30)
	a := &anim{on: true}
	t1, _, _ := s.Kick(a, start)
	_, t2, _, _ := s.Fire(t1, a, start)
	if render, _, _, _ := s.Fire(t1, a, start.Add(s.Interval())); render {
		t.Errorf("a tick fired twice: a late duplicate would draw a frame out of order")
	}
	if !s.Owns(t2) || s.Owns(t1) {
		t.Errorf("Owns(t1)=%v Owns(t2)=%v, want only the next tick", s.Owns(t1), s.Owns(t2))
	}
	if render, _, _, _ := s.Fire(t2, a, start.Add(s.Interval())); !render {
		t.Errorf("the next tick did not draw")
	}
}

func TestSchedulerStaleTickAfterStop(t *testing.T) {
	s := kitty.NewScheduler(30)
	a := &anim{on: true}
	old, _, _ := s.Kick(a, start)
	s.Stop()
	if render, _, _, again := s.Fire(old, a, start); render || again {
		t.Errorf("tick of a stopped loop rendered (%v) or continued (%v)", render, again)
	}
	cur, _, ok := s.Kick(a, start.Add(time.Second))
	if !ok {
		t.Fatal("Kick after Stop did not restart")
	}
	if render, _, _, _ := s.Fire(old, a, start.Add(time.Second)); render {
		t.Errorf("an old loop's late tick rendered beside the new loop")
	}
	if render, _, _, again := s.Fire(cur, a, start.Add(time.Second)); !render || !again {
		t.Errorf("the new loop's tick did not render")
	}
}

// cutTerm takes the first cut bytes of a write and fails the rest, the
// way a terminal connection can die mid-command; short returns no error.
type cutTerm struct {
	cut   func([]byte) int
	short bool
	got   bytes.Buffer
}

func (c *cutTerm) Write(p []byte) (int, error) {
	n := min(c.cut(p), len(p))
	c.got.Write(p[:n])
	if n == len(p) {
		return n, nil
	}
	if c.short {
		return n, nil
	}
	return n, io.ErrUnexpectedEOF
}

// afterTransmit cuts a frame's commands right after the a=T command's
// string terminator: the terminal has the image, not the cursor restore.
func afterTransmit(p []byte) int {
	if i := bytes.Index(p, []byte("\x1b\\\x1b8")); i >= 0 {
		return i + 2
	}
	return len(p)
}

func TestKittyGraphPartialWriteKeepsCleanupOwed(t *testing.T) {
	for _, short := range []bool{false, true} {
		dir := t.TempDir()
		tr := kitty.NewTransport(dir, pid)
		term := &cutTerm{cut: afterTransmit, short: short}
		err := tr.Show(term, frame(2, 2, 0), kitty.Cell{})
		if err == nil {
			t.Fatalf("short=%v: a frame cut after its transmit reported success", short)
		}
		if short && !errors.Is(err, io.ErrShortWrite) {
			t.Errorf("a short write without an error: %v, want io.ErrShortWrite", err)
		}
		if !strings.Contains(term.got.String(), "a=T") || !tr.Shown() {
			t.Errorf("short=%v: the image reached the terminal but Shown is %v: nobody would clear it", short, tr.Shown())
		}
		if files, _ := filepath.Glob(filepath.Join(dir, "*.rgba")); len(files) != 1 {
			t.Errorf("short=%v: the possibly transmitted frame's file was removed (%v): the terminal may still read it", short, files)
		}
		var b bytes.Buffer
		if err := tr.Clear(&b); err != nil || tr.Shown() {
			t.Errorf("Clear after a partial frame: %v, Shown %v", err, tr.Shown())
		}
	}
	// A cut Clear owes the clear still.
	tr := kitty.NewTransport(t.TempDir(), pid)
	read(t, show(t, tr, frame(2, 2, 0), kitty.Cell{}))
	if err := tr.Clear(&cutTerm{cut: func(p []byte) int { return 3 }}); err == nil || !tr.Shown() {
		t.Errorf("a Clear cut short: err %v, Shown %v; want the error and the clear still owed", err, tr.Shown())
	}
}

func TestKittyGraphFailedFileWriteBounded(t *testing.T) {
	dir := t.TempDir()
	tr := kitty.NewTransport(dir, pid)
	// The file is created, then writing it fails (a full disk, a size
	// limit).
	createThenFail := func(name string, _ []byte, mode os.FileMode) error {
		if err := os.WriteFile(name, []byte("partial"), mode); err != nil {
			return err
		}
		return errors.New("disk full")
	}
	kitty.SetFiles(tr, createThenFail, os.Remove)
	for range 20 {
		if err := tr.Show(io.Discard, frame(2, 2, 0), kitty.Cell{}); err == nil {
			t.Fatal("a failed frame file reported success")
		}
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.rgba")); len(files) != 0 {
		t.Errorf("20 failed frame files left %d behind", len(files))
	}
	// When the half-written file cannot be removed either, it counts
	// against the budget.
	kitty.SetFiles(tr, createThenFail, func(string) error { return errors.New("read-only") })
	for range 20 {
		tr.Show(io.Discard, frame(2, 2, 0), kitty.Cell{})
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.rgba")); len(files) > kitty.MaxUnread {
		t.Errorf("%d undeletable failed frame files, want at most %d", len(files), kitty.MaxUnread)
	}
}
