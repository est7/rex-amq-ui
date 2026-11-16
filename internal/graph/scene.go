package graph

import (
	"cmp"
	"math"
	"time"
	"unicode/utf8"

	"github.com/est7/rex-amq-ui/internal/adapter"
	"github.com/est7/rex-amq-ui/internal/raster"
)

// Size is the Graph region: its cells and the pixel size of one cell.
type Size struct {
	Cols, Rows int
	Cell       raster.Metrics
}

// Status is what the replay bar says.
type Status struct {
	Shown, Total int
	Live         bool
	HideRetired  bool
	// Span is the time from the first message to the last one shown.
	Span time.Duration
	// Current is the last message shown; nil when none is.
	Current *Summary
}

// Summary is one message as the replay bar names it.
type Summary struct {
	From    string
	To      []string
	Subject string
	// State is drained, queued, sent, bounced, or dead_lettered.
	State string
	At    time.Time
}

type NodeKind int

const (
	KindAgent NodeKind = iota
	KindRetired
	// KindMore is the "+N" badge standing for every agent not drawn.
	KindMore
)

type Node struct {
	ID       string
	Label    string
	Role     string
	Kind     NodeKind
	Activity string
	Selected bool
	Rect     raster.Rect
	// Halo is the breathing glow's strength in [0,1]; 0 draws none.
	Halo float64
}

type EdgeStyle int

const (
	StyleDrained EdgeStyle = iota
	StyleQueued
	StyleAlert
	StyleObligation
)

type Edge struct {
	From, To string
	Count    int
	Style    EdgeStyle
	Width    float64
	// Curve runs center to center; T0..T1 is the part outside the badges,
	// with the arrow's tip at T1.
	Curve  raster.Quad
	T0, T1 float64
}

type Pulse struct {
	Edge int
	// T is the pulse's place along the visible part of the edge, 0..1.
	T float64
}

type Scene struct {
	Size   Size
	Nodes  []Node
	Edges  []Edge
	Pulses []Pulse
	Status Status
	// Graph is the area the nodes are placed in, between the replay bar
	// and the legend.
	Graph raster.Rect
}

// Pixels is the region's size in pixels.
func (sz Size) Pixels() (w, h int) { return sz.Cols * sz.Cell.CellW, sz.Rows * sz.Cell.CellH }

// Bands of the frame, in cells: the replay bar on top, the legend and the
// current message's summary below.
const (
	topBand    = 1.6
	bottomBand = 2.7
)

// Scene places the model at sz: nodes around an ellipse filling the graph
// area (the lead at the top, then clockwise), edges as curves that bow to
// the right of travel so a reply never covers its request, each clipped to
// the gap between the two badges. All geometry scales from the cell size.
func (m *Model) Scene(sz Size) Scene {
	s := Scene{Size: sz, Status: m.Status()}
	if !sz.Cell.Valid() || sz.Cols <= 0 || sz.Rows <= 0 {
		return s
	}
	cw, ch := float64(sz.Cell.CellW), float64(sz.Cell.CellH)
	w, h := sz.Pixels()
	s.Graph = raster.Rect{Min: raster.Point{X: cw, Y: topBand * ch}, Max: raster.Point{X: float64(w) - cw, Y: float64(h) - bottomBand*ch}}
	if !m.ready {
		return s
	}
	now := m.clock()
	c := m.cast()

	s.Nodes = m.nodes(c, now)
	place(s.Nodes, s.Graph, cw, ch)
	rect := map[string]raster.Rect{}
	for _, n := range s.Nodes {
		rect[n.ID] = n.Rect
	}

	aggs, keys := m.edges(c)
	at := map[edgeKey]int{}
	base := max(1.2, cw*0.2)
	for _, k := range keys {
		a := aggs[k]
		e := Edge{From: k.from, To: k.to, Count: a.count, Style: a.style, Width: edgeWidth(base, a.count, a.style)}
		e.Curve, e.T0, e.T1 = route(rect[k.from], rect[k.to], cw*0.5)
		at[k] = len(s.Edges)
		s.Edges = append(s.Edges, e)
	}
	for _, p := range m.pulses {
		if !m.drawn(p, now) {
			continue
		}
		for _, k := range m.pulseEdges(c, p.msg) {
			if i, ok := at[k]; ok {
				s.Pulses = append(s.Pulses, Pulse{Edge: i, T: float64(now.Sub(p.at)) / float64(pulseLife)})
			}
		}
	}
	return s
}

