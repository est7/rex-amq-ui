package graph

import (
	"image/color"
	"strconv"
	"unicode/utf8"

	"github.com/est7/rex-amq-ui/internal/raster"
	"github.com/est7/rex-amq-ui/internal/theme"
)

// Palette is the colors a frame uses. Bg must be the panel's background so
// the image sits seamlessly in the cells around it.
type Palette struct {
	Bg, Surface, Border, Fg, Muted        color.NRGBA
	Drained, Queued, Alert, Obligation    color.NRGBA
	Working, Blocked, Idle, Error, Accent color.NRGBA
}

// FromTheme is the panel's palette in the graph's terms. The Graph tab's
// text legend uses the same theme colours for the same strokes: drained
// Green, obligation Yellow, queued Blue, bounced and dead letters Red.
func FromTheme(t theme.Palette) Palette {
	c := func(x color.Color) color.NRGBA { return color.NRGBAModel.Convert(x).(color.NRGBA) }
	return Palette{
		Bg: c(t.Bg), Surface: c(t.Surface), Border: c(t.Overlay), Fg: c(t.Fg), Muted: c(t.Muted),
		Drained: c(t.Green), Queued: c(t.Blue), Alert: c(t.Red), Obligation: c(t.Yellow),
		Working: c(t.Green), Blocked: c(t.Orange), Idle: c(t.Faint), Error: c(t.Red), Accent: c(t.Purple),
	}
}

func alpha(c color.NRGBA, a float64) color.NRGBA {
	c.A = uint8(min(max(a, 0), 1)*255 + 0.5)
	return c
}

// Paint draws s on c, which must be s.Size's pixels. Every pixel is
// painted, opaque.
func Paint(c *raster.Canvas, s Scene, p Palette) {
	c.Clear(p.Bg)
	if !s.Size.Cell.Valid() {
		return
	}
	cw, ch := float64(s.Size.Cell.CellW), float64(s.Size.Cell.CellH)
	for _, e := range s.Edges {
		paintEdge(c, e, p, cw, ch)
	}
	for _, pl := range s.Pulses {
		e := s.Edges[pl.Edge]
		at := e.Curve.At(e.T0 + (e.T1-e.T0)*pl.T)
		col := edgeColor(e.Style, p)
		r := e.Width*1.3 + cw*0.25
		c.Halo(raster.RectXYWH(at.X-r, at.Y-r, 2*r, 2*r), r, r*1.6, alpha(col, 0.45))
		c.Disc(at, r, p.Fg)
	}
	k := textScale(s.Nodes, cw)
	for _, n := range s.Nodes {
		paintNode(c, n, p, cw, ch, k)
	}
	// Counts go over the badges: a count hidden under a badge it passes
	// would read as a missing edge.
	for _, e := range s.Edges {
		paintCount(c, e, p, cw, ch)
	}
}

func edgeColor(s EdgeStyle, p Palette) color.NRGBA {
	switch s {
	case StyleQueued:
		return p.Queued
	case StyleAlert:
		return p.Alert
	case StyleObligation:
		return p.Obligation
	default:
		return p.Drained
	}
}

func edgeDash(s EdgeStyle, cw float64) raster.Dash {
	if s == StyleQueued || s == StyleAlert {
		return raster.Dash{On: cw * 1.1, Off: cw * 0.75}
	}
	return raster.Dash{}
}

func paintEdge(c *raster.Canvas, e Edge, p Palette, cw, ch float64) {
	if e.T1 <= e.T0 {
		return
	}
	col := edgeColor(e.Style, p)
	path := e.Curve.Flatten(e.T0, e.T1, cw/2)
	head := max(ch*0.42, e.Width*3)
	body := raster.TrimEnd(path, head*0.7)
	if e.Style == StyleObligation {
		c.Stroke(body, e.Width*3.2, alpha(col, 0.22), raster.Dash{})
	}
	c.Stroke(body, e.Width, col, edgeDash(e.Style, cw))
	tip := path[len(path)-1]
	c.Arrow(tip, e.Curve.Tangent(e.T1), head, head*0.42, col)
}

