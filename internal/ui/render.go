package ui

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/est7/rex-amq-ui/internal/adapter"
)

// Frame layout: a 1-cell rounded border around the panel when at least 2x2,
// containing two header lines (title, tabs), the tab's content, and a footer
// line.
const (
	borderWidth = 1
	headerLines = 2
	footerLines = 1
)

// Rect is a rectangle of cells in the frame, from its top-left corner.
type Rect struct{ X, Y, W, H int }

func (m Model) framed() bool       { return m.w >= 2*borderWidth && m.h >= 2*borderWidth }
func (m Model) innerW() int        { return max(m.w-2*borderWidth, 0) }
func (m Model) innerH() int        { return max(m.h-2*borderWidth, 0) }
func (m Model) contentHeight() int { return max(m.innerH()-headerLines-footerLines, 0) }

func (m Model) render() string {
	if m.w <= 0 || m.h <= 0 {
		return ""
	}
	if !m.framed() {
		return newCanvas(m.w, m.h, m.pal.Bg).String()
	}
	c := newCanvas(m.innerW(), m.innerH(), m.pal.Bg)
	m.header(c)
	m.tabBar(c)
	body := newCanvas(m.innerW(), m.contentHeight(), m.pal.Bg)
	switch {
	case !m.st.Ready():
		m.waiting(body)
	case m.tab == TabEpics:
		m.epicsView(body)
	case m.tab == TabOverview:
		m.overview(body)
	case m.tab == TabMembers:
		m.membersView(body)
	case m.tab == TabMail:
		m.mailView(body)
	case m.tab == TabGraph:
		m.graphView(body)
	}
	if body.h > 0 {
		for _, l := range body.rows() {
			if !c.full() {
				c.lines = append(c.lines, l)
			}
		}
	}
	for c.left() > footerLines {
		c.blank()
	}
	m.footer(c)
	return frameCanvas(m.w, m.h, m.pal.Bg, m.pal.Faint, c)
}

func (m Model) header(c *canvas) {
	p := m.pal
	var crew []Span
	if m.st.Panel != nil && m.st.Panel.Crew != nil && m.st.Panel.Crew.Crew != "" {
		crew = []Span{{Text: " · crew:" + m.st.Panel.Crew.Crew, Fg: p.Muted}}
	}
	var left []Span
	if m.DrilledDown() {
		epicName := m.drill.epic.Name
		if epicName == "" && m.st.Panel != nil {
			epicName = m.st.Panel.SessionLabel
		}
		if epicName == "" {
			epicName = m.drill.epic.SessionID
		}
		left = []Span{
			{Text: " ← ", Fg: p.Aqua, Bold: true},
			{Text: m.drill.hubLabel, Fg: p.Muted},
			{Text: " › ", Fg: p.Faint},
			{Text: epicName, Fg: p.Fg, Bold: true},
		}
		left = append(left, crew...)
	} else {
		title := "rex-amq-ui"
		if m.st.Panel != nil && m.st.Panel.SessionLabel != "" {
			title = m.st.Panel.SessionLabel
		}
		left = append([]Span{{Text: " " + title, Fg: p.Fg, Bold: true}}, crew...)
	}
	var live Span
	switch {
	case m.st.Ready() && m.status.Status == adapter.Connected:
		live = Span{Text: "● Live", Fg: p.Green}
	case m.status.Status == adapter.Refused:
		live = Span{Text: "✕ Refused", Fg: p.Red}
	case m.status.Status == adapter.Reconnecting:
		live = Span{Text: "◌ Reconnecting", Fg: p.Yellow}
	default:
		live = Span{Text: "◌ Connecting", Fg: p.Faint}
	}
	right := []Span{live, {Text: "  " + m.size.String() + " ", Fg: p.Muted}}
	if m.deps.Layer != nil && !m.lay.Connected {
		right = append([]Span{{Text: "◌ Rex  ", Fg: p.Yellow}}, right...)
	}
	c.addLR(p.Bg, left, right)
}

type tabPill struct {
	tab        Tab
	start, end int // half-open frame X cell coordinates [start, end)
	spans      []Span
}

