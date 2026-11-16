package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeAdapter writes an adapter that prints fixture (a recorded or edited
// `panel --json` stream) and then stays up, as the real one does, until it
// is stopped. It records its argv to argsFile.
func fakeAdapter(t *testing.T, fixture string) (bin, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	path, err := filepath.Abs(filepath.Join("..", "..", "testdata", "panel", fixture))
	if err != nil {
		t.Fatal(err)
	}
	argsFile = filepath.Join(dir, "args")
	bin = filepath.Join(dir, "rex-amq-adapter")
	body := "#!/bin/sh\necho \"$@\" > '" + argsFile + "'\ncat '" + path + "'\nexec sleep 60\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile
}

// writeAdapter writes an adapter whose whole script is body.
func writeAdapter(t *testing.T, body string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "rex-amq-adapter")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

type waitingOut struct {
	Session string `json:"session"`
	Members []struct {
		Handle  string   `json:"handle"`
		BlockID *string  `json:"block_id"`
		Reasons []string `json:"reasons"`
	} `json:"members"`
}

func runWaitingTest(t *testing.T, args []string, env map[string]string, timeout time.Duration) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	getenv := func(k string) string { return env[k] }
	start := time.Now()
	code = runWaiting(args, getenv, &out, &errb, timeout)
	if took := time.Since(start); took > timeout+3*time.Second {
		t.Fatalf("waiting took %v with a %v timeout", took, timeout)
	}
	return code, out.String(), errb.String()
}

func decodeWaiting(t *testing.T, stdout string) waitingOut {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want one JSON line, got %d: %q", len(lines), stdout)
	}
	var w waitingOut
	if err := json.Unmarshal([]byte(lines[0]), &w); err != nil {
		t.Fatalf("stdout %q: %v", stdout, err)
	}
	return w
}

