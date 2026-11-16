package ui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/est7/rex-amq-ui/internal/layer"
)

var update = flag.Bool("update", false, "rewrite testdata/golden from the current frames")

// TestGolden freezes each tab at both sizes, built from the s1 streams
// through the real reducer. The goldens hold the frame's text; colour and
// background are TestPaintNoHoles's and TestViewMapping's.
func TestGolden(t *testing.T) {
	tabs := []struct {
		name, fixture string
	}{
		{"overview", "edited-crew-attention"},
		{"members", "edited-crew-attention"},
		{"mail", "edited-crew-attention"},
		{"graph", "edited-crew-attention"},
		{"epics", "edited-personal-cockpit"},
	}
	for _, tab := range tabs {
		for _, size := range []layer.Size{layer.Drawer, layer.Board} {
			name := tab.name + "_" + strings.ToLower(size.String())
			t.Run(name, func(t *testing.T) {
				w, h := refSize(t, size)
				m := build(t, tab.fixture, w, h, Deps{}, Options{Tab: tab.name, Size: size})
				frame := m.Frame()
				if bad := holes(frame, w, h); bad != nil {
					t.Fatalf("holes: %v", bad)
				}
				got := plain(frame) + "\n"
				path := filepath.Join("..", "..", "testdata", "golden", name+".golden")
				if *update {
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%v (run go test ./internal/ui -run TestGolden -update)", err)
				}
				if got != string(want) {
					t.Errorf("frame differs from %s (rerun with -update if intended):\n--- got\n%s\n--- want\n%s", path, got, want)
				}
			})
		}
	}
}
