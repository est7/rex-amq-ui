package raster

import (
	"fmt"
	"image"
	"image/color"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// The fonts are Go Mono and Go Mono Bold, compiled into the binary, so text
// renders the same whatever fonts the machine has.
var (
	parseOnce sync.Once
	regular   *opentype.Font
	bold      *opentype.Font
	parseErr  error
)

func fonts() (*opentype.Font, *opentype.Font, error) {
	parseOnce.Do(func() {
		if regular, parseErr = opentype.Parse(gomono.TTF); parseErr != nil {
			return
		}
		bold, parseErr = opentype.Parse(gomonobold.TTF)
	})
	return regular, bold, parseErr
}

// Monospace advance as a fraction of the font size: Go Mono's glyphs are
// 0.6 em wide, so a font of CellW/0.6 pixels fills a terminal cell.
const monoAdvance = 0.6

type faceKey struct {
	px   int
	bold bool
}

// Face is a font at one pixel size.
type Face struct {
	face   font.Face
	Ascent float64
	// Height is the distance between baselines.
	Height  float64
	Advance float64
}

// Face is Go Mono (bold when asked) at px pixels per em, made once per
// canvas and kept.
func (c *Canvas) Face(px int, isBold bool) *Face {
	px = max(px, 4)
	k := faceKey{px, isBold}
	if f, ok := c.faces[k]; ok {
		return f
	}
	reg, b, err := fonts()
	if err != nil {
		// The fonts are compiled in; failing to parse them is a broken
		// build, not a runtime condition.
		panic(fmt.Sprintf("raster: embedded Go Mono font: %v", err))
	}
	src := reg
	if isBold {
		src = b
	}
	ff, err := opentype.NewFace(src, &opentype.FaceOptions{Size: float64(px), DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic(fmt.Sprintf("raster: Go Mono at %dpx: %v", px, err))
	}
	m := ff.Metrics()
	adv, _ := ff.GlyphAdvance('M')
	f := &Face{face: ff, Ascent: fix(m.Ascent), Height: fix(m.Height), Advance: fix(adv)}
	if c.faces == nil {
		c.faces = map[faceKey]*Face{}
	}
	c.faces[k] = f
	return f
}

// CellFace is the face whose glyphs are one terminal cell wide, scaled by
// k (1 is the terminal's own text size).
func (c *Canvas) CellFace(k float64, isBold bool) *Face {
	return c.Face(int(float64(c.m.CellW)/monoAdvance*k+0.5), isBold)
}

// Measure is the width of s in pixels.
func (f *Face) Measure(s string) float64 {
	return fix(font.MeasureString(f.face, s))
}

// Fit is s cut to at most w pixels wide, ending in "…" when cut.
func (f *Face) Fit(s string, w float64) string {
	if f.Measure(s) <= w {
		return s
	}
	r := []rune(s)
	for n := len(r) - 1; n > 0; n-- {
		if t := string(r[:n]) + "…"; f.Measure(t) <= w {
			return t
		}
	}
	return ""
}

// Text paints s with its baseline starting at at.
func (c *Canvas) Text(at Point, s string, f *Face, col color.NRGBA) {
	d := font.Drawer{
		Dst:  c.img,
		Src:  image.NewUniform(col),
		Face: f.face,
		Dot:  fixed.Point26_6{X: fixed.Int26_6(at.X * 64), Y: fixed.Int26_6(at.Y * 64)},
	}
	d.DrawString(s)
}

// TextCentered paints s centered on center, both ways.
func (c *Canvas) TextCentered(center Point, s string, f *Face, col color.NRGBA) {
	c.Text(Point{center.X - f.Measure(s)/2, center.Y + f.Ascent*0.38}, s, f, col)
}

func fix(v fixed.Int26_6) float64 { return float64(v) / 64 }
