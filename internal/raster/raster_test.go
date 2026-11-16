package raster_test

import (
	"fmt"
	"image/color"
	"math"
	"slices"
	"testing"

	"github.com/est7/rex-amq-ui/internal/raster"
	"github.com/est7/rex-amq-ui/internal/raster/rastertest"
)

var (
	bg    = color.NRGBA{0x2d, 0x35, 0x3b, 0xff}
	white = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	red   = color.NRGBA{0xe6, 0x7e, 0x80, 0xff}
)

// The cell sizes observed on live Rex: a standard display and a Retina one.
var cellSizes = []raster.Metrics{{CellW: 9, CellH: 22}, {CellW: 23, CellH: 49}}

var _ = rastertest.UpdateFlag()

func newCanvas(w, h int) *raster.Canvas {
	c := raster.NewCanvas(w, h, raster.Metrics{CellW: 9, CellH: 22})
	c.Clear(bg)
	return c
}

func pix(c *raster.Canvas, x, y int) color.RGBA {
	return c.Image().RGBAAt(x, y)
}

func isBg(p color.RGBA) bool { return p.R == bg.R && p.G == bg.G && p.B == bg.B }

func TestRasterRoundRectAntiAliased(t *testing.T) {
	c := newCanvas(40, 40)
	// Edges at x=10.5 and x=30.5 cut the pixel columns 10 and 30 in half.
	c.FillRoundRect(raster.Rect{Min: raster.Point{X: 10.5, Y: 10}, Max: raster.Point{X: 30.5, Y: 30}}, 6, white)

	if p := pix(c, 20, 20); p.R != 0xff || p.G != 0xff {
		t.Errorf("interior pixel %v, want white", p)
	}
	if p := pix(c, 5, 20); !isBg(p) {
		t.Errorf("outside pixel %v, want background", p)
	}
	edge := pix(c, 10, 20)
	if isBg(edge) || edge.R == 0xff {
		t.Errorf("half-covered edge pixel %v: want a blend between background and white", edge)
	}
	if mid := (int(bg.R) + 0xff) / 2; math.Abs(float64(int(edge.R)-mid)) > 3 {
		t.Errorf("edge pixel red %d, want about %d for half coverage", edge.R, mid)
	}
	// The corner is rounded: the pixel at the bounding box's corner stays
	// background while the straight edge next to it is covered.
	if p := pix(c, 11, 10); !isBg(p) {
		t.Errorf("rounded corner pixel %v, want background", p)
	}
	if p := pix(c, 20, 10); isBg(p) {
		t.Errorf("top edge pixel %v, want covered", p)
	}
}

func TestRasterStrokeRoundCapsAndDash(t *testing.T) {
	c := newCanvas(120, 30)
	path := []raster.Point{{X: 10, Y: 10.5}, {X: 110, Y: 10.5}}
	c.Stroke(path, 4, white, raster.Dash{})
	for _, x := range []int{10, 60, 109} {
		if p := pix(c, x, 10); p.R != 0xff {
			t.Errorf("solid stroke pixel x=%d is %v, want white", x, p)
		}
	}
	// A round cap reaches half the width past the end point, no further.
	if p := pix(c, 8, 10); isBg(p) {
		t.Errorf("cap pixel %v, want covered", p)
	}
	if p := pix(c, 5, 10); !isBg(p) {
		t.Errorf("pixel past the cap %v, want background", p)
	}

	path = []raster.Point{{X: 10, Y: 20.5}, {X: 110, Y: 20.5}}
	c.Stroke(path, 4, red, raster.Dash{On: 10, Off: 10})
	// Dashes run [10,20], [30,40], ...: x=15 is inside one, x=25 in a gap.
	if p := pix(c, 15, 20); isBg(p) {
		t.Errorf("dash pixel %v, want covered", p)
	}
	if p := pix(c, 25, 20); !isBg(p) {
		t.Errorf("gap pixel %v, want background", p)
	}
}

