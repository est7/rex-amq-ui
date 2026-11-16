// Package raster draws anti-aliased shapes and text into an opaque RGBA
// canvas in pure Go, so a frame renders the same on every machine.
//
// Shapes are signed distance fields: each pixel's coverage is how far it
// lies inside the shape's edge, clamped to one pixel of ramp. Every
// primitive works only inside its own bounding box. Sizes are pixels; a
// caller scales them from the terminal's cell size (Metrics) so a frame
// looks the same at 9x22 and at 23x49.
package raster

import (
	"image"
	"image/color"
	"math"
)

// Metrics is the pixel size of one terminal cell, as Rex reports it
// (cell_width_px, cell_height_px).
type Metrics struct{ CellW, CellH int }

// Valid reports whether both sides are positive.
func (m Metrics) Valid() bool { return m.CellW > 0 && m.CellH > 0 }

// Point is a position in pixels; (0,0) is the top-left corner of the
// canvas's top-left pixel, so that pixel's center is (0.5,0.5).
type Point struct{ X, Y float64 }

func (p Point) Add(q Point) Point             { return Point{p.X + q.X, p.Y + q.Y} }
func (p Point) Sub(q Point) Point             { return Point{p.X - q.X, p.Y - q.Y} }
func (p Point) Mul(k float64) Point           { return Point{p.X * k, p.Y * k} }
func (p Point) Dot(q Point) float64           { return p.X*q.X + p.Y*q.Y }
func (p Point) Len() float64                  { return math.Hypot(p.X, p.Y) }
func (p Point) Perp() Point                   { return Point{-p.Y, p.X} }
func (p Point) Lerp(q Point, t float64) Point { return p.Add(q.Sub(p).Mul(t)) }

// Unit is p scaled to length 1, or the zero point when p is zero.
func (p Point) Unit() Point {
	l := p.Len()
	if l == 0 {
		return Point{}
	}
	return p.Mul(1 / l)
}

// Rect is an axis-aligned rectangle in pixels, Min inclusive, Max
// exclusive.
type Rect struct{ Min, Max Point }

// RectXYWH is the rectangle at x,y of size w x h.
func RectXYWH(x, y, w, h float64) Rect { return Rect{Point{x, y}, Point{x + w, y + h}} }

func (r Rect) W() float64    { return r.Max.X - r.Min.X }
func (r Rect) H() float64    { return r.Max.Y - r.Min.Y }
func (r Rect) Center() Point { return r.Min.Lerp(r.Max, 0.5) }
func (r Rect) Inset(d float64) Rect {
	return Rect{Point{r.Min.X + d, r.Min.Y + d}, Point{r.Max.X - d, r.Max.Y - d}}
}

// Contains reports whether p lies inside r.
func (r Rect) Contains(p Point) bool {
	return p.X >= r.Min.X && p.X < r.Max.X && p.Y >= r.Min.Y && p.Y < r.Max.Y
}

// Pixels is the smallest whole-pixel rectangle covering r.
func (r Rect) Pixels() image.Rectangle {
	return image.Rect(int(math.Floor(r.Min.X)), int(math.Floor(r.Min.Y)), int(math.Ceil(r.Max.X)), int(math.Ceil(r.Max.Y)))
}

// Canvas is an opaque RGBA image with the primitives drawn into it. It is
// meant to be kept and reused frame after frame: its fonts and scratch
// buffer stay warm. A Canvas is not safe for concurrent use.
type Canvas struct {
	img *image.RGBA
	m   Metrics
	// mask is a coverage scratch buffer the size of the image, all zero
	// between calls: a stroke's segments meet in max-combined coverage
	// here before one composite, so joints are not blended twice.
	mask  []float32
	faces map[faceKey]*Face
}

// NewCanvas is a w x h canvas, opaque black until cleared.
func NewCanvas(w, h int, m Metrics) *Canvas {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 0xff
	}
	return &Canvas{img: img, m: m, mask: make([]float32, w*h)}
}

// Image is the canvas's pixels: tightly packed RGBA rows, every pixel
// opaque, so premultiplied and straight alpha are the same bytes.
func (c *Canvas) Image() *image.RGBA { return c.img }

// Metrics is the cell size the canvas was made for.
func (c *Canvas) Metrics() Metrics { return c.m }

// Bounds is the canvas rectangle in pixels.
func (c *Canvas) Bounds() Rect {
	b := c.img.Bounds()
	return Rect{Point{0, 0}, Point{float64(b.Dx()), float64(b.Dy())}}
}

// Clear paints every pixel col, fully opaque whatever col's alpha.
func (c *Canvas) Clear(col color.NRGBA) {
	p := c.img.Pix
	if len(p) == 0 {
		return
	}
	p[0], p[1], p[2], p[3] = col.R, col.G, col.B, 0xff
	for n := 4; n < len(p); n *= 2 {
		copy(p[n:], p[:n])
	}
}

// FillRect paints r col, anti-aliased at fractional edges.
func (c *Canvas) FillRect(r Rect, col color.NRGBA) {
	c.FillRoundRect(r, 0, col)
}

// FillRoundRect paints r with corners of the given radius.
func (c *Canvas) FillRoundRect(r Rect, radius float64, col color.NRGBA) {
	c.shade(r.Pixels(), col, func(p Point) float64 {
		return coverage(roundRectSDF(p, r, radius))
	})
}