func (m *Model) nodes(c cast, now time.Time) []Node {
	phase := float64(now.Sub(m.epoch)%breathPeriod) / float64(breathPeriod)
	breath := 0.35 + 0.45*(0.5-0.5*math.Cos(2*math.Pi*phase))
	var out []Node
	for _, id := range c.order {
		a, isAgent := c.agents[id]
		n := Node{ID: id, Label: id, Selected: id == m.selected}
		switch {
		case !isAgent:
			n.Kind, n.Role = KindMore, "others"
		case c.retired[id]:
			n.Kind, n.Role, n.Activity = KindRetired, "retired "+roleOf(a), a.Activity.State
		default:
			n.Kind, n.Role, n.Activity = KindAgent, roleOf(a), a.Activity.State
			if breathes(a) {
				n.Halo = breath
			}
		}
		out = append(out, n)
	}
	return out
}

func roleOf(a adapter.Agent) string {
	return cmp.Or(a.Role, a.Kind, "agent")
}

// place sizes every badge alike (wide enough for the longest label within
// limits) and centers them on an ellipse inside area.
func place(nodes []Node, area raster.Rect, cw, ch float64) {
	if len(nodes) == 0 {
		return
	}
	runes := 0
	for _, n := range nodes {
		runes = max(runes, utf8.RuneCountInString(n.Label), utf8.RuneCountInString(n.Role))
	}
	bw := min(max(float64(runes+4)*cw, 10*cw), max(area.W()/3.2, 8*cw), 24*cw)
	bh := 2.5 * ch
	rx := max(area.W()/2-bw/2-cw, 0)
	ry := max(area.H()/2-bh/2-ch*0.3, 0)
	c := area.Center()
	start := -math.Pi / 2
	if len(nodes) == 2 {
		start = math.Pi
	}
	for i := range nodes {
		p := c
		if len(nodes) > 1 {
			a := start + 2*math.Pi*float64(i)/float64(len(nodes))
			p = raster.Point{X: c.X + rx*math.Cos(a), Y: c.Y + ry*math.Sin(a)}
		}
		nodes[i].Rect = raster.RectXYWH(p.X-bw/2, p.Y-bh/2, bw, bh)
	}
}

// route is the curve from a's center to b's, bowed right of travel by a
// sixth of its length, and the span of it outside both badges (grown by
// gap so the arrow stops short of b).
func route(a, b raster.Rect, gap float64) (raster.Quad, float64, float64) {
	p0, p2 := a.Center(), b.Center()
	chord := p2.Sub(p0)
	ctrl := p0.Lerp(p2, 0.5).Add(chord.Perp().Unit().Mul(chord.Len() / 6))
	q := raster.Quad{P0: p0, P1: ctrl, P2: p2}
	ga, gb := a.Inset(-gap*0.5), b.Inset(-gap)
	t0 := boundary(q, ga, 0, 0.5, true)
	t1 := boundary(q, gb, 0.5, 1, false)
	return q, t0, t1
}

// boundary finds where the curve crosses r's edge between lo and hi by
// bisection: leaving r when leaving is true, entering it otherwise.
func boundary(q raster.Quad, r raster.Rect, lo, hi float64, leaving bool) float64 {
	for range 24 {
		mid := (lo + hi) / 2
		if r.Contains(q.At(mid)) == leaving {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// edgeWidth grows with the log of the message count, capped, and an open
// obligation is drawn heavier than any plain edge of its weight.
func edgeWidth(base float64, count int, s EdgeStyle) float64 {
	w := base * min(1+0.55*math.Log2(float64(max(count, 1))), 2.6)
	if s == StyleObligation {
		w = max(w*1.5, base*1.9)
	}
	return w
}