// tabPills builds the tab bar pills and their exact frame X coordinates so
// rendering and mouse click hit-testing stay in lockstep.
func (m Model) tabPills() []tabPill {
	p := m.pal
	tabs := m.tabs()
	out := make([]tabPill, 0, len(tabs))
	col := 1 // leading space inside left border
	maxX := m.w - borderWidth
	for _, t := range tabs {
		label := m.tabLabel(t)
		bg, fg := p.Surface, p.Muted
		if t == m.tab {
			bg, fg = p.Overlay, p.Fg
		}
		ps := []Span{{Text: " " + label.name, Fg: fg, Bg: bg, Bold: t == m.tab}}
		if label.count != "" {
			ps = append(ps, Span{Text: " " + label.count, Fg: label.countFg, Bg: bg, Bold: t == m.tab})
		}
		ps = append(ps, Span{Text: " ", Bg: bg})
		w := 0
		for _, s := range ps {
			w += s.width()
		}
		start := borderWidth + col
		end := min(start+w, maxX)
		out = append(out, tabPill{tab: t, start: start, end: end, spans: ps})
		col += w + 1 // 1-cell gap after pill
	}
	return out
}

func (m Model) tabAt(cellX int) (Tab, bool) {
	for _, p := range m.tabPills() {
		if cellX >= p.start && cellX < p.end {
			return p.tab, true
		}
	}
	return 0, false
}

func (m Model) tabBar(c *canvas) {
	spans := []Span{{Text: " "}}
	for _, pill := range m.tabPills() {
		spans = append(spans, pill.spans...)
		spans = append(spans, Span{Text: " "})
	}
	c.add(spans...)
}

type tabLabel struct {
	name, count string
	countFg     color.Color
}

func (m Model) tabLabel(t Tab) tabLabel {
	p := m.pal
	switch t {
	case TabEpics:
		return tabLabel{"Epics", fmt.Sprint(len(m.epics())), p.Muted}
	case TabOverview:
		if n := len(m.alerts()); n > 0 {
			return tabLabel{"Overview", fmt.Sprintf("●%d", n), p.Red}
		}
		return tabLabel{name: "Overview"}
	case TabMembers:
		return tabLabel{"Members", fmt.Sprint(m.liveCount()), p.Muted}
	case TabMail:
		return tabLabel{"Mail", fmt.Sprint(len(m.st.Messages)), p.Muted}
	}
	return tabLabel{name: "Graph"}
}

func (m Model) footer(c *canvas) {
	p := m.pal
	if m.hint != "" {
		fg := p.Muted
		if m.hintBad {
			fg = p.Yellow
		}
		c.addOn(p.Surface, Span{Text: " " + m.hint, Fg: fg})
		return
	}
	var keys []string
	switch m.tab {
	case TabEpics:
		keys = []string{"↵ drill", "s switch", "o hand-back", "1-9 pick"}
	case TabOverview:
		keys = []string{"↵ focus", "o hand-back"}
	case TabMembers:
		keys = []string{"↵ focus", "1-9 pick", "r retired"}
	case TabMail:
		keys = []string{"/ search", "p filter", "f follow", "␣ excerpt", "o open"}
	case TabGraph:
		keys = []string{"[ ] replay", "L live", "h hide retired", "t mode"}
	}
	if m.DrilledDown() {
		keys = append(keys, "s switch", "esc back", "⇥ tabs", "z zoom", "Q quit")
	} else {
		keys = append(keys, "⇥ tabs", "z zoom", "q hide", "Q quit")
	}
	// Hints that do not fit whole are left out rather than cut.
	spans, used := []Span{{Text: " "}}, 1
	for i, k := range keys {
		sep := ""
		if i > 0 {
			sep = "  "
		}
		key, what, _ := strings.Cut(k, " ")
		w := ansi.StringWidth(sep + k)
		if used+w > c.w {
			break
		}
		used += w
		spans = append(spans, Span{Text: sep}, Span{Text: key, Fg: p.Fg, Bold: true}, Span{Text: " " + what, Fg: p.Faint})
	}
	c.addOn(p.Surface, spans...)
}

func (m Model) waiting(c *canvas) {
	p := m.pal
	c.blank()
	c.add(Span{Text: "  Waiting for rex-amq-adapter…", Fg: p.Muted})
	if m.status.Err != nil {
		c.add(Span{Text: "  " + m.status.Err.Error(), Fg: p.Faint})
		if m.status.RetryIn > 0 {
			c.add(Span{Text: "  retrying in " + m.status.RetryIn.Round(time.Millisecond).String(), Fg: p.Faint})
		}
	}
}

// ago is how long before now t was, in its largest unit.
func ago(now, t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// wrap breaks untrusted text paragraph-by-paragraph into lines of at most w
// cells, preserving explicit newlines and blank lines.
func wrap(text string, w int) []string {
	if w <= 0 {
		return nil
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		out = append(out, wrapLine(para, w)...)
	}
	return out
}

func wrapLine(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	c := clean(s)
	if c == "" {
		return []string{""}
	}
	return strings.Split(ansi.Wrap(c, w, " "), "\n")
}

var blockedOnMe = regexp.MustCompile(`(?i)blocked on me`)
