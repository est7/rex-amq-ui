// Package theme is the panel's colours: the Everforest palette it starts
// with, and the palette it switches to once the terminal has told it its
// real foreground and background (OSC 10/11).
//
// Rex drops OSC 10/11 queries from a block no app owns yet, so the panel
// asks only after the block has an owner (see internal/ui); until then, and
// when the terminal never answers, Everforest dark is the palette.
package theme

import (
	"fmt"
	"image/color"
	"math"
)

// Palette is every colour the panel paints with. Bg is the panel's
// background: every cell the panel draws carries it or another of the
// surface colours, never the terminal's default.
type Palette struct {
	Dark bool

	Bg      color.Color // the panel's background
	Surface color.Color // cards and the tab bar
	Overlay color.Color // the selected row and the active tab
	Fg      color.Color
	Muted   color.Color // secondary text
	Faint   color.Color // rules and hints

	Red, Orange, Yellow, Green, Aqua, Blue, Purple color.Color

	// Tints behind alert and hand-back cards.
	RedBg, YellowBg, GreenBg, BlueBg color.Color
}

func hex(s string) color.Color {
	var r, g, b uint8
	if _, err := fmt.Sscanf(s, "#%02x%02x%02x", &r, &g, &b); err != nil {
		panic("theme: bad colour " + s)
	}
	return color.RGBA{R: r, G: g, B: b, A: 0xff}
}

// Everforest is the Everforest (medium contrast) palette, dark or light.
func Everforest(dark bool) Palette {
	if dark {
		return Palette{
			Dark: true,
			Bg:   hex("#2d353b"), Surface: hex("#343f44"), Overlay: hex("#475258"),
			Fg: hex("#d3c6aa"), Muted: hex("#9da9a0"), Faint: hex("#7a8478"),
			Red: hex("#e67e80"), Orange: hex("#e69875"), Yellow: hex("#dbbc7f"), Green: hex("#a7c080"),
			Aqua: hex("#83c092"), Blue: hex("#7fbbb3"), Purple: hex("#d699b6"),
			RedBg: hex("#514045"), YellowBg: hex("#4d4c43"), GreenBg: hex("#425047"), BlueBg: hex("#3a515d"),
		}
	}
	return Palette{
		Bg: hex("#fdf6e3"), Surface: hex("#f4f0d9"), Overlay: hex("#e6e2cc"),
		Fg: hex("#5c6a72"), Muted: hex("#829181"), Faint: hex("#a6b0a0"),
		Red: hex("#f85552"), Orange: hex("#f57d26"), Yellow: hex("#dfa000"), Green: hex("#8da101"),
		Aqua: hex("#35a77c"), Blue: hex("#3a94c5"), Purple: hex("#df69ba"),
		RedBg: hex("#fde3da"), YellowBg: hex("#faedcd"), GreenBg: hex("#f0f1d2"), BlueBg: hex("#e9f0e9"),
	}
}

// Default is the palette before the terminal has answered.
func Default() Palette { return Everforest(true) }

// FromTerminal is the palette for a terminal whose foreground is fg and
// background bg: Everforest dark or light by the background's luminance,
// with the terminal's own background and foreground, and the surfaces
// mixed between them so cards stay a step off the background.
func FromTerminal(fg, bg color.Color) Palette {
	p := Everforest(IsDark(bg))
	p.Bg, p.Fg = opaque(bg), opaque(fg)
	p.Surface = Mix(p.Bg, p.Fg, 0.07)
	p.Overlay = Mix(p.Bg, p.Fg, 0.16)
	p.Faint = Mix(p.Bg, p.Fg, 0.45)
	p.Muted = Mix(p.Bg, p.Fg, 0.7)
	p.RedBg = Mix(p.Bg, p.Red, 0.18)
	p.YellowBg = Mix(p.Bg, p.Yellow, 0.18)
	p.GreenBg = Mix(p.Bg, p.Green, 0.18)
	p.BlueBg = Mix(p.Bg, p.Blue, 0.18)
	return p
}

// IsDark reports whether c's relative luminance (sRGB, WCAG) is below the
// midpoint, so light text reads on it.
func IsDark(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	lum := 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
	return lum < 0.18 // the luminance of mid grey (#777)
}

func linear(v uint32) float64 {
	c := float64(v) / 0xffff
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// Mix is a + (b-a)*t per channel, opaque.
func Mix(a, b color.Color, t float64) color.Color {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	m := func(x, y uint32) uint8 { return uint8((float64(x) + (float64(y)-float64(x))*t) / 0x101) }
	return color.RGBA{R: m(ar, br), G: m(ag, bg), B: m(ab, bb), A: 0xff}
}

func opaque(c color.Color) color.Color {
	r, g, b, _ := c.RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 0xff}
}
