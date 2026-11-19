package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestHelperPanel is the panel process TestPanelOnATerminal runs under a
// pseudo-terminal; it does nothing in a normal test run.
func TestHelperPanel(t *testing.T) {
	if os.Getenv("REX_AMQ_UI_HELPER") != "1" {
		return
	}
	stty := exec.Command("stty", "cols", "70", "rows", "24")
	stty.Stdin = os.Stdin
	if err := stty.Run(); err != nil {
		fmt.Println("stty:", err)
	}
	code := run([]string{"--adapter-bin", os.Getenv("REX_AMQ_UI_HELPER_ADAPTER")}, os.Stdout, os.Stderr)
	fmt.Printf("EXIT=%d\n", code)
	os.Exit(0)
}

var escapes = regexp.MustCompile(`\x1b\[[0-9;:?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78]`)

// TestPanelOnATerminal runs the panel on a real pseudo-terminal (script(1)),
// outside Rex: raw mode, the input filter's descriptor polling, the alt
// screen, and a clean exit on Q, with an Esc key in between.
func TestPanelOnATerminal(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("uses BSD script(1)")
	}
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("no script(1)")
	}
	dir := t.TempDir()
	fixture, _ := filepath.Abs(filepath.Join("..", "..", "testdata", "panel", "edited-crew-attention.ndjson"))
	fake := filepath.Join(dir, "rex-amq-adapter")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ncat '"+fixture+"'\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ts := filepath.Join(dir, "typescript")
	cmd := exec.Command("script", "-q", "-t", "0", ts, os.Args[0], "-test.run=^TestHelperPanel$")
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "REX_") && !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "XDG_CACHE_HOME=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env,
		"HOME="+filepath.Join(dir, "home"),
		"XDG_CACHE_HOME="+filepath.Join(dir, "cache"),
		"REX_AMQ_UI_HELPER=1",
		"REX_AMQ_UI_HELPER_ADAPTER="+fake,
	)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	screen := func() string { b, _ := os.ReadFile(ts); return escapes.ReplaceAllString(string(b), "") }
	waitFor(t, "the panel on screen", func() bool { return strings.Contains(screen(), "is waiting on you") })
	// A lone Esc at the top tries to minimize: with no Rex the footer says
	// so. Held instead of delivered, it would never show.
	in.Write([]byte("\x1b"))
	waitFor(t, "the Esc key to reach the panel", func() bool { return strings.Contains(screen(), "not connected to Rex") })
	for _, k := range []string{"j", "z", "Q"} {
		in.Write([]byte(k))
		time.Sleep(200 * time.Millisecond)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		cmd.Process.Kill()
		t.Fatal("the panel did not exit on Q")
	}
	raw, _ := os.ReadFile(ts)
	got := screen()
	for _, want := range []string{"auth-refresh", "Drawer", "not connected to Rex", "EXIT=0"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	if !strings.Contains(string(raw), "\x1b[?1049h") || !strings.Contains(string(raw), "\x1b[?1049l") {
		t.Error("the alt screen was not entered and left")
	}
	if strings.Contains(got, "^[") || strings.Contains(got, "1;2c") {
		t.Errorf("terminal replies or escapes leaked as text")
	}
}
