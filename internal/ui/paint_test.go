package ui

import (
	"strings"
	"testing"

	"github.com/est7/rex-amq-ui/internal/theme"
)

func TestPaintPrimitivesFillEveryCell(t *testing.T) {
	p := theme.Default()
	nasty := []Span{
		{Text: "界面 🧑‍💻 ok", Fg: p.Fg},
		{Text: "\x1b[31mred\x1b[0m\ttab\nnl\x9b2J\xff", Fg: p.Red, Bg: p.RedBg, Bold: true},
		{Text: "", Fg: p.Fg},
		{Text: "tail", Fg: p.Muted, Italic: true},
	}
	for _, w := range []int{0, 1, 2, 3, 7, 40, 120} {
		for _, h := range []int{0, 1, 2, 5} {
			c := newCanvas(w, h, p.Bg)
			c.add(nasty...)
			c.addLR(p.Surface, nasty, []Span{{Text: "● Live", Fg: p.Green}, {Text: " 界", Fg: p.Fg}})
			c.addOn(p.YellowBg, Span{Text: strings.Repeat("x", 300), Fg: p.Fg})
			frame := c.String()
			if bad := holes(frame, w, h); bad != nil {
				t.Errorf("%dx%d: %v", w, h, bad)
			}
			if strings.Contains(frame, "\x1b[31m") || strings.Contains(frame, "\t") || strings.Contains(frame, "\x9b") {
				t.Errorf("%dx%d: untrusted control reached the frame: %q", w, h, frame)
			}
		}
	}
}

func TestLineLRKeepsRightSide(t *testing.T) {
	p := theme.Default()
	got := plain(lineLR(20, p.Bg, []Span{{Text: strings.Repeat("a", 30)}}, []Span{{Text: "● Live"}}))
	if !strings.HasSuffix(got, "● Live") || !strings.Contains(got, "…") {
		t.Fatalf("right side lost or left not cut: %q", got)
	}
}
