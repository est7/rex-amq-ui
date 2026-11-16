package graph_test

import (
	"fmt"
	"os"
	"testing"
	"time"
	"unicode"

	"github.com/est7/rex-amq-ui/internal/adapter"
	"github.com/est7/rex-amq-ui/internal/graph"
	"github.com/est7/rex-amq-ui/internal/raster"
	"github.com/est7/rex-amq-ui/internal/raster/rastertest"
)

// crew is a six-member crew with a departed implementer and the human
// writing in from outside: every edge style, a retired node, the "+N"
// node, working halos, and an open obligation.
var _ = rastertest.UpdateFlag()

func crew() (agents []adapter.Agent, msgs []adapter.Message, obs []adapter.Obligation) {
	agents = []adapter.Agent{
		agent("claude-98", "orchestrator", "working"),
		agent("claude-105", "implementer", "working"),
		agent("claude-106", "implementer", "idle"),
		agent("codex-33", "reviewer", "blocked"),
		agent("pi-7", "writer", "idle"),
		agent("claude-90", "narrator", "idle"),
		retired("claude-99", "implementer", t0.Add(30*time.Minute)),
	}
	add := func(id string, min int, from, to, state string) {
		msgs = append(msgs, msg(id, min, from, []string{to}, state))
	}
	add("m01", 0, "est9", "claude-98", adapter.MsgDrained)
	add("m02", 2, "claude-98", "claude-99", adapter.MsgDrained)
	add("m03", 3, "claude-98", "claude-99", adapter.MsgDrained)
	add("m04", 20, "claude-99", "claude-98", adapter.MsgDrained)
	add("m05", 31, "claude-98", "claude-105", adapter.MsgDrained)
	add("m06", 32, "claude-98", "claude-106", adapter.MsgDrained)
	add("m07", 34, "claude-98", "pi-7", adapter.MsgDrained)
	add("m08", 40, "claude-105", "claude-98", adapter.MsgDrained)
	add("m09", 41, "claude-98", "codex-33", adapter.MsgDrained)
	add("m10", 44, "codex-33", "claude-98", adapter.MsgDrained)
	add("m11", 45, "claude-98", "claude-105", adapter.MsgDrained)
	add("m12", 47, "claude-98", "claude-105", adapter.MsgDrained)
	add("m13", 50, "claude-106", "claude-98", adapter.MsgDrained)
	add("m14", 52, "claude-98", "codex-33", adapter.MsgDrained) // opens the review obligation
	add("m15", 53, "claude-90", "claude-98", adapter.MsgQueued)
	add("m16", 54, "pi-7", "claude-99", adapter.MsgQueued)
	add("m17", 55, "claude-98", "claude-106", adapter.MsgDeadLettered)
	add("m18", 56, "claude-105", "claude-98", adapter.MsgSent)
	b := msg("m19", 57, "pi-7", []string{"claude-98"}, adapter.MsgQueued)
	b.Bounced = true
	msgs = append(msgs, b)
	obs = []adapter.Obligation{{Ref: "m14", Kind: "request", Subject: "review s1", OwedBy: "codex-33", OwedTo: "claude-98", State: "open"}}
	for i := range msgs {
		msgs[i].Subject = []string{"rex-amq-ui s3 kitty-graph+replay", "review s1 control stream", "hand-back: s1 green", "status: phase A"}[i%4]
	}
	return agents, msgs, obs
}

func crewModel() (*graph.Model, *clock) {
	agents, msgs, obs := crew()
	m, c := newModel()
	m.Update(state(agents, msgs[:len(msgs)-1], obs...))
	m.Select("claude-106")
	c.Advance(time.Second)
	// The last message arrives live and is half way along its edge.
	m.Update(state(agents, msgs, obs...))
	c.Advance(500 * time.Millisecond)
	return m, c
}

var frameSizes = []struct {
	name string
	size graph.Size
}{
	// Board (88% of a window) and Drawer (35%) at the two cell sizes
	// observed on live Rex.
	{"board-9x22", graph.Size{Cols: 150, Rows: 34, Cell: raster.Metrics{CellW: 9, CellH: 22}}},
	{"board-23x49", graph.Size{Cols: 98, Rows: 28, Cell: raster.Metrics{CellW: 23, CellH: 49}}},
	{"drawer-9x22", graph.Size{Cols: 56, Rows: 38, Cell: raster.Metrics{CellW: 9, CellH: 22}}},
	{"drawer-23x49", graph.Size{Cols: 38, Rows: 30, Cell: raster.Metrics{CellW: 23, CellH: 49}}},
}

func render(m *graph.Model, sz graph.Size) *raster.Canvas {
	w, h := sz.Pixels()
	c := raster.NewCanvas(w, h, sz.Cell)
	graph.Paint(c, m.Scene(sz), graph.Everforest())
	return c
}

