package ui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/est7/rex-amq-ui/internal/adapter"
)

// entry is one line of a list; row is the list row it belongs to, so a
// row may take several lines.
type entry struct {
	left, right []Span
	bg          color.Color
	row         int
}

func rowBounds(es []entry, sel int) (first, last int) {
	first, last = -1, -1
	for i, e := range es {
		if e.row == sel {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	return first, last
}

// listStart is the first visible entry index for a viewport of height lines,
// shared between list rendering and mouse row hit-testing.
func listStart(es []entry, sel, off, height int) int {
	first, last := rowBounds(es, sel)
	start := 0
	if first >= 0 && height > 0 && len(es) > height {
		if rowH := last - first + 1; rowH > height {
			off = min(max(off, 0), rowH-height)
			start = first + off
		} else {
			start = max(first-height/2, 0)
			if last >= start+height {
				start = last - height + 1
			}
			start = min(start, first, len(es)-height)
		}
	}
	return start
}

// list draws entries into c, scrolled to show the selected row: centred
// when it fits in the viewport, or scrolled line-by-line by off when the
// selected row is taller than the viewport.
func list(c *canvas, es []entry, sel, off int) {
	start := listStart(es, sel, off, c.left())
	for _, e := range es[start:] {
		if c.full() {
			return
		}
		c.addLR(e.bg, e.left, e.right)
	}
}

func (m Model) listEntries(t Tab) ([]entry, int) {
	switch t {
	case TabOverview:
		return m.overviewEntries(), m.contentHeight()
	case TabMembers:
		return m.memberEntries(), m.contentHeight()
	case TabMail:
		return m.mailEntries(), max(m.contentHeight()-1, 0)
	case TabEpics:
		return m.epicsEntries(), m.contentHeight()
	}
	return nil, 0
}

func (m Model) maxScroll(t Tab, sel int) int {
	es, n := m.listEntries(t)
	if n <= 0 {
		return 0
	}
	first, last := rowBounds(es, sel)
	if first < 0 {
		return 0
	}
	return max(last-first+1-n, 0)
}

// fit is untrusted text cut or padded to exactly n cells.
func fit(s string, n int) string {
	if n <= 0 {
		return ""
	}
	s = ansi.Truncate(clean(s), n, "…")
	return s + strings.Repeat(" ", max(n-ansi.StringWidth(s), 0))
}

func capsule(text string, fg, bg color.Color) Span {
	return Span{Text: " " + text + " ", Fg: fg, Bg: bg}
}

// overview lists the alerts, one row each, then the narrator, the
// lifecycle, and the hand-back, a row per section, so j/k and G scroll
// through all of it.
func (m Model) overview(c *canvas) { list(c, m.overviewEntries(), m.sel[TabOverview], m.scroll[TabOverview]) }

// overviewSections are the rows below the alerts: narrator, then the
// lifecycle and hand-back when the view has them.
func (m Model) overviewSections() int {
	n := 1
	if v := m.view(); v != nil {
		if v.Lifecycle != nil {
			n++
		}
		if v.HandBack != nil {
			n++
		}
	}
	return n
}

func (m Model) overviewHandBackRow() (int, bool) {
	v := m.view()
	if v == nil || v.HandBack == nil {
		return 0, false
	}
	row := len(m.alerts()) + 1
	if v.Lifecycle != nil {
		row++
	}
	return row, true
}

func (m Model) view() *adapter.SessionView {
	if m.st.Panel == nil {
		return nil
	}
	return m.st.Panel.View
}

func (m Model) overviewEntries() []entry {
	p, now := m.pal, m.now()
	var es []entry
	add := func(row int, bg color.Color, left []Span, right ...Span) {
		es = append(es, entry{left: left, right: right, bg: bg, row: row})
	}
	as := m.alerts()
	if len(as) > 0 {
		add(0, p.Bg, []Span{{Text: fmt.Sprintf(" SYSTEM ALERTS  %d", len(as)), Fg: p.Red, Bold: true}})
		for i, a := range as {
			icon, fg := m.alertIcon(a.kind)
			bg := p.RedBg
			if i == m.sel[TabOverview] {
				bg = p.Overlay
			}
			prefix := " " + icon + " "
			age := Span{Text: " " + ago(now, a.since) + " ", Fg: p.Muted}
			textW := m.innerW() - ansi.StringWidth(prefix) - age.width()
			if textW <= 0 {
				add(i, bg, []Span{{Text: prefix, Fg: fg, Bold: true}, {Text: a.text, Fg: p.Fg}}, age)
				continue
			}
			wrapped := wrapLine(a.text, textW)
			add(i, bg, []Span{{Text: prefix, Fg: fg, Bold: true}, {Text: wrapped[0], Fg: p.Fg}}, age)
			indent := strings.Repeat(" ", ansi.StringWidth(prefix))
			for _, cont := range wrapped[1:] {
				add(i, bg, []Span{{Text: indent + cont, Fg: p.Fg}})
			}
		}
		add(len(as)-1, p.Bg, nil)
	}
	row := len(as)
	v := m.view()
	if v != nil && !v.NoView() && v.Narrative != nil && v.Narrative.Text != "" {
		add(row, p.Bg, []Span{{Text: " from narrator (" + v.Narrator + ") · " + ago(now, v.Narrative.At) + " ago", Fg: p.Muted, Italic: true}})
		for _, l := range wrap(v.Narrative.Text, m.innerW()-2) {
			add(row, p.Bg, []Span{{Text: " " + l, Fg: p.Fg}})
		}
	} else {
		add(row, p.Bg, []Span{{Text: " No narrator summary yet.", Fg: p.Faint, Italic: true}})
	}
	add(row, p.Bg, nil)
	if v != nil && v.Lifecycle != nil {
		row++
		lc := v.Lifecycle
		spans := []Span{{Text: " Epic ", Fg: p.Muted}, {Text: orDash(lc.Name), Fg: p.Fg, Bold: true}, {Text: " "}, m.lifecycleCapsule(lc.State)}
		if !lc.OpenedAt.IsZero() {
			spans = append(spans, Span{Text: " since " + ago(now, lc.OpenedAt), Fg: p.Muted})
		}
		if lc.Crew != "" {
			spans = append(spans, Span{Text: " · crew " + lc.Crew, Fg: p.Muted})
		}
		add(row, p.Bg, spans)
		add(row, p.Bg, nil)
	}
	if v != nil && v.HandBack != nil {
		row++
		for _, e := range m.handBackCard(v.HandBack) {
			e.row = row
			es = append(es, e)
		}
	}
	return es
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func (m Model) alertIcon(k alertKind) (string, color.Color) {
	p := m.pal
	switch k {
	case alertWaiting:
		return "◆", p.Yellow
	case alertTurnError, alertDeadLetter:
		return "✕", p.Red
	case alertStalled:
		return "◷", p.Orange
	case alertBounced:
		return "↩", p.Orange
	}
	return "⧗", p.Blue
}

func (m Model) lifecycleCapsule(state string) Span {
	p := m.pal
	switch state {
	case "open", "opened":
		return capsule(state, p.Green, p.GreenBg)
	case "creating":
		return capsule(state, p.Yellow, p.YellowBg)
	case "closing":
		return capsule(state, p.Orange, p.YellowBg)
	}
	return capsule(orDash(state), p.Muted, p.Surface)
}

// handBackCard renders the full hand-back wrapped paragraph-by-paragraph
// with its Blocked on me section highlighted.
func (m Model) handBackCard(hb *adapter.HandBack) []entry {
	p, bg := m.pal, m.pal.BlueBg
	var es []entry
	add := func(spans ...Span) { es = append(es, entry{left: spans, bg: bg}) }
	title := fmt.Sprintf(" Hand-back #%d · %s · %s ago", hb.Seq, hb.By, ago(m.now(), hb.At))
	es = append(es, entry{left: []Span{{Text: title, Fg: p.Blue, Bold: true}}, right: []Span{{Text: "o", Fg: p.Fg, Bold: true}, {Text: " open ", Fg: p.Muted}}, bg: bg})
	body := strings.ReplaceAll(hb.Body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return es
	}
	w := m.innerW() - 4
	blocked := false
	for _, l := range strings.Split(body, "\n") {
		fg, bold := p.Fg, false
		switch {
		case blockedOnMe.MatchString(l):
			blocked, fg, bold = true, p.Yellow, true
		case blocked && strings.TrimSpace(l) == "", blocked && strings.HasPrefix(strings.TrimSpace(l), "#"):
			blocked = false
		case blocked:
			fg = p.Yellow
		}
		if w <= 0 {
			continue
		}
		for _, wl := range wrapLine(l, w) {
			if wl == "" {
				add()
			} else {
				add(Span{Text: "   " + wl, Fg: fg, Bold: bold})
			}
		}
	}
	return es
}

func (m Model) memberEntries() []entry {
	p := m.pal
	var es []entry
	last := group(-1)
	ms := m.members()
	wide := m.innerW() >= 100
	nameW, modelW := m.memberColumns(ms, wide)
	for i, a := range ms {
		if g := m.groupOf(a); g != last {
			last = g
			if g == groupRetired {
				es = append(es, entry{left: []Span{{Text: fmt.Sprintf(" ▾ Retired %d", m.retiredCount()), Fg: p.Muted, Bold: true}}, bg: p.Bg, row: -1})
			} else {
				es = append(es, entry{left: []Span{{Text: " " + groupNames[g], Fg: p.Muted, Bold: true}}, bg: p.Bg, row: -1})
			}
		}
		es = append(es, m.memberEntry(a, i, wide, nameW, modelW))
	}
	if n := m.retiredCount(); n > 0 && !m.retired {
		es = append(es, entry{left: []Span{{Text: fmt.Sprintf(" ▸ Retired %d", n), Fg: p.Muted, Bold: true}, {Text: "  r to show", Fg: p.Faint}}, bg: p.Bg, row: -1})
	}
	return es
}

func (m Model) membersView(c *canvas) {
	p := m.pal
	ms := m.members()
	if len(ms) == 0 && m.retiredCount() == 0 {
		c.blank()
		c.add(Span{Text: " No agents in this session.", Fg: p.Faint, Italic: true})
		return
	}
	list(c, m.memberEntries(), m.sel[TabMembers], m.scroll[TabMembers])
}

func memberDisplayName(a adapter.Agent) string {
	if a.Role != "" {
		return a.Role + "(" + a.Handle + ")"
	}
	return a.Handle
}

func (m Model) memberColumns(ms []adapter.Agent, wide bool) (nameW, modelW int) {
	maxName, maxModel := 0, 0
	for _, a := range ms {
		if w := ansi.StringWidth(clean(memberDisplayName(a))); w > maxName {
			maxName = w
		}
		if m.groupOf(a) != groupRetired {
			if w := ansi.StringWidth(clean(memberModelText(a, wide))); w > maxModel {
				maxModel = w
			}
		}
	}
	if wide {
		return min(max(maxName, 16), 30), min(max(maxModel, 8), 24)
	}
	nameW = min(max(maxName, 8), 24)
	modelW = min(max(maxModel, 1), 12)
	const rowOverhead = 9 // " " + idx(2) + mark(2) + dot(1) + " "(1) + 2 column spaces
	taskBudget := max(m.innerW()/3, 18)
	if avail := m.innerW() - rowOverhead - taskBudget; avail > 0 && nameW+modelW > avail {
		modelW = min(modelW, max(avail/3, 6))
		nameW = max(min(nameW, avail-modelW), min(8, avail))
		if nameW+modelW > avail {
			modelW = max(avail-nameW, 1)
		}
	}
	return nameW, modelW
}

func compactEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "high":
		return "hi"
	case "medium":
		return "med"
	case "low":
		return "lo"
	case "xhigh":
		return "xhi"
	case "minimal":
		return "min"
	default:
		return strings.TrimSpace(effort)
	}
}

func isVersionToken(tok string) bool {
	if tok == "" {
		return false
	}
	hasDigit := false
	for _, r := range tok {
		if r >= '0' && r <= '9' {
			hasDigit = true
			continue
		}
		if r == '.' {
			continue
		}
		return false
	}
	return hasDigit
}

func isEffortToken(tok string) bool {
	switch strings.ToLower(tok) {
	case "high", "medium", "low", "xhigh", "minimal":
		return true
	}
	return false
}

func startsWithLetter(s string) bool {
	if s == "" {
		return false
	}
	b := s[0]
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func compactModelName(model, effort string) string {
	m := strings.TrimSpace(model)
	if m == "" {
		return ""
	}
	if i := strings.LastIndex(m, "/"); i >= 0 && i+1 < len(m) {
		m = m[i+1:]
	}
	tokens := strings.Split(m, "-")
	if strings.TrimSpace(effort) != "" && len(tokens) > 1 && isEffortToken(tokens[len(tokens)-1]) {
		tokens = tokens[:len(tokens)-1]
	}
	for len(tokens) > 1 {
		last := strings.ToLower(tokens[len(tokens)-1])
		if last == "latest" || last == "preview" || (len(last) == 8 && isVersionToken(last)) {
			tokens = tokens[:len(tokens)-1]
			continue
		}
		break
	}
	if len(tokens) <= 1 {
		return strings.Join(tokens, "-")
	}
	switch strings.ToLower(tokens[0]) {
	case "claude", "gemini":
		var kept []string
		for _, tok := range tokens[1:] {
			if !isVersionToken(tok) {
				kept = append(kept, tok)
			}
		}
		if len(kept) > 0 {
			return strings.Join(kept, "-")
		}
	case "gpt":
		var named []string
		for _, tok := range tokens[1:] {
			if !isVersionToken(tok) && startsWithLetter(tok) {
				named = append(named, tok)
			}
		}
		if len(named) > 0 {
			return strings.Join(named, "-")
		}
	}
	return strings.Join(tokens, "-")
}

func memberModelText(a adapter.Agent, wide bool) string {
	if wide {
		model := a.Model
		if a.Effort != "" {
			model += "/" + a.Effort
		}
		return orDash(model)
	}
	model := compactModelName(a.Model, a.Effort)
	eff := compactEffort(a.Effort)
	switch {
	case model != "" && eff != "":
		return model + "/" + eff
	case model != "":
		return model
	case eff != "":
		return eff
	default:
		return "—"
	}
}

func (m Model) memberEntry(a adapter.Agent, i int, wide bool, nameW, modelW int) entry {
	p := m.pal
	bg := p.Bg
	if i == m.sel[TabMembers] {
		bg = p.Overlay
	}
	idx := "  "
	if i < 9 {
		idx = fmt.Sprintf("%d ", i+1)
	}
	mark := Span{Text: "  "}
	if !m.DrilledDown() && a.BlockID != "" && a.BlockID == m.lay.Focused {
		mark = Span{Text: "▶ ", Fg: p.Aqua, Bold: true}
	}
	dot, dotFg := m.statusDot(a)
	name := memberDisplayName(a)
	retired := m.groupOf(a) == groupRetired
	nameFg := p.Fg
	if retired {
		nameFg = p.Faint
	}
	left := []Span{{Text: " " + idx, Fg: p.Faint}, mark, {Text: dot + " ", Fg: dotFg}, {Text: fit(name, nameW), Fg: nameFg, Bold: i == m.sel[TabMembers]}}
	if retired {
		reason := orDash(a.Lifecycle.DepartReason)
		if a.Lifecycle.DepartedAt != nil {
			reason += " · " + ago(m.now(), *a.Lifecycle.DepartedAt) + " ago"
		}
		left = append(left, Span{Text: " " + reason, Fg: p.Faint, Italic: true})
		return entry{left: left, bg: bg, row: i}
	}
	left = append(left, Span{Text: " " + fit(memberModelText(a, wide), modelW), Fg: p.Muted})
	if wide {
		left = append(left, Span{Text: " " + fit(a.Tab, 10), Fg: p.Faint})
	}
	left = append(left, Span{Text: " " + a.Task, Fg: p.Fg})
	return entry{left: left, right: m.badges(a), bg: bg, row: i}
}

func (m Model) statusDot(a adapter.Agent) (string, color.Color) {
	p := m.pal
	switch a.Activity.State {
	case "working":
		return "●", p.Green
	case "blocked":
		return "●", p.Orange
	case "error":
		return "●", p.Red
	case "idle", "done":
		return "○", p.Muted
	}
	return "·", p.Faint
}

func (m Model) badges(a adapter.Agent) []Span {
	p := m.pal
	var out []Span
	if m.st.Panel != nil && len(m.st.Panel.Attention[a.Handle]) > 0 {
		out = append(out, Span{Text: " !" + fmt.Sprint(len(m.st.Panel.Attention[a.Handle])), Fg: p.Red, Bold: true})
	}
	if a.Delivery.PendingDoorbell {
		out = append(out, Span{Text: " ⍾", Fg: p.Yellow})
	}
	if n := a.Delivery.Unread; n > 0 {
		out = append(out, Span{Text: fmt.Sprintf(" ✉%d", n), Fg: p.Yellow, Bold: true})
	}
	if out != nil {
		out = append(out, Span{Text: " "})
	}
	return out
}

func (m Model) mailEntries() []entry {
	p := m.pal
	rows := m.mailRows()
	var es []entry
	for i, msg := range rows {
		bg := p.Bg
		if i == m.sel[TabMail] {
			bg = p.Overlay
		}
		ex, can := m.excerpt(msg.ID)
		open := can && m.mail.expanded[msg.ID]
		mark := "  "
		switch {
		case open:
			mark = "▾ "
		case can:
			mark = "▸ "
		}
		left := []Span{{Text: " " + fit(ago(m.now(), msg.CreatedAt), 4), Fg: p.Faint}, {Text: mark, Fg: p.Blue},
			{Text: msg.From, Fg: p.Fg}, {Text: " → ", Fg: p.Faint}, {Text: recipients(msg), Fg: p.Fg}, {Text: " "},
			m.mailCapsule(msg), {Text: " " + msg.Subject, Fg: p.Fg, Bold: i == m.sel[TabMail]}}
		es = append(es, entry{left: left, bg: bg, row: i})
		if open {
			for _, l := range wrap(ex.Body, m.innerW()-8) {
				if l == "" {
					es = append(es, entry{bg: p.Surface, row: i})
				} else {
					es = append(es, entry{left: []Span{{Text: "       " + l, Fg: p.Muted}}, bg: p.Surface, row: i})
				}
			}
			if ex.Truncated {
				es = append(es, entry{left: []Span{{Text: "       …", Fg: p.Faint}}, bg: p.Surface, row: i})
			}
		}
	}
	return es
}

func (m Model) mailView(c *canvas) {
	p := m.pal
	bar := []Span{{Text: " "}}
	switch {
	case m.mail.typing:
		bar = append(bar, Span{Text: "/ ", Fg: p.Blue, Bold: true}, Span{Text: m.mail.query, Fg: p.Fg}, Span{Text: "▏", Fg: p.Blue})
	case m.mail.query != "":
		bar = append(bar, capsule("/ "+m.mail.query, p.Blue, p.BlueBg), Span{Text: " "})
	}
	if m.mail.from != "" {
		bar = append(bar, capsule("from/to "+m.mail.from, p.Aqua, p.Surface), Span{Text: " "})
	}
	follow := capsule("Follow", p.Faint, p.Surface)
	if m.mail.follow {
		follow = capsule("Follow", p.Green, p.GreenBg)
	}
	c.addLR(p.Bg, bar, []Span{follow, {Text: " "}})
	es := m.mailEntries()
	if len(es) == 0 {
		c.blank()
		c.add(Span{Text: " No messages.", Fg: p.Faint, Italic: true})
		return
	}
	list(c, es, m.sel[TabMail], m.scroll[TabMail])
}

func (m Model) mailCapsule(msg adapter.Message) Span {
	p := m.pal
	switch {
	case msg.State == adapter.MsgDeadLettered:
		return capsule("dead_lettered", p.Red, p.RedBg)
	case msg.Bounced:
		return capsule("bounced", p.Orange, p.RedBg)
	case msg.State == adapter.MsgDrained:
		return capsule("drained", p.Green, p.GreenBg)
	case msg.State == adapter.MsgQueued:
		return capsule("queued", p.Yellow, p.YellowBg)
	}
	return capsule(orDash(msg.State), p.Blue, p.BlueBg)
}

// GraphRegion is the rectangle of the frame the Graph tab leaves to the
// graph renderer, painted with the background until it draws there: the
// rows between the replay bar and the legend, inside the panel border.
func (m Model) GraphRegion() Rect {
	if !m.framed() {
		return Rect{}
	}
	return Rect{X: borderWidth, Y: borderWidth + headerLines + 1, W: m.innerW(), H: max(m.contentHeight()-3, 0)}
}

func (m Model) graphView(c *canvas) {
	p := m.pal
	msgs := m.graphMessages()
	at, replaying := m.replayIndex()
	if !replaying {
		at = len(msgs) - 1
	}
	live := capsule("● Live", p.Faint, p.Surface)
	if !replaying {
		live = capsule("● Live", p.Green, p.GreenBg)
	}
	hide := "○"
	if m.hideRetired {
		hide = "●"
	}
	span := ""
	if len(msgs) > 1 {
		span = " · " + compact(msgs[len(msgs)-1].CreatedAt.Sub(msgs[0].CreatedAt))
	}
	c.addLR(p.Surface,
		[]Span{{Text: " |<  <  >  ", Fg: p.Fg, Bold: true}, live, {Text: "  Hide retired " + hide, Fg: p.Muted}},
		[]Span{{Text: fmt.Sprintf("%d / %d messages%s ", at+1, len(msgs), span), Fg: p.Muted}})
	for range m.GraphRegion().H {
		c.blank()
	}
	c.add(Span{Text: " ──", Fg: p.Green}, Span{Text: " drained  ", Fg: p.Muted},
		Span{Text: "━━", Fg: p.Yellow, Bold: true}, Span{Text: " obligation  ", Fg: p.Muted},
		Span{Text: "┄", Fg: p.Blue}, Span{Text: "┄", Fg: p.Red}, Span{Text: " queued · bounced", Fg: p.Muted})
	if at >= 0 {
		cur := msgs[at]
		c.add(Span{Text: " " + cur.From + " → " + recipients(cur), Fg: p.Fg}, Span{Text: " · " + cur.Subject, Fg: p.Muted})
	}
}

// compact is d as 4m30s, 2h05m, or 3d.
func compact(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func (m Model) epicsEntries() []entry {
	p := m.pal
	var out []entry
	for i, e := range m.epics() {
		bg := p.Bg
		if i == m.sel[TabEpics] {
			bg = p.Overlay
		}
		idx := "  "
		if i < 9 {
			idx = fmt.Sprintf("%d ", i+1)
		}
		state := ""
		if e.Lifecycle != nil {
			state = e.Lifecycle.State
		}
		var right []Span
		if e.HandBack != nil {
			right = append(right, capsule("hand-back", p.Blue, p.BlueBg), Span{Text: " "})
		}
		if e.OpenAttention > 0 {
			right = append(right, Span{Text: fmt.Sprintf("!%d ", e.OpenAttention), Fg: p.Red, Bold: true})
		}
		if e.OpenObligations > 0 {
			right = append(right, Span{Text: fmt.Sprintf("⧗%d ", e.OpenObligations), Fg: p.Blue})
		}
		right = append(right, Span{Text: fmt.Sprintf("%d active ", e.ActiveMembers), Fg: p.Muted})
		out = append(out, entry{left: []Span{{Text: " " + idx, Fg: p.Faint}, {Text: orDash(e.Name), Fg: p.Fg, Bold: true}, {Text: " "}, m.lifecycleCapsule(state)}, right: right, bg: bg, row: i})
		out = append(out, entry{left: []Span{{Text: "   " + e.Dir, Fg: p.Faint}}, bg: bg, row: i})
		say := Span{Text: "   no narrator yet", Fg: p.Faint, Italic: true}
		if e.Narrative != nil && e.Narrative.Text != "" {
			say = Span{Text: "   " + e.Narrative.Text, Fg: p.Muted, Italic: true}
		}
		out = append(out, entry{left: []Span{say}, bg: bg, row: i})
		out = append(out, entry{bg: p.Bg, row: i})
	}
	return out
}

func (m Model) epicsView(c *canvas) {
	p := m.pal
	if len(m.epics()) == 0 {
		c.blank()
		c.add(Span{Text: " No Epics are linked to this personal agent yet.", Fg: p.Faint, Italic: true})
		return
	}
	list(c, m.epicsEntries(), m.sel[TabEpics], m.scroll[TabEpics])
}
