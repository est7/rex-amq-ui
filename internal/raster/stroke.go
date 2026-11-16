package raster

import (
	"image"
	"image/color"
	"math"
)

// Quad is a quadratic Bézier curve from P0 to P2 bent toward P1.
type Quad struct{ P0, P1, P2 Point }

// At is the curve's point at t in [0,1].
func (q Quad) At(t float64) Point {
	u := 1 - t
	return q.P0.Mul(u * u).Add(q.P1.Mul(2 * u * t)).Add(q.P2.Mul(t * t))
}

// Tangent is the curve's direction of travel at t (not normalized).
func (q Quad) Tangent(t float64) Point {
	return q.P1.Sub(q.P0).Mul(2 * (1 - t)).Add(q.P2.Sub(q.P1).Mul(2 * t))
}

// Flatten is the curve between t0 and t1 as a polyline whose segments are
// at most about step pixels long.
func (q Quad) Flatten(t0, t1, step float64) []Point {
	approx := q.At(t0).Sub(q.At((t0+t1)/2)).Len() + q.At((t0+t1)/2).Sub(q.At(t1)).Len()
	n := max(2, int(math.Ceil(approx/max(step, 0.5))))
	pts := make([]Point, n+1)
	for i := range pts {
		pts[i] = q.At(t0 + (t1-t0)*float64(i)/float64(n))
	}
	return pts
}

// Dash is a stroke's dash pattern in pixels; the zero Dash is solid.
type Dash struct{ On, Off float64 }

func (d Dash) solid() bool { return d.On <= 0 || d.Off <= 0 }

// Stroke paints the polyline path width pixels wide with round caps and
// joins, solid or dashed. A dash is a run of the path On pixels long, each
// with its own round caps, then a gap Off long.
func (c *Canvas) Stroke(path []Point, width float64, col color.NRGBA, dash Dash) {
	if len(path) < 2 || width <= 0 {
		return
	}
	var box image.Rectangle
	for _, piece := range dashes(path, dash) {
		for i := 1; i < len(piece); i++ {
			box = box.Union(c.capsule(piece[i-1], piece[i], width/2))
		}
	}
	c.composite(box, col)
}

// PathLength is the length of a polyline in pixels.
func PathLength(path []Point) float64 {
	l := 0.0
	for i := 1; i < len(path); i++ {
		l += path[i].Sub(path[i-1]).Len()
	}
	return l
}

// TrimEnd is path with its last d pixels of length removed.
func TrimEnd(path []Point, d float64) []Point {
	keep := PathLength(path) - d
	if keep <= 0 {
		return nil
	}
	return cut(path, 0, keep)
}

// dashes splits path into the runs a dash pattern draws.
func dashes(path []Point, d Dash) [][]Point {
	if d.solid() {
		return [][]Point{path}
	}
	total := PathLength(path)
	var out [][]Point
	for s := 0.0; s < total; s += d.On + d.Off {
		if piece := cut(path, s, min(s+d.On, total)); len(piece) >= 2 {
			out = append(out, piece)
		}
	}
	return out
}

// cut is the part of path between arc lengths from and to.
func cut(path []Point, from, to float64) []Point {
	var out []Point
	walked := 0.0
	for i := 1; i < len(path); i++ {
		a, b := path[i-1], path[i]
		l := b.Sub(a).Len()
		if l == 0 {
			continue
		}
		s0, s1 := walked, walked+l
		walked = s1
		if s1 < from || s0 > to {
			continue
		}
		lo, hi := max(from, s0), min(to, s1)
		if len(out) == 0 {
			out = append(out, a.Lerp(b, (lo-s0)/l))
		}
		out = append(out, a.Lerp(b, (hi-s0)/l))
	}
	return out
}

// capsule max-combines the coverage of the segment a-b, r pixels around
// it, into the mask, and returns the pixels it touched.
func (c *Canvas) capsule(a, b Point, r float64) image.Rectangle {
	box := boundsOf([]Point{a, b}).Inset(-r - 1).Pixels().Intersect(c.img.Rect)
	ab := b.Sub(a)
	ll := ab.Dot(ab)
	w := c.img.Rect.Dx()
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			p := Point{float64(x) + 0.5, float64(y) + 0.5}
			ap := p.Sub(a)
			t := 0.0
			if ll > 0 {
				t = min(max(ap.Dot(ab)/ll, 0), 1)
			}
			cov := float32(coverage(ap.Sub(ab.Mul(t)).Len() - r))
			if i := y*w + x; cov > c.mask[i] {
				c.mask[i] = cov
			}
		}
	}
	return box
}

// composite blends col through the mask over box and zeroes the mask
// there again.
func (c *Canvas) composite(box image.Rectangle, col color.NRGBA) {
	w := c.img.Rect.Dx()
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			i := y*w + x
			if cov := c.mask[i]; cov > 0 {
				c.blend(c.img.PixOffset(x, y), col, float64(cov))
				c.mask[i] = 0
			}
		}
	}
}
