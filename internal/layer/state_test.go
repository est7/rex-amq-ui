package layer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/est7/rex-amq-ui/internal/rexctl"
	"github.com/est7/rex-amq-ui/internal/rexctl/rexctltest"
)

func TestSizePersistenceRoundTripAndFallback(t *testing.T) {
	if sz, ok := LoadSize(""); sz != Drawer || ok {
		t.Fatalf("empty cacheDir: got (%v, %v), want (Drawer, false)", sz, ok)
	}
	if err := SaveSize("", Board); err != nil {
		t.Fatalf("SaveSize empty cacheDir: %v", err)
	}

	dir := t.TempDir()
	if sz, ok := LoadSize(dir); sz != Drawer || ok {
		t.Fatalf("missing file: got (%v, %v), want (Drawer, false)", sz, ok)
	}

	for _, want := range []Size{Board, Drawer, Board} {
		if err := SaveSize(dir, want); err != nil {
			t.Fatalf("SaveSize(%v): %v", want, err)
		}
		got, ok := LoadSize(dir)
		if !ok || got != want {
			t.Fatalf("LoadSize after SaveSize(%v): got (%v, %v)", want, got, ok)
		}
		raw, err := os.ReadFile(StatePath(dir))
		if err != nil {
			t.Fatalf("read state.json: %v", err)
		}
		var ps map[string]string
		if err := json.Unmarshal(raw, &ps); err != nil {
			t.Fatalf("unmarshal state.json %q: %v", raw, err)
		}
		wantStr := "drawer"
		if want == Board {
			wantStr = "board"
		}
		if ps["size"] != wantStr {
			t.Fatalf("state.json size = %q, want %q", ps["size"], wantStr)
		}
		entries, err := os.ReadDir(filepath.Dir(StatePath(dir)))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != stateFile {
			t.Fatalf("unexpected files in state dir: %v", entries)
		}
	}

	// Tolerant reader keeps unknown fields.
	if err := os.WriteFile(StatePath(dir), []byte(`{"size":"board","future":123}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := LoadSize(dir); !ok || got != Board {
		t.Fatalf("tolerant read: got (%v, %v), want (Board, true)", got, ok)
	}

	// Corrupt JSON and unknown size values fall back to Drawer, false.
	for _, bad := range []string{"{not json", `{"size":"huge"}`, `{}`, ""} {
		if err := os.WriteFile(StatePath(dir), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, ok := LoadSize(dir); got != Drawer || ok {
			t.Errorf("bad input %q: got (%v, %v), want (Drawer, false)", bad, got, ok)
		}
	}
}

func TestOpenLaunchSizeCreatesAtPersistedSizeWithoutResizingRestored(t *testing.T) {
	t.Run("new layer uses Launch.Size", func(t *testing.T) {
		s := rexctltest.NewServer(t)
		halfWindow(s)
		s.SetView(view(3, paneA, false, "", "", rexctl.Bounds{}))
		o, err := Open(context.Background(), dial(t, s), Launch{
			Session: sess, Size: Board, Command: []string{"/bin/rex-amq-ui"},
		})
		if err != nil || o.Restored {
			t.Fatalf("Open: %+v %v", o, err)
		}
		r, ok := s.Last("session.new_layer")
		if !ok {
			t.Fatal("expected session.new_layer")
		}
		var p struct {
			Bounds rexctl.Bounds `json:"bounds"`
			Layout struct {
				Block struct {
					Options struct {
						Command []string `json:"command"`
					} `json:"options"`
				} `json:"block"`
			} `json:"layout"`
		}
		if err := r.Decode(&p); err != nil {
			t.Fatal(err)
		}
		if p.Bounds != Board.Bounds() {
			t.Errorf("bounds = %v, want Board %v", p.Bounds, Board.Bounds())
		}
		if !slices.Contains(p.Layout.Block.Options.Command, "--board") {
			t.Errorf("argv %v missing --board", p.Layout.Block.Options.Command)
		}
	})

	t.Run("restored layer ignores Launch.Size when Board is false", func(t *testing.T) {
		s := rexctltest.NewServer(t)
		halfWindow(s)
		s.SetView(view(4, paneA, true, rexctl.LayerMinimized, "client:app", Drawer.Bounds()))
		o, err := Open(context.Background(), dial(t, s), Launch{
			Session: sess, Size: Board, Command: []string{"/bin/rex-amq-ui"},
		})
		if err != nil || !o.Restored {
			t.Fatalf("Open: %+v %v", o, err)
		}
		if got, want := methods(s), []string{"session.attach", "session.set_layer_state", "block.method"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("calls %v, want %v (must not resize restored layer)", got, want)
		}
	})
}