func TestRasterStrokeJointsBlendOnce(t *testing.T) {
	// A half-transparent stroke whose segments overlap at a joint must
	// show the same color at the joint as along a segment.
	c := newCanvas(60, 30)
	half := color.NRGBA{0xff, 0xff, 0xff, 0x80}
	c.Stroke([]raster.Point{{X: 5, Y: 15.5}, {X: 30, Y: 15.5}, {X: 55, Y: 15.5}}, 6, half, raster.Dash{})
	if a, b := pix(c, 30, 15), pix(c, 15, 15); a != b {
		t.Errorf("joint pixel %v differs from segment pixel %v: coverage blended twice", a, b)
	}
}

func TestRasterArrowPointsAlongDir(t *testing.T) {
	c := newCanvas(40, 40)
	c.Arrow(raster.Point{X: 30, Y: 20}, raster.Point{X: 1}, 16, 7, white)
	if p := pix(c, 20, 19); isBg(p) {
		t.Errorf("pixel inside the head %v, want covered", p)
	}
	if p := pix(c, 33, 19); !isBg(p) {
		t.Errorf("pixel past the tip %v, want background", p)
	}
	if p := pix(c, 15, 19); isBg(p) {
		t.Errorf("pixel at the base %v, want covered", p)
	}
	if p := pix(c, 28, 26); !isBg(p) {
		t.Errorf("pixel beside the narrow tip %v, want background", p)
	}
}

func TestRasterHaloFadesWithDistance(t *testing.T) {
	c := newCanvas(80, 40)
	c.Halo(raster.RectXYWH(30, 10, 20, 20), 10, 12, white)
	prev := 256
	for x := 50; x < 64; x++ {
		r := int(pix(c, x, 20).R)
		if r > prev {
			t.Fatalf("halo brightens at x=%d (%d after %d)", x, r, prev)
		}
		prev = r
	}
	if p := pix(c, 65, 20); !isBg(p) {
		t.Errorf("pixel past the spread %v, want background", p)
	}
	if p := pix(c, 40, 20); p.R != 0xff {
		t.Errorf("pixel inside the halo %v, want full color", p)
	}
}

func TestRasterTextEmbeddedFont(t *testing.T) {
	for _, m := range cellSizes {
		t.Run(fmt.Sprintf("%dx%d", m.CellW, m.CellH), func(t *testing.T) {
			c := raster.NewCanvas(40*m.CellW, 2*m.CellH, m)
			c.Clear(bg)
			f := c.CellFace(1, false)
			if math.Abs(f.Advance-float64(m.CellW)) > 0.75 {
				t.Errorf("cell face advance %.2f, want about one cell (%d px)", f.Advance, m.CellW)
			}
			if got, want := f.Measure("claude-106"), 10*f.Advance; math.Abs(got-want) > 0.01 {
				t.Errorf("monospace width %.2f, want %.2f", got, want)
			}
			before := slices.Clone(c.Image().Pix)
			c.Text(raster.Point{X: 0, Y: float64(m.CellH)}, "claude-106", f, white)
			// Count pixels whose colour the text changed, inside the run the
			// ten glyphs occupy and nowhere else.
			inside, outside := 0, 0
			img := c.Image()
			for y := 0; y < img.Rect.Dy(); y++ {
				for x := 0; x < img.Rect.Dx(); x++ {
					o := img.PixOffset(x, y)
					if slices.Equal(before[o:o+3], img.Pix[o:o+3]) {
						continue
					}
					if x < int(f.Measure("claude-106"))+1 && y < m.CellH+m.CellH/2 {
						inside++
					} else {
						outside++
					}
				}
			}
			if inside < 10*m.CellW || outside != 0 {
				t.Errorf("text changed %d pixels in its run and %d outside it, want glyphs (at least %d) and nothing else", inside, outside, 10*m.CellW)
			}
			if s := f.Fit("orchestrator-of-everything", 8*f.Advance); s != "orchestr…" && s != "orchest…" {
				t.Errorf("Fit = %q, want the text cut with an ellipsis", s)
			}
		})
	}
}

func TestRasterStaysOpaque(t *testing.T) {
	c := newCanvas(50, 50)
	c.Halo(raster.RectXYWH(10, 10, 20, 20), 4, 10, color.NRGBA{0xff, 0, 0, 0x40})
	c.Stroke([]raster.Point{{X: 0, Y: 0}, {X: 50, Y: 50}}, 3, color.NRGBA{0, 0xff, 0, 0x80}, raster.Dash{On: 4, Off: 3})
	for i := 3; i < len(c.Image().Pix); i += 4 {
		if c.Image().Pix[i] != 0xff {
			t.Fatalf("pixel %d alpha %d, want 255: Kitty would show the terminal through it", i/4, c.Image().Pix[i])
		}
	}
}