// TestReplayReferenceFrames renders the crew live (halos on, a pulse under
// way) and mid-replay, and compares with the committed frames under
// testdata/graph; -update rewrites them.
func TestReplayReferenceFrames(t *testing.T) {
	for _, fs := range frameSizes {
		t.Run("live-"+fs.name, func(t *testing.T) {
			m, _ := crewModel()
			rastertest.Golden(t, fmt.Sprintf("../../testdata/graph/live-%s.png", fs.name), render(m, fs.size).Image())
		})
	}
	t.Run("replay-board-9x22", func(t *testing.T) {
		m, c := crewModel()
		for range 8 {
			m.Prev()
		}
		c.Advance(400 * time.Millisecond)
		if st := m.Status(); st.Live || st.Shown != 11 || st.Total != 19 {
			t.Fatalf("status %+v, want 11/19 in replay", st)
		}
		rastertest.Golden(t, "../../testdata/graph/replay-board-9x22.png", render(m, frameSizes[0].size).Image())
	})
}

func overlap(a, b raster.Rect) bool {
	return a.Min.X < b.Max.X && b.Min.X < a.Max.X && a.Min.Y < b.Max.Y && b.Min.Y < a.Max.Y
}

func TestSceneGeometry(t *testing.T) {
	for _, fs := range frameSizes {
		t.Run(fs.name, func(t *testing.T) {
			m, _ := crewModel()
			s := m.Scene(fs.size)
			w, h := fs.size.Pixels()
			canvas := raster.Rect{Max: raster.Point{X: float64(w), Y: float64(h)}}
			if len(s.Nodes) != 8 {
				t.Fatalf("nodes %v, want 6 active, 1 retired, +1", nodeIDs(s))
			}
			for i, a := range s.Nodes {
				if a.Rect.Min.X < s.Graph.Min.X || a.Rect.Min.Y < s.Graph.Min.Y || a.Rect.Max.X > s.Graph.Max.X || a.Rect.Max.Y > s.Graph.Max.Y {
					t.Errorf("node %s at %v leaves the graph area %v", a.ID, a.Rect, s.Graph)
				}
				for _, b := range s.Nodes[i+1:] {
					if overlap(a.Rect, b.Rect) {
						t.Errorf("nodes %s %v and %s %v overlap", a.ID, a.Rect, b.ID, b.Rect)
					}
				}
			}
			if !overlap(s.Graph, canvas) {
				t.Fatalf("graph area %v outside the canvas", s.Graph)
			}
			rects := map[string]raster.Rect{}
			for _, n := range s.Nodes {
				rects[n.ID] = n.Rect
			}
			for _, e := range s.Edges {
				if e.T0 >= e.T1 {
					t.Errorf("%s->%s has no visible part (T0 %.3f, T1 %.3f)", e.From, e.To, e.T0, e.T1)
					continue
				}
				if rects[e.From].Contains(e.Curve.At(e.T0)) || rects[e.To].Contains(e.Curve.At(e.T1)) {
					t.Errorf("%s->%s starts or ends inside a badge", e.From, e.To)
				}
				for k := 0; k <= 64; k++ {
					p := e.Curve.At(e.T0 + (e.T1-e.T0)*float64(k)/64)
					for _, n := range s.Nodes {
						if n.ID != e.From && n.ID != e.To && n.Rect.Contains(p) {
							t.Errorf("%s->%s runs behind %s's badge at %v", e.From, e.To, n.ID, p)
							k = 64
							break
						}
					}
				}
				if back, ok := edge(s, e.To, e.From); ok {
					a, b := e.Curve.At(0.5), back.Curve.At(0.5)
					if d, gap := a.Sub(b).Len(), (e.Width+back.Width)/2+float64(fs.size.Cell.CellW)/2; d < gap {
						t.Errorf("%s<->%s strokes %.1fpx apart, want %.1f: the reply covers the request", e.From, e.To, d, gap)
					}
				}
			}
		})
	}
}

func TestScenePaintsEveryPixelOpaque(t *testing.T) {
	m, _ := crewModel()
	c := render(m, frameSizes[2].size)
	for i := 3; i < len(c.Image().Pix); i += 4 {
		if c.Image().Pix[i] != 0xff {
			t.Fatalf("pixel %d is not opaque", i/4)
		}
	}
}

func BenchmarkGraphFrame(b *testing.B) {
	for _, fs := range frameSizes[:2] {
		b.Run(fs.name, func(b *testing.B) {
			m, _ := crewModel()
			w, h := fs.size.Pixels()
			c := raster.NewCanvas(w, h, fs.size.Cell)
			pal := graph.Everforest()
			for b.Loop() {
				graph.Paint(c, m.Scene(fs.size), pal)
			}
		})
	}
}

// TestSceneGlyphsInFont guards the frame's own text: every non-ASCII rune
// the painter writes must exist in Go Mono, or it draws as a box.
func TestSceneGlyphsInFont(t *testing.T) {
	src, err := os.ReadFile("paint.go")
	if err != nil {
		t.Fatal(err)
	}
	f := raster.NewCanvas(1, 1, raster.Metrics{CellW: 9, CellH: 22}).CellFace(1, false)
	for _, r := range string(src) {
		if r > unicode.MaxASCII && !f.Has(r) {
			t.Errorf("paint.go writes %q (U+%04X), which Go Mono does not have", r, r)
		}
	}
	if f.Has('◆') {
		t.Fatalf("negative control: Go Mono should lack U+25C6")
	}
}