// StrokeRoundRect paints a border of the given width inside r's edge.
func (c *Canvas) StrokeRoundRect(r Rect, radius, width float64, col color.NRGBA) {
	c.shade(r.Pixels(), col, func(p Point) float64 {
		d := roundRectSDF(p, r, radius)
		return coverage(math.Abs(d+width/2) - width/2)
	})
}

// Halo paints a soft glow around r (its corners rounded by radius): full
// col at the edge fading to nothing spread pixels out, and col inside.
// col's alpha is the glow's strength, which is how a caller breathes it.
// A circle halo is a square r with radius half its side.
func (c *Canvas) Halo(r Rect, radius, spread float64, col color.NRGBA) {
	if spread <= 0 {
		return
	}
	c.shade(r.Inset(-spread).Pixels(), col, func(p Point) float64 {
		d := roundRectSDF(p, r, radius)
		if d <= 0 {
			return 1
		}
		f := 1 - d/spread
		if f <= 0 {
			return 0
		}
		return f * f
	})
}

// Disc paints a filled circle.
func (c *Canvas) Disc(center Point, r float64, col color.NRGBA) {
	b := RectXYWH(center.X-r, center.Y-r, 2*r, 2*r)
	c.shade(b.Pixels(), col, func(p Point) float64 {
		return coverage(p.Sub(center).Len() - r)
	})
}

// Arrow paints a filled arrowhead with its point at tip, pointing along
// dir, length long and 2*halfWidth wide at its base.
func (c *Canvas) Arrow(tip, dir Point, length, halfWidth float64, col color.NRGBA) {
	u := dir.Unit()
	if u == (Point{}) {
		return
	}
	base := tip.Sub(u.Mul(length))
	n := u.Perp().Mul(halfWidth)
	tri := [3]Point{tip, base.Add(n), base.Sub(n)}
	b := boundsOf(tri[:]).Inset(-1)
	c.shade(b.Pixels(), col, func(p Point) float64 {
		return coverage(convexSDF(p, tri[:]))
	})
}

// shade blends col over every pixel of box by the coverage f gives the
// pixel's center.
func (c *Canvas) shade(box image.Rectangle, col color.NRGBA, f func(Point) float64) {
	box = box.Intersect(c.img.Rect)
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			if cov := f(Point{float64(x) + 0.5, float64(y) + 0.5}); cov > 0 {
				c.blend(c.img.PixOffset(x, y), col, cov)
			}
		}
	}
}

// blend mixes col into the pixel at off by cov times col's alpha.
func (c *Canvas) blend(off int, col color.NRGBA, cov float64) {
	a := cov * float64(col.A) / 255
	if a >= 1 {
		c.img.Pix[off], c.img.Pix[off+1], c.img.Pix[off+2] = col.R, col.G, col.B
		return
	}
	p := c.img.Pix[off : off+3 : off+3]
	p[0] = mix(p[0], col.R, a)
	p[1] = mix(p[1], col.G, a)
	p[2] = mix(p[2], col.B, a)
}

func mix(dst, src uint8, a float64) uint8 {
	return uint8(float64(dst) + (float64(src)-float64(dst))*a + 0.5)
}

// coverage is how much of a pixel a signed distance d covers: 1 a half
// pixel inside the edge, 0 a half pixel outside.
func coverage(d float64) float64 {
	return min(max(0.5-d, 0), 1)
}

// roundRectSDF is the signed distance from p to r with corners of
// radius, negative inside.
func roundRectSDF(p Point, r Rect, radius float64) float64 {
	radius = min(radius, r.W()/2, r.H()/2)
	c := r.Center()
	qx := math.Abs(p.X-c.X) - (r.W()/2 - radius)
	qy := math.Abs(p.Y-c.Y) - (r.H()/2 - radius)
	outside := math.Hypot(max(qx, 0), max(qy, 0))
	return outside + min(max(qx, qy), 0) - radius
}

// convexSDF is the signed distance from p to a convex polygon, exact
// inside and along edges, a slight underestimate past a corner (which
// only touches the outermost ramp pixel).
func convexSDF(p Point, poly []Point) float64 {
	area := 0.0
	for i := range poly {
		a, b := poly[i], poly[(i+1)%len(poly)]
		area += a.X*b.Y - b.X*a.Y
	}
	d := math.Inf(-1)
	for i := range poly {
		a, b := poly[i], poly[(i+1)%len(poly)]
		e := b.Sub(a).Unit()
		// Outward normal: right of the edge for a counter-clockwise
		// polygon in y-down coordinates (positive area), left otherwise.
		n := Point{e.Y, -e.X}
		if area < 0 {
			n = n.Mul(-1)
		}
		d = max(d, p.Sub(a).Dot(n))
	}
	return d
}

func boundsOf(ps []Point) Rect {
	r := Rect{ps[0], ps[0]}
	for _, p := range ps[1:] {
		r.Min.X, r.Min.Y = min(r.Min.X, p.X), min(r.Min.Y, p.Y)
		r.Max.X, r.Max.Y = max(r.Max.X, p.X), max(r.Max.Y, p.Y)
	}
	return r
}
