package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
}
