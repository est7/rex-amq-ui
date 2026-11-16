package theme

import (
	"image/color"
	"testing"
)

func TestFromTerminalPicksVariantByLuminance(t *testing.T) {
	black, white := color.RGBA{0, 0, 0, 255}, color.RGBA{255, 255, 255, 255}
	if p := FromTerminal(white, black); !p.Dark || p.Bg != (color.RGBA{0, 0, 0, 255}) {
		t.Fatalf("black background: dark=%v bg=%v", p.Dark, p.Bg)
	}
	solarizedLight := color.RGBA{0xfd, 0xf6, 0xe3, 255}
	if p := FromTerminal(black, solarizedLight); p.Dark || p.Red != Everforest(false).Red {
		t.Fatalf("light background: dark=%v red=%v", p.Dark, p.Red)
	}
	// Mid grey sits on the line; a shade darker is dark, lighter is light.
	if !IsDark(color.RGBA{0x60, 0x60, 0x60, 255}) || IsDark(color.RGBA{0x90, 0x90, 0x90, 255}) {
		t.Fatal("luminance threshold misplaced")
	}
}

func TestSurfacesDifferFromBackground(t *testing.T) {
	p := FromTerminal(color.RGBA{220, 220, 220, 255}, color.RGBA{20, 20, 20, 255})
	for name, c := range map[string]color.Color{"surface": p.Surface, "overlay": p.Overlay, "redbg": p.RedBg} {
		if c == p.Bg {
			t.Errorf("%s equals the background", name)
		}
	}
}
