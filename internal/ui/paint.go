package ui

import (
	"image/color"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Painting rules. A Rex floating layer shows the terminal underneath through
// every cell drawn on the default background, so every cell the panel emits
// carries an explicit background: each span is rendered with one, the rest
// of a line is padded with the line's, and a frame is padded to its full
// height with background lines. Styles are never nested (an inner reset
// would end the outer background mid-line) and lines are never joined or
// placed by Lipgloss helpers that pad with unstyled spaces. line and canvas
// are the only ways views emit text.

// Span is a run of text in one style. A nil Bg takes the line's background.
type Span struct {
	Text      string
	Fg, Bg    color.Color
	Bold      bool
	Italic    bool
	Underline bool
}

func (s Span) width() int { return ansi.StringWidth(s.Text) }

// line renders spans into exactly width cells on bg: text past the width is
// cut with an ellipsis, and the rest of the line is padded with bg. Text is
// cleaned first, so nothing in it reaches the terminal as a control.
func line(width int, bg color.Color, spans ...Span) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	left := width
	for _, s := range spans {
		if left <= 0 {
			break
		}
		s.Text = clean(s.Text)
		w := s.width()
		if w == 0 {
			continue
		}
		if w > left {
			s.Text = ansi.Truncate(s.Text, left, "…")
			w = s.width()
		}
		b.WriteString(render(s, bg))
		left -= w
	}
	if left > 0 {
		b.WriteString(render(Span{Text: strings.Repeat(" ", left)}, bg))
	}
	return b.String()
}

// lineLR is a line with right flush against its end; left is cut first
// when both do not fit, keeping at least one cell between them.
func lineLR(width int, bg color.Color, left, right []Span) string {
	rw := 0
	for i := range right {
		right[i].Text = clean(right[i].Text)
		rw += right[i].width()
	}
	if rw >= width {
		return line(width, bg, right...)
	}
	l := line(width-rw, bg, left...)
	return l + line(rw, bg, right...)
}

func render(s Span, bg color.Color) string {
	if s.Bg == nil {
		s.Bg = bg
	}
	st := lipgloss.NewStyle().Background(s.Bg)
	if s.Fg != nil {
		st = st.Foreground(s.Fg)
	}
	if s.Bold {
		st = st.Bold(true)
	}
	if s.Italic {
		st = st.Italic(true)
	}
	if s.Underline {
		st = st.Underline(true)
	}
	return st.Render(s.Text)
}

// clean makes untrusted text one line of printable text: tabs and line
// breaks become spaces, every other control character (escape sequences'
// introducers, C1 controls included) is dropped, and bytes that are not
// UTF-8 become U+FFFD, so no raw 8-bit control gets through either.
func clean(s string) string {
	ok := utf8.ValidString(s)
	for _, r := range s {
		if isControl(r) {
			ok = false
			break
		}
	}
	if ok {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteByte(' ')
		case isControl(r):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == '�'
}

// canvas is a frame of exactly w x h cells on bg, built line by line. Lines
// past h are dropped; missing lines are painted blank.
type canvas struct {
	w, h  int
	bg    color.Color
	lines []string
}

func newCanvas(w, h int, bg color.Color) *canvas {
	return &canvas{w: max(w, 0), h: max(h, 0), bg: bg}
}

// full reports whether no more lines fit.
func (c *canvas) full() bool { return len(c.lines) >= c.h }

// left is how many lines still fit.
func (c *canvas) left() int { return max(c.h-len(c.lines), 0) }

// add appends a line of spans on the canvas background.
func (c *canvas) add(spans ...Span) { c.addOn(c.bg, spans...) }

// addOn appends a line of spans on bg (a card's tint).
func (c *canvas) addOn(bg color.Color, spans ...Span) {
	if !c.full() {
		c.lines = append(c.lines, line(c.w, bg, spans...))
	}
}

// addLR appends a line with right flush against its end.
func (c *canvas) addLR(bg color.Color, left, right []Span) {
	if !c.full() {
		c.lines = append(c.lines, lineLR(c.w, bg, left, right))
	}
}

// blank appends an empty line.
func (c *canvas) blank() { c.add() }

// rows pads c to h lines and returns them (each width w, or "" when w == 0).
func (c *canvas) rows() []string {
	for !c.full() {
		c.blank()
	}
	return c.lines
}

// String is the frame: h lines of w cells.
func (c *canvas) String() string {
	if c.w == 0 || c.h == 0 {
		return ""
	}
	return strings.Join(c.rows(), "\n")
}

// frameCanvas wraps inner (sized w-2 x h-2) in a 1-cell opaque rounded border
// when w >= 2 && h >= 2. Tiny dimensions fall back to a solid bg canvas so
// every cell still carries an explicit background.
func frameCanvas(w, h int, bg, borderFg color.Color, inner *canvas) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	if w < 2 || h < 2 {
		return newCanvas(w, h, bg).String()
	}
	lines := make([]string, 0, h)
	fill := strings.Repeat("─", w-2)
	lines = append(lines, line(w, bg, Span{Text: "╭" + fill + "╮", Fg: borderFg}))
	edge := render(Span{Text: "│", Fg: borderFg}, bg)
	for _, r := range inner.rows() {
		lines = append(lines, edge+r+edge)
	}
	lines = append(lines, line(w, bg, Span{Text: "╰" + fill + "╯", Fg: borderFg}))
	return strings.Join(lines, "\n")
}
