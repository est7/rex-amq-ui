package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/est7/rex-amq-ui/internal/apc"
	"github.com/est7/rex-amq-ui/internal/ui"
)

// syncBuffer is the panel's terminal output.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) snapshot() (raw string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String(), s.b.Len()
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestPanelProgram runs the real panel (Bubble Tea program, APC input
// filter, adapter stream) on pipes against a fake adapter that sends its
// snapshot, then the rest of its stream once the test says so.
func TestPanelProgram(t *testing.T) {
	// The panel forces TrueColor: an environment that asks for no colour
	// must not strip its backgrounds.
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	dir := t.TempDir()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "panel", "edited-crew-attention.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	gate, done := filepath.Join(dir, "go"), filepath.Join(dir, "done")
	script := filepath.Join(dir, "rex-amq-adapter")
	body := "#!/bin/sh\nhead -n 1 '" + fixture + "'\nwhile [ ! -e '" + gate + "' ]; do sleep 0.02; done\n" +
		"tail -n +2 '" + fixture + "'\ntouch '" + done + "'\nexec sleep 60\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inW.Close()
	out := &syncBuffer{}
	errc := make(chan error, 1)
	go func() {
		errc <- runPanel(context.Background(), panelConfig{
			Session: "session:s", AdapterBin: script, In: inR, Out: out, Width: 70, Height: 48,
			Options: ui.Options{},
		})
	}()
	shows := func(s string) func() bool {
		return func() bool { raw, _ := out.snapshot(); return strings.Contains(ansi.Strip(raw), s) }
	}
	waitFor(t, "the snapshot on screen", shows("auth-refresh"))
	waitFor(t, "the claude-02 turn error alert", shows("hit a turn error"))

	inW.WriteString(apc.Encode(apc.Command{Verb: apc.Pause}))
	time.Sleep(300 * time.Millisecond) // the pause lands; a last frame may flush
	_, before := out.snapshot()
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the rest of the stream", func() bool { _, err := os.Stat(done); return err == nil })
	time.Sleep(400 * time.Millisecond)
	if _, after := out.snapshot(); after != before {
		raw, _ := out.snapshot()
		t.Fatalf("the paused panel wrote %d bytes: %q", after-before, raw[before:])
	}
	if shows("is waiting on you")() {
		t.Fatal("frames reached the screen while paused")
	}

	inW.WriteString(apc.Encode(apc.Command{Verb: apc.Resume}))
	waitFor(t, "resume to draw the frames folded while paused", shows("is waiting on you"))

	// A sequence split across writes, then keys: tab:members, Q.
	inW.WriteString("\x1b_amqui;tab:mem")
	time.Sleep(100 * time.Millisecond)
	inW.WriteString("bers\x1b\\")
	waitFor(t, "tab:members", shows("Retired 1"))

	inW.WriteString("Q")
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("panel ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Q did not quit the panel")
	}
	raw, _ := out.snapshot()
	if strings.Contains(raw, "amqui") || strings.Contains(raw, "\x1b_") {
		t.Fatal("APC bytes reached the panel's output")
	}
	if !strings.Contains(raw, "48;2;") {
		t.Fatal("no truecolor background in the output")
	}
	seen, bad := erases(raw)
	if bad != nil {
		t.Fatalf("erases on the default background: %v", bad)
	}
	t.Logf("line erases the renderer used, all under a set background: %v", seen)
}

func TestErasesCheckDetectsDefaultBackground(t *testing.T) {
	if _, bad := erases("\x1b[48;2;1;2;3mab\x1b[38;2;49;0;0mc\x1b[K"); bad != nil {
		t.Fatalf("an erase under a background flagged: %v", bad)
	}
	for _, raw := range []string{"ab\x1b[K", "\x1b[48;2;1;2;3mab\x1b[m\x1b[5X", "\x1b[48;5;3ma\x1b[49m\x1b[K"} {
		if _, bad := erases(raw); bad == nil {
			t.Errorf("%q: not detected", raw)
		}
	}
}

var csi = regexp.MustCompile(`\x1b\[([0-9;:?]*)([@-~])`)

// erases checks that the renderer erases within a line (EL K, ECH X) only
// while a background colour is set: the cells it clears then keep the
// panel's background on a terminal with bce (Rex's TERM, xterm-256color,
// declares it). It returns the erase sequences seen and those that erase
// on the default background.
func erases(raw string) (seen map[string]int, bad []string) {
	seen = map[string]int{}
	bg := false
	for _, m := range csi.FindAllStringSubmatch(raw, -1) {
		params, final := m[1], m[2]
		switch final {
		case "m":
			ps := strings.Split(params, ";")
			for i := 0; i < len(ps); i++ {
				switch p := ps[i]; {
				case p == "" || p == "0" || p == "49":
					bg = false
				case p == "48":
					bg = true
					if i+1 < len(ps) && ps[i+1] == "2" {
						i += 4
					} else {
						i += 2
					}
				case p == "38" || p == "58":
					if i+1 < len(ps) && ps[i+1] == "2" {
						i += 4
					} else {
						i += 2
					}
				case len(p) == 2 && (p[0] == '4' || p == "10"):
					bg = true
				}
			}
		case "K", "X":
			seen["CSI "+params+final]++
			if !bg {
				bad = append(bad, "CSI "+params+final)
			}
		}
	}
	return seen, bad
}

func TestOpenExitCodes(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	rex := env(map[string]string{"REX_SERVER": "unix:///nonexistent/rex.sock", "REX_SESSION": "session:s"})
	for _, c := range []struct {
		name string
		args []string
		env  func(string) string
		want int
	}{
		{"help", []string{"--help"}, rex, 0},
		{"unknown flag", []string{"--nope"}, rex, 2},
		{"unknown tab", []string{"--tab", "inbox"}, rex, 2},
		{"no server", nil, env(map[string]string{"REX_SESSION": "session:s"}), 2},
		{"no session", nil, env(map[string]string{"REX_SERVER": "unix:///x.sock"}), 2},
		{"Rex unreachable", []string{"--tab", "mail"}, rex, 1},
	} {
		var out, errb bytes.Buffer
		if got := runOpen(c.args, c.env, &out, &errb); got != c.want || out.Len() != 0 {
			t.Errorf("%s: exit %d stdout %q, want exit %d and no stdout", c.name, got, out.String(), c.want)
		}
	}
}
