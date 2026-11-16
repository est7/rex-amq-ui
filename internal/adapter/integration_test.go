package adapter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRex answers the three reads the adapter's panel stream makes of Rex
// (rex-amq-adapter internal/rex/rex.go: Whoami, ListBlocks, InspectSession),
// after the global flags it puts first. Anything else fails loudly.
const fakeRex = `#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in
    --autostart=false) shift ;;
    -S) shift 2 ;;
    *) break ;;
  esac
done
case "$1 $2" in
  "whoami --json") echo '{"instance_id":"it-instance","endpoint":"unix:///tmp/rex-amq-ui-it.sock"}' ;;
  "block ls") echo '{"blocks":[{"block_id":"block:it-panel","label":"panel","window_id":"window:it-1","placement":"placed"}]}' ;;
  "session inspect") printf '{"session_id":"%s","label":"rex-amq-ui-it","windows":[{"window_id":"window:it-1","label":"main"}]}\n' "$3" ;;
  *) echo "fake rex: unsupported: $*" >&2; exit 1 ;;
esac
`

// TestAdapterPanelIntegration runs the adapter named by REX_AMQ_ADAPTER_BIN
// (only that one: it does not resolve) as the human's panel would, isolated
// from every live Rex session and adapter state: env -i style, a fake rex
// first on PATH, an empty adapter home seeded with a crew claim and a
// lifecycle. A binary that is broken or has no panel fails the test.
func TestAdapterPanelIntegration(t *testing.T) {
	bin := os.Getenv("REX_AMQ_ADAPTER_BIN")
	if bin == "" {
		t.Skip("REX_AMQ_ADAPTER_BIN is not set; build rex-amq-adapter and point it there")
	}
	dir := t.TempDir()
	fakeBin, home, state := filepath.Join(dir, "bin"), filepath.Join(dir, "home"), filepath.Join(dir, "state")
	const session = "session:01a1e4aa-0000-7000-8000-0000000000f1"
	sessionDir := filepath.Join(state, "amq-root", "extensions", "rex-amq-adapter", "sessions", strings.ReplaceAll(session, ":", "_"))
	for _, d := range []string{fakeBin, home, sessionDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(fakeBin, "rex"), fakeRex, 0o755)
	write(filepath.Join(sessionDir, "crew.json"), `{"crew":"it-crew","claimed_by":"claude-1","claimed_at":"2026-11-16T11:50:00Z"}`, 0o644)
	write(filepath.Join(sessionDir, "lifecycle.json"), `{"session_id":"`+session+`","name":"rex-amq-ui-it","state":"opened","updated_at":"2026-11-16T11:50:00Z"}`, 0o644)

	s := &Stream{Bin: bin, Session: session, Env: []string{"PATH=" + fakeBin + ":/usr/bin:/bin", "HOME=" + home, "REX_AMQ_ADAPTER_HOME=" + state}}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan Update, 64)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, out) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the adapter was not stopped")
		}
	}()

	deadline := time.After(20 * time.Second)
	var st State
	next := func(what string) Frame {
		t.Helper()
		for {
			select {
			case u := <-out:
				switch u := u.(type) {
				case StatusUpdate:
					if u.Status == Reconnecting {
						t.Fatalf("the adapter run ended while waiting for %s: %v", what, u.Err)
					}
				case FrameUpdate:
					st = st.Apply(u.Frame)
					return u.Frame
				}
			case <-deadline:
				t.Fatalf("no %s from %s", what, bin)
			}
		}
	}

	if _, ok := next("snapshot").(Snapshot); !ok {
		t.Fatal("the first frame is not a snapshot")
	}
	p := st.Panel
	if st.Scope.SessionID != session || st.Scope.Server != "unix:///tmp/rex-amq-ui-it.sock" || p == nil || p.SessionLabel != "rex-amq-ui-it" ||
		p.Crew == nil || p.Crew.Crew != "it-crew" || p.View == nil || !p.View.NoView() || p.View.Lifecycle == nil || p.View.Lifecycle.Name != "rex-amq-ui-it" || p.Personal() {
		t.Fatalf("snapshot state: scope %+v panel %+v", st.Scope, p)
	}

	write(filepath.Join(sessionDir, "hand-back.json"), `{"by":"claude-1","at":"2026-11-16T12:10:00Z","body":"## Blocked on me\nNone","seq":1}`, 0o644)
	for {
		if _, ok := next("panel upsert").(PanelUpsert); ok {
			break
		}
	}
	if hb := st.Panel.View.HandBack; hb == nil || hb.By != "claude-1" || hb.Body != "## Blocked on me\nNone" {
		t.Fatalf("hand-back after the upsert: %+v", hb)
	}
}
