package ui

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/rivo/uniseg"
)

// holes checks that frame is exactly w x h cells and that every cell, the
// padding and blank rows included, is drawn on an explicit background. It
// reads the frame the way a terminal would: SGR parameters set and clear
// the background, and each grapheme takes its display width in cells with
// the background in force when it is printed. Any escape sequence or
// control it does not model is reported, so a frame it cannot read never
// passes. It returns one problem per line at most.
//
// It is written apart from the painter (its own SGR reader, uniseg for
// widths) so a painter bug cannot hide behind a shared helper.
func holes(frame string, w, h int) []string {
	if w == 0 || h == 0 {
		if frame != "" {
			return []string{fmt.Sprintf("a %dx%d frame should be empty, got %q", w, h, frame)}
		}
		return nil
	}
	lines := strings.Split(frame, "\n")
	var out []string
	if len(lines) != h {
		out = append(out, fmt.Sprintf("frame has %d lines, want %d", len(lines), h))
	}
	bg := false // the background state carries across lines, as in a terminal
	for y, l := range lines {
		cells, problem := 0, ""
		for i := 0; i < len(l) && problem == ""; {
			switch {
			case l[i] == 0x1b:
				n, err := sgr(l[i:], &bg)
				if err != nil {
					problem = fmt.Sprintf("line %d col %d: %v", y, cells, err)
					break
				}
				i += n
			case l[i] < 0x20 || l[i] == 0x7f:
				problem = fmt.Sprintf("line %d col %d: control byte %#x", y, cells, l[i])
			default:
				g, _, width, _ := uniseg.FirstGraphemeClusterInString(l[i:], -1)
				if !bg {
					problem = fmt.Sprintf("line %d col %d: %q drawn on the default background", y, cells, g)
					break
				}
				cells += width
				i += len(g)
			}
		}
		if problem == "" && cells != w {
			problem = fmt.Sprintf("line %d is %d cells wide, want %d", y, cells, w)
		}
		if problem != "" {
			out = append(out, problem)
		}
	}
	return out
}

// sgr reads the CSI ... m sequence at the start of s, updating bg, and
// returns its length. Anything else is an error.
func sgr(s string, bg *bool) (int, error) {
	if len(s) < 2 || s[1] != '[' {
		return 0, fmt.Errorf("unmodelled escape %q", s[:min(len(s), 8)])
	}
	end := strings.IndexFunc(s[2:], func(r rune) bool { return r >= 0x40 && r <= 0x7e })
	if end < 0 || s[2+end] != 'm' {
		return 0, fmt.Errorf("unmodelled CSI %q", s[:min(len(s), 12)])
	}
	body := s[2 : 2+end]
	n := 2 + end + 1
	if body == "" {
		*bg = false
		return n, nil
	}
	ps := strings.Split(body, ";")
	for i := 0; i < len(ps); i++ {
		p, err := strconv.Atoi(ps[i])
		if err != nil {
			return 0, fmt.Errorf("SGR %q: parameter %q", body, ps[i])
		}
		switch {
		case p == 0:
			*bg = false
		case p == 49:
			*bg = false
		case p >= 40 && p <= 47, p >= 100 && p <= 107:
			*bg = true
		case p == 38 || p == 48 || p == 58:
			skip, err := extended(ps[i+1:])
			if err != nil {
				return 0, fmt.Errorf("SGR %q: %v", body, err)
			}
			if p == 48 {
				*bg = true
			}
			i += skip
		case p == 7:
			// Reverse video paints the background with the foreground,
			// default included; the painter never uses it.
			return 0, fmt.Errorf("SGR %q: reverse video is not modelled", body)
		case p >= 1 && p <= 9, p >= 21 && p <= 29, p >= 30 && p <= 37, p == 39, p == 59, p >= 90 && p <= 97:
		default:
			return 0, fmt.Errorf("SGR %q: unmodelled parameter %d", body, p)
		}
	}
	return n, nil
}

// extended is how many parameters follow 38, 48, or 58: 5;n or 2;r;g;b,
// each in 0..255.
func extended(rest []string) (int, error) {
	n := 0
	switch {
	case len(rest) >= 2 && rest[0] == "5":
		n = 2
	case len(rest) >= 4 && rest[0] == "2":
		n = 4
	default:
		return 0, fmt.Errorf("truncated extended colour %v", rest)
	}
	for _, a := range rest[1:n] {
		if v, err := strconv.Atoi(a); err != nil || v < 0 || v > 255 {
			return 0, fmt.Errorf("extended colour argument %q", a)
		}
	}
	return n, nil
}

const bgOn = "\x1b[48;2;45;53;59m"

func TestPaintNoHolesDetectsHoles(t *testing.T) {
	good := bgOn + "abc" + "\x1b[m" + bgOn + "  \x1b[m"
	if p := holes(good+"\n"+good, 5, 2); p != nil {
		t.Fatalf("a whole frame was flagged: %v", p)
	}
	// A foreground whose green is 49 is not a background reset.
	fg49 := bgOn + "\x1b[38;2;49;49;49mabcde\x1b[m"
	if p := holes(fg49, 5, 1); p != nil {
		t.Fatalf("38;2;49;... read as a background reset: %v", p)
	}
	planted := map[string]struct {
		frame string
		w, h  int
	}{
		"unstyled cell mid-line": {bgOn + "ab\x1b[m c" + bgOn + "d\x1b[m", 5, 1},
		"short line":             {good + "\n" + bgOn + "abcd\x1b[m", 5, 2},
		"missing blank row":      {good, 5, 2},
		"49 then a cell":         {bgOn + "ab\x1b[49mcde\x1b[m", 5, 1},
		"0 then a cell":          {bgOn + "ab\x1b[0mcde", 5, 1},
		"bare reset then a cell": {bgOn + "ab\x1b[mcde", 5, 1},
		"wide grapheme overflow": {bgOn + "abc界\x1b[m", 4, 1},
		"default bg at line two": {good + "\n" + "\x1b[mabcde", 5, 2},
		"unmodelled sequence":    {bgOn + "ab\x1b[2Kcde\x1b[m", 5, 1},
		"OSC":                    {bgOn + "\x1b]8;;x\x1b\\abcde\x1b[m", 5, 1},
		"raw tab":                {bgOn + "ab\tcd\x1b[m", 5, 1},
		"too many lines":         {good + "\n" + good + "\n" + good, 5, 2},
		"reverse video":          {bgOn + "\x1b[7mabcde\x1b[m", 5, 1},
		"truncated colour":       {"\x1b[48;2;1;2mabcde\x1b[m", 5, 1},
		"colour out of range":    {"\x1b[48;2;1;2;256mabcde\x1b[m", 5, 1},
		"256-colour index range": {"\x1b[48;5;300mabcde\x1b[m", 5, 1},
	}
	for name, c := range planted {
		if p := holes(c.frame, c.w, c.h); len(p) == 0 {
			t.Errorf("%s: not detected", name)
		}
	}
}
