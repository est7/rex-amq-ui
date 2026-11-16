package graph

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/est7/rex-amq-ui/internal/raster"
)

// Palette is the colors a frame uses. Bg must be the panel's background so
// the image sits seamlessly in the cells around it.
type Palette struct {
	Bg, Surface, Border, Fg, Muted        color.NRGBA
	Drained, Queued, Alert, Obligation    color.NRGBA
	Working, Blocked, Idle, Error, Accent color.NRGBA
}

func rgb(v uint32) color.NRGBA {
	return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 0xff}
}

// Everforest is the dark Everforest palette, the panel's fallback theme.
func Everforest() Palette {
	return Palette{
		Bg: rgb(0x2d353b), Surface: rgb(0x3d484d), Border: rgb(0x56635f), Fg: rgb(0xd3c6aa), Muted: rgb(0x859289),
		Drained: rgb(0x9da9a0), Queued: rgb(0x7fbbb3), Alert: rgb(0xe67e80), Obligation: rgb(0xdbbc7f),
		Working: rgb(0xa7c080), Blocked: rgb(0xe69875), Idle: rgb(0x7a8478), Error: rgb(0xe67e80), Accent: rgb(0xd699b6),
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
	paintBar(c, s, p, cw, ch)
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
	paintLegend(c, s, p, cw, ch)
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

// paintBar is the replay bar: position, span and mode on the left, the
// keys on the right.
func paintBar(c *raster.Canvas, s Scene, p Palette, cw, ch float64) {
	st := s.Status
	f, b := c.CellFace(1, false), c.CellFace(1, true)
	y := ch * 0.8
	x := cw
	// The mode icon is drawn, not a glyph: Go Mono has no pause sign.
	mode, modeCol := "Live", p.Working
	icon := raster.Point{X: x + cw*0.5, Y: y}
	if st.Live {
		c.Disc(icon, cw*0.42, modeCol)
	} else {
		mode, modeCol = "Replay", p.Obligation
		bar := raster.RectXYWH(icon.X-cw*0.42, icon.Y-ch*0.22, cw*0.28, ch*0.44)
		c.FillRect(bar, modeCol)
		bar.Min.X, bar.Max.X = bar.Min.X+cw*0.56, bar.Max.X+cw*0.56
		c.FillRect(bar, modeCol)
	}
	x += cw * 1.5
	c.Text(raster.Point{X: x, Y: y + b.Ascent*0.38}, mode, b, modeCol)
	x += b.Measure(mode) + cw*2
	pos := fmt.Sprintf("%d / %d messages", st.Shown, st.Total)
	if st.Total > 0 {
		pos += " · " + span(st.Span)
	}
	c.Text(raster.Point{X: x, Y: y + f.Ascent*0.38}, pos, f, p.Fg)
	x += f.Measure(pos) + cw*2
	if st.HideRetired {
		c.Text(raster.Point{X: x, Y: y + f.Ascent*0.38}, "retired hidden", f, p.Muted)
	}
	// The key hints shrink, then go, when the status needs the room.
	w, _ := s.Size.Pixels()
	for _, keys := range []string{"[ prev  ] next  L live  h retired", "[ ] L h"} {
		if kx := float64(w) - cw - f.Measure(keys); kx >= x {
			c.Text(raster.Point{X: kx, Y: y + f.Ascent*0.38}, keys, f, p.Muted)
			break
		}
	}
}

// paintLegend shows each stroke style as drawn, then the current message.
func paintLegend(c *raster.Canvas, s Scene, p Palette, cw, ch float64) {
	w, h := s.Size.Pixels()
	y := float64(h) - ch*1.9
	base := max(1.2, cw*0.2)
	// The full legend, or in a narrow drawer a shorter, then smaller one.
	for _, v := range []legend{
		{text: 1, sample: 4, gap: 2.5, names: [4]string{"drained", "open obligation", "queued", "bounced / dead letter"}},
		{text: 1, sample: 2.5, gap: 1.5, names: [4]string{"drained", "obligation", "queued", "bounced"}},
		{text: 0.7, sample: 1.7, gap: 0.8, names: [4]string{"drained", "obligation", "queued", "bounced"}},
	} {
		if f := c.CellFace(v.text, false); v.width(f, cw) <= float64(w)-2*cw {
			v.paint(c, f, p, cw, y, base)
			break
		}
	}

	cur := s.Status.Current
	if cur == nil {
		return
	}
	f := c.CellFace(1, false)
	y = float64(h) - ch*0.7
	line := fmt.Sprintf("%s  %s → %s  %s  ", cur.At.Format(time.TimeOnly), cur.From, strings.Join(cur.To, ", "), cur.State)
	c.Text(raster.Point{X: cw, Y: y + f.Ascent*0.38}, f.Fit(line, float64(w)-2*cw), f, p.Fg)
	if rest := float64(w) - 2*cw - f.Measure(line); rest > cw*4 {
		c.Text(raster.Point{X: cw + f.Measure(line), Y: y + f.Ascent*0.38}, f.Fit(cur.Subject, rest), f, p.Muted)
	}
}

// span is a duration as the replay bar shows it: 38s, 14m, 2h05m.
func span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

var legendStyles = [4]EdgeStyle{StyleDrained, StyleObligation, StyleQueued, StyleAlert}

// legend is one form of the legend line: text at that fraction of the
// terminal's size, stroke samples sample cells long, gap cells between
// entries.
type legend struct {
	text, sample, gap float64
	names             [4]string
}

func (l legend) width(f *raster.Face, cw float64) float64 {
	w := 0.0
	for _, n := range l.names {
		w += cw*(l.sample+1+l.gap) + f.Measure(n)
	}
	return w - cw*l.gap
}

func (l legend) paint(c *raster.Canvas, f *raster.Face, p Palette, cw, y, base float64) {
	x := cw
	for i, style := range legendStyles {
		col := edgeColor(style, p)
		w := edgeWidth(base, 1, style)
		seg := []raster.Point{{X: x, Y: y}, {X: x + cw*l.sample, Y: y}}
		if style == StyleObligation {
			c.Stroke(seg, w*3.2, alpha(col, 0.22), raster.Dash{})
		}
		dash := edgeDash(style, cw)
		if dash != (raster.Dash{}) {
			on := cw * min(0.8, l.sample/3.2)
			dash = raster.Dash{On: on, Off: on * 0.7}
		}
		c.Stroke(seg, w, col, dash)
		x += cw * (l.sample + 1)
		c.Text(raster.Point{X: x, Y: y + f.Ascent*0.38}, l.names[i], f, p.Muted)
		x += f.Measure(l.names[i]) + cw*l.gap
	}
}