// paintCount labels an edge with its message count (or "!" for an open
// obligation whose opener is not in the mail) on a pill a third of the way
// from its sender, so a request's count and its reply's do not stack.
func paintCount(c *raster.Canvas, e Edge, p Palette, cw, ch float64) {
	if e.T1 <= e.T0 {
		return
	}
	label := strconv.Itoa(e.Count)
	if e.Count == 0 {
		label = "!"
	}
	f := c.CellFace(0.85, true)
	at := e.Curve.At(e.T0 + (e.T1-e.T0)*0.33)
	w, h := f.Measure(label)+cw*1.1, ch*0.78
	r := raster.RectXYWH(at.X-w/2, at.Y-h/2, w, h)
	col := edgeColor(e.Style, p)
	c.FillRoundRect(r, h/2, p.Bg)
	c.StrokeRoundRect(r, h/2, max(1, cw*0.12), col)
	c.TextCentered(at, label, f, col)
}

func activityColor(a string, p Palette) color.NRGBA {
	switch a {
	case "working":
		return p.Working
	case "blocked":
		return p.Blocked
	case "error":
		return p.Error
	default:
		return p.Idle
	}
}

// textScale is the badge text size, as a fraction of the terminal's, at
// which the longest label fits its badge: 1 when there is room, never
// below 0.7 (past that, labels are cut with an ellipsis instead). The 3%
// margin absorbs the font size rounding to whole pixels. One
// scale for every badge keeps them alike.
func textScale(nodes []Node, cw float64) float64 {
	k := 1.0
	for _, n := range nodes {
		if n.Kind == KindMore {
			continue
		}
		if runes := utf8.RuneCountInString(n.Label); runes > 0 {
			k = min(k, 0.97*badgeInner(n.Rect, cw)/(float64(runes)*cw))
		}
	}
	return max(k, 0.7)
}

// badgeInner is the width a badge leaves for its text, past the status dot.
func badgeInner(r raster.Rect, cw float64) float64 { return r.W() - cw*3 }

func paintNode(c *raster.Canvas, n Node, p Palette, cw, ch, k float64) {
	r := n.Rect
	radius := ch * 0.45
	border, fill, text, sub := p.Border, p.Surface, p.Fg, p.Muted
	dot := activityColor(n.Activity, p)
	switch n.Kind {
	case KindAgent:
		border = activityColor(n.Activity, p)
		if n.Activity == "" || n.Activity == "idle" || n.Activity == "absent" || n.Activity == "done" {
			border = p.Border
		}
	case KindRetired:
		fill, text, dot = p.Bg, p.Muted, p.Border
	case KindMore:
		fill = p.Bg
	}
	if n.Halo > 0 {
		c.Halo(r, radius, ch*0.75, alpha(p.Working, n.Halo))
	}
	if n.Selected {
		c.Halo(r, radius, ch*0.3, alpha(p.Accent, 0.55))
		border = p.Accent
	}
	c.FillRoundRect(r, radius, fill)
	bw := max(1, cw*0.16)
	if n.Selected {
		bw *= 1.8
	}
	c.StrokeRoundRect(r, radius, bw, border)

	label, role := c.CellFace(k, true), c.CellFace(0.85*k, false)
	if n.Kind == KindMore {
		c.TextCentered(raster.Point{X: r.Center().X, Y: r.Min.Y + r.H()*0.4}, n.Label, label, text)
		c.TextCentered(raster.Point{X: r.Center().X, Y: r.Min.Y + r.H()*0.72}, n.Role, role, sub)
		return
	}
	x := r.Min.X + cw*2.2
	inner := badgeInner(r, cw)
	c.Disc(raster.Point{X: r.Min.X + cw*1.15, Y: r.Min.Y + r.H()*0.4}, cw*0.42, dot)
	c.Text(raster.Point{X: x, Y: r.Min.Y + r.H()*0.4 + label.Ascent*0.38}, label.Fit(n.Label, inner), label, text)
	c.Text(raster.Point{X: x, Y: r.Min.Y + r.H()*0.74 + role.Ascent*0.38}, role.Fit(n.Role, inner), role, sub)
}
