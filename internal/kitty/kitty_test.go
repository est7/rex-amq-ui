package kitty_test

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

func TestKittyGraphFirstFrame(t *testing.T) {
	dir := t.TempDir()
	tr := kitty.NewTransport(dir, pid)
	img := frame(5, 3, 10)
	out, err := tr.Frame(img, kitty.Cell{Col: 2, Row: 4})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fmt.Sprintf("tty-graphics-protocol-rex-amq-ui-%d-1.rgba", pid))
	want := "\x1b7\x1b[5;3H" + transmit(path, 5, 3, 1) + "\x1b8"
	if string(out) != want {
		t.Fatalf("first frame\n got %q\nwant %q\n(save cursor, move to the region's top-left cell, transmit image 1, restore; nothing to delete yet)", out, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("frame file: %v", err)
	}
	if !bytes.Equal(data, img.Pix) {
		t.Errorf("frame file holds %d bytes not equal to the image's RGBA", len(data))
	}
}

func TestKittyGraphDoubleBuffers(t *testing.T) {
	dir := t.TempDir()
	tr := kitty.NewTransport(dir, pid)
	at := kitty.Cell{Col: 0, Row: 2}
	if _, err := tr.Frame(frame(4, 4, 1), at); err != nil {
		t.Fatal(err)
	}
	var ids []int
	var paths []string
	for i := range 4 {
		out, err := tr.Frame(frame(4, 4, byte(i)), at)
		if err != nil {
			t.Fatal(err)
		}
		id, old := 2, 1
		if i%2 == 1 {
			id, old = 1, 2
		}
		p := sentPath(t, out)
		want := "\x1b7\x1b[3;1H" + transmit(p, 4, 4, id) + "\x1b8" + del(old)
		if string(out) != want {
			t.Fatalf("frame %d\n got %q\nwant %q\n(new image first, then delete the old one: no blank frame)", i+2, out, want)
		}
		ids = append(ids, id)
		paths = append(paths, p)
	}
	if !slices.Equal(ids, []int{2, 1, 2, 1}) {
		t.Errorf("image ids %v, want alternating 2 1 2 1", ids)
	}
	for i, p := range paths {
		if want := fmt.Sprintf("tty-graphics-protocol-rex-amq-ui-%d-%d.rgba", pid, i+2); filepath.Base(p) != want {
			t.Errorf("frame file %s, want %s: every frame gets a new sequence number", filepath.Base(p), want)
		}
	}
}

func TestKittyGraphClearAll(t *testing.T) {
	tr := kitty.NewTransport(t.TempDir(), pid)
	at := kitty.Cell{}
	if _, err := tr.Frame(frame(2, 2, 0), at); err != nil {
		t.Fatal(err)
	}
	if got := string(tr.ClearAll()); got != "\x1b_Ga=d,d=A,q=2\x1b\\" {
		t.Fatalf("ClearAll %q", got)
	}
	out, err := tr.Frame(frame(2, 2, 0), at)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("a=d")) {
		t.Errorf("first frame after ClearAll deletes an image already gone: %q", out)
	}
	if !bytes.Contains(out, []byte(",i=1,")) {
		t.Errorf("first frame after ClearAll %q, want image 1", out)
	}
}

func TestKittyGraphCleanupOwnFilesOnly(t *testing.T) {
	dir := t.TempDir()
	tr := kitty.NewTransport(dir, pid)
	for range 3 {
		if _, err := tr.Frame(frame(2, 2, 0), kitty.Cell{}); err != nil {
			t.Fatal(err)
		}
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
	dir := t.TempDir()
	big := frame(10, 10, 3)
	sub := big.SubImage(image.Rect(2, 3, 6, 5)).(*image.RGBA)
	out, err := kitty.NewTransport(dir, pid).Frame(sub, kitty.Cell{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("s=4,v=2,")) {
		t.Errorf("command %q, want s=4,v=2", out)
	}
	data, err := os.ReadFile(sentPath(t, out))
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

func TestKittyGraphWriteFailureKeepsState(t *testing.T) {
	dir := t.TempDir()
	tr := kitty.NewTransport(filepath.Join(dir, "missing"), pid)
	out, err := tr.Frame(frame(2, 2, 0), kitty.Cell{})
	if err == nil || out != nil {
		t.Fatalf("frame into a missing directory: out %q err %v, want an error and no bytes", out, err)
	}
	if err := os.Mkdir(filepath.Join(dir, "missing"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err = tr.Frame(frame(2, 2, 0), kitty.Cell{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("a=d")) || !bytes.Contains(out, []byte(",i=1,")) {
		t.Errorf("after a failed frame %q, want image 1 with nothing to delete", out)
	}
}

func TestKittyGraphRejectsEmptyImage(t *testing.T) {
	if _, err := kitty.NewTransport(t.TempDir(), pid).Frame(image.NewRGBA(image.Rect(0, 0, 0, 5)), kitty.Cell{}); err == nil {
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
		render, next, again := s.Fire(tick, a, now)
		if !render || !again || next != s.Interval() {
			t.Fatalf("frame %d: render %v again %v next %v, want a frame and the next in %v", i, render, again, next, s.Interval())
		}
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
	s.Fire(tick, a, start)
	a.on = false
	render, _, again := s.Fire(tick, a, start.Add(s.Interval()))
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

func TestSchedulerStaleTickAfterStop(t *testing.T) {
	s := kitty.NewScheduler(30)
	a := &anim{on: true}
	old, _, _ := s.Kick(a, start)
	s.Stop()
	if render, _, again := s.Fire(old, a, start); render || again {
		t.Errorf("tick of a stopped loop rendered (%v) or continued (%v)", render, again)
	}
	cur, _, ok := s.Kick(a, start.Add(time.Second))
	if !ok {
		t.Fatal("Kick after Stop did not restart")
	}
	if render, _, _ := s.Fire(old, a, start.Add(time.Second)); render {
		t.Errorf("an old loop's late tick rendered beside the new loop")
	}
	if render, _, again := s.Fire(cur, a, start.Add(time.Second)); !render || !again {
		t.Errorf("the new loop's tick did not render")
	}
}