// TestWaitingSeveral: a session where two members wait on the human, each
// for its own reason; members are in handle order, with their panes.
func TestWaitingSeveral(t *testing.T) {
	bin, argsFile := fakeAdapter(t, "edited-crew-attention.ndjson")
	code, stdout, stderr := runWaitingTest(t, []string{"--json", "--session", "session:s", "--adapter-bin", bin}, nil, 5*time.Second)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	w := decodeWaiting(t, stdout)
	if w.Session != "session:s" {
		t.Errorf("session %q", w.Session)
	}
	type member struct {
		handle, block string
		reasons       []string
	}
	var got []member
	for _, m := range w.Members {
		b := "<none>"
		if m.BlockID != nil {
			b = *m.BlockID
		}
		got = append(got, member{m.Handle, b, m.Reasons})
	}
	want := []member{
		{"claude-02", "block:claude-02", []string{"turn_error"}},
		{"pi-04", "block:pi-04", []string{"stalled"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("members\n got %+v\nwant %+v", got, want)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if a := strings.TrimSpace(string(raw)); a != "panel --json --session session:s" {
		t.Errorf("adapter argv %q", a)
	}
}

// TestWaitingNone: sessions where nobody waits print an empty list (never
// null: the Lua side decodes null as nil), with the session the stream
// watched.
func TestWaitingNone(t *testing.T) {
	for _, fixture := range []string{"edited-personal-cockpit.ndjson", "edited-no-view.ndjson"} {
		t.Run(fixture, func(t *testing.T) {
			bin, _ := fakeAdapter(t, fixture)
			code, stdout, stderr := runWaitingTest(t, []string{"--json", "--adapter-bin", bin}, nil, 5*time.Second)
			if code != 0 {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
			if !strings.Contains(stdout, `"members":[]`) {
				t.Errorf("stdout %q: want \"members\":[]", stdout)
			}
			w := decodeWaiting(t, stdout)
			if !strings.HasPrefix(w.Session, "session:") || len(w.Members) != 0 {
				t.Errorf("got %+v", w)
			}
		})
	}
}

// TestWaitingReasonsAndGonePanes: a member's reasons are its mark kinds,
// each once, in the order they first appear, without their times; a member
// whose pane is gone has no block to focus.
func TestWaitingReasonsAndGonePanes(t *testing.T) {
	bin, _ := fakeAdapter(t, "edited-waiters-repeat-gone.ndjson")
	code, stdout, stderr := runWaitingTest(t, []string{"--json", "--adapter-bin", bin},
		map[string]string{"REX_SESSION": "session:s"}, 5*time.Second)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	w := decodeWaiting(t, stdout)
	if len(w.Members) != 2 {
		t.Fatalf("members %+v", w.Members)
	}
	c, p := w.Members[0], w.Members[1]
	if c.Handle != "claude-01" || c.BlockID == nil || *c.BlockID != "block:claude-01" ||
		!reflect.DeepEqual(c.Reasons, []string{"waiting_on_the_human", "turn_error"}) {
		t.Errorf("claude-01: %+v (block %v)", c, c.BlockID)
	}
	if p.Handle != "pi-04" || p.BlockID != nil || !reflect.DeepEqual(p.Reasons, []string{"stalled"}) {
		t.Errorf("pi-04 (pane gone): %+v (block %v)", p, p.BlockID)
	}
}

// TestWaitingSessionFromEnv: without --session the adapter is asked for
// $REX_SESSION.
func TestWaitingSessionFromEnv(t *testing.T) {
	bin, argsFile := fakeAdapter(t, "edited-crew-attention.ndjson")
	code, _, stderr := runWaitingTest(t, []string{"--json", "--adapter-bin", bin},
		map[string]string{"REX_SESSION": "session:env"}, 5*time.Second)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	raw, _ := os.ReadFile(argsFile)
	if a := strings.TrimSpace(string(raw)); a != "panel --json --session session:env" {
		t.Errorf("adapter argv %q", a)
	}
}

// TestWaitingAdapterFails: an adapter that exits before its snapshot, or
// cannot start, is exit 1 at once (no retry loop), with its reason.
func TestWaitingAdapterFails(t *testing.T) {
	cases := map[string]string{
		"exits":    writeAdapter(t, "echo 'panel: refused: agent pane' >&2\nexit 3\n"),
		"garbage":  writeAdapter(t, "echo 'not json'\nexec sleep 60\n"),
		"no-start": filepath.Join(t.TempDir(), "missing-adapter"),
	}
	for name, bin := range cases {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			code, stdout, stderr := runWaitingTest(t, []string{"--json", "--session", "session:s", "--adapter-bin", bin}, nil, 5*time.Second)
			if code != 1 || stdout != "" {
				t.Fatalf("exit %d stdout %q", code, stdout)
			}
			if took := time.Since(start); took > 3*time.Second {
				t.Errorf("took %v: a failed adapter should end waiting at once", took)
			}
			if !strings.HasPrefix(stderr, "rex-amq-ui waiting:") {
				t.Errorf("stderr %q", stderr)
			}
			if name == "exits" && !strings.Contains(stderr, "refused: agent pane") {
				t.Errorf("stderr %q: want the adapter's reason", stderr)
			}
		})
	}
}

// TestWaitingNoSnapshotInTime: an adapter that never sends its snapshot is
// exit 1 after the timeout, and its process is stopped.
func TestWaitingNoSnapshotInTime(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	bin := writeAdapter(t, "echo $$ > '"+pidFile+"'\nexec sleep 60\n")
	start := time.Now()
	code, stdout, stderr := runWaitingTest(t, []string{"--json", "--session", "session:s", "--adapter-bin", bin}, nil, 1500*time.Millisecond)
	if code != 1 || stdout != "" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if took := time.Since(start); took < 1500*time.Millisecond {
		t.Errorf("gave up after %v, before the timeout", took)
	}
	if !strings.Contains(stderr, "no snapshot") {
		t.Errorf("stderr %q", stderr)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	if alive(strings.TrimSpace(string(raw))) {
		t.Errorf("adapter %s still running after waiting returned", raw)
	}
}

// TestWaitingUsage: bad invocations are exit 2 and never start the adapter.
func TestWaitingUsage(t *testing.T) {
	bin, argsFile := fakeAdapter(t, "edited-crew-attention.ndjson")
	for _, args := range [][]string{
		{"--adapter-bin", bin},                     // no --json
		{"--json", "--nope", "--adapter-bin", bin}, // unknown flag
		{"--json", "--adapter-bin", bin, "extra"},  // positional
	} {
		code, stdout, _ := runWaitingTest(t, args, nil, 5*time.Second)
		if code != 2 || stdout != "" {
			t.Errorf("%v: exit %d stdout %q", args, code, stdout)
		}
	}
	if _, err := os.Stat(argsFile); err == nil {
		t.Errorf("a usage error started the adapter")
	}
}

// TestWaitingDispatch: `rex-amq-ui waiting` reaches the subcommand.
func TestWaitingDispatch(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"waiting", "--nope"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errb.String(), "rex-amq-ui waiting") {
		t.Errorf("stderr %q", errb.String())
	}
}

func alive(pid string) bool {
	n, err := strconv.Atoi(pid)
	return err == nil && syscall.Kill(n, 0) == nil
}