// TestRasterReference renders every primitive at both observed cell sizes,
// all geometry scaled from the cell, and compares with the committed PNGs.
func TestRasterReference(t *testing.T) {
	for _, m := range cellSizes {
		t.Run(fmt.Sprintf("%dx%d", m.CellW, m.CellH), func(t *testing.T) {
			c := samples(m)
			rastertest.Golden(t, fmt.Sprintf("../../testdata/raster/primitives-%dx%d.png", m.CellW, m.CellH), c.Image())
		})
	}
}

func samples(m raster.Metrics) *raster.Canvas {
	cw, ch := float64(m.CellW), float64(m.CellH)
	c := raster.NewCanvas(40*m.CellW, 8*m.CellH, m)
	c.Clear(bg)
	badge := raster.RectXYWH(2*cw, 1*ch, 14*cw, 2.4*ch)
	c.Halo(badge, ch*0.5, ch*0.6, color.NRGBA{0xa7, 0xc0, 0x80, 0x70})
	c.FillRoundRect(badge, ch*0.5, color.NRGBA{0x3d, 0x48, 0x4d, 0xff})
	c.StrokeRoundRect(badge, ch*0.5, cw*0.22, color.NRGBA{0xa7, 0xc0, 0x80, 0xff})
	c.Disc(raster.Point{X: badge.Min.X + 1.4*cw, Y: badge.Center().Y}, cw*0.45, color.NRGBA{0xa7, 0xc0, 0x80, 0xff})
	f := c.CellFace(1, true)
	c.Text(raster.Point{X: badge.Min.X + 2.6*cw, Y: badge.Min.Y + 1.05*ch}, "claude-106", f, color.NRGBA{0xd3, 0xc6, 0xaa, 0xff})
	c.Text(raster.Point{X: badge.Min.X + 2.6*cw, Y: badge.Min.Y + 1.95*ch}, "implementer", c.CellFace(0.85, false), color.NRGBA{0x85, 0x92, 0x89, 0xff})

	q := raster.Quad{P0: raster.Point{X: 20 * cw, Y: 1.5 * ch}, P1: raster.Point{X: 28 * cw, Y: 0}, P2: raster.Point{X: 37 * cw, Y: 3 * ch}}
	path := q.Flatten(0, 1, cw/2)
	head := ch * 0.45
	c.Stroke(raster.TrimEnd(path, head*0.6), cw*0.3, color.NRGBA{0xdb, 0xbc, 0x7f, 0xff}, raster.Dash{})
	c.Arrow(q.P2, q.Tangent(1), head, head*0.45, color.NRGBA{0xdb, 0xbc, 0x7f, 0xff})

	q2 := raster.Quad{P0: raster.Point{X: 20 * cw, Y: 5 * ch}, P1: raster.Point{X: 28 * cw, Y: 7.5 * ch}, P2: raster.Point{X: 37 * cw, Y: 5 * ch}}
	c.Stroke(raster.TrimEnd(q2.Flatten(0, 1, cw/2), head*0.6), cw*0.22, color.NRGBA{0xe6, 0x7e, 0x80, 0xff}, raster.Dash{On: cw * 1.2, Off: cw * 0.8})
	c.Arrow(q2.P2, q2.Tangent(1), head, head*0.45, color.NRGBA{0xe6, 0x7e, 0x80, 0xff})

	ring := raster.RectXYWH(6*cw, 5*ch, 2*ch, 2*ch)
	c.Halo(ring, ch, ch*0.5, color.NRGBA{0x7f, 0xbb, 0xb3, 0x90})
	c.Disc(ring.Center(), ch*0.6, color.NRGBA{0x7f, 0xbb, 0xb3, 0xff})
	return c
}

func BenchmarkRasterSamples(b *testing.B) {
	for b.Loop() {
		samples(raster.Metrics{CellW: 23, CellH: 49})
	}
}
