package ui

import (
	"cmp"
	"fmt"
	"image/color"
	"slices"

	"github.com/est7/rex-amq-ui/internal/adapter"
	"github.com/est7/rex-amq-ui/internal/graph"
)

// graphUsesTUI reports whether the Graph tab should render its zero-temp-file
// Unicode/TUI topology instead of a Kitty graphics image: when toggled with
// `t`, when Kitty graphics are unavailable, when cell pixel metrics are
// missing, or after Kitty delivery fell back on ErrBacklog.
func (m Model) graphUsesTUI() bool {
	return m.graphTUI || m.gfx == nil || !m.gfx.cell.Valid() || m.gfx.stalled
}

type graphTUIEdge struct {
	to     string
	count  int
	style  graph.EdgeStyle
	active bool
}

// graphTUINodes returns EVERY live member (with no MaxActive cap and no +N
// collapsing), followed by every retired member unless m.hideRetired is set.
// Ordering is lead first, then live members by message traffic descending then
// handle ascending, then retired members by handle ascending.
func (m Model) graphTUINodes() []adapter.Agent {
	traffic := map[string]int{}
	for _, msg := range m.graphMessages() {
		traffic[msg.From]++
		for _, to := range msg.To {
			traffic[to]++
		}
	}
	var live, retired []adapter.Agent
	for _, a := range m.st.Agents {
		if m.groupOf(a) == groupRetired {
			if !m.hideRetired {
				retired = append(retired, a)
			}
			continue
		}
		live = append(live, a)
	}
	slices.SortStableFunc(live, func(a, b adapter.Agent) int {
		al, bl := 0, 0
		if m.groupOf(a) == groupLead {
			al = 1
		}
		if m.groupOf(b) == groupLead {
			bl = 1
		}
		return cmp.Or(
			-cmp.Compare(al, bl),
			-cmp.Compare(traffic[a.Handle], traffic[b.Handle]),
			cmp.Compare(a.Handle, b.Handle),
		)
	})
	slices.SortStableFunc(retired, func(a, b adapter.Agent) int {
		return cmp.Compare(a.Handle, b.Handle)
	})
	return append(live, retired...)
}

func (m Model) selectedGraphHandle() string {
	nodes := m.graphTUINodes()
	i := m.sel[TabGraph]
	if i < 0 || i >= len(nodes) {
		return ""
	}
	return nodes[i].Handle
}

func graphMessageEdgeStyle(msg adapter.Message) graph.EdgeStyle {
	switch {
	case msg.Bounced || msg.State == adapter.MsgDeadLettered:
		return graph.StyleAlert
	case msg.State == adapter.MsgDrained:
		return graph.StyleDrained
	default:
		return graph.StyleQueued
	}
}

// graphTUIEdges aggregates outgoing edges and traffic totals up to the replay
// cursor, matching graph.Model's replay, obligation, and hide-retired rules.
func (m Model) graphTUIEdges(nodes []adapter.Agent) (outgoing map[string][]graphTUIEdge, outTotal, inTotal map[string]int) {
	outgoing = map[string][]graphTUIEdge{}
	outTotal = map[string]int{}
	inTotal = map[string]int{}

	msgs := m.graphMessages()
	at, replaying := m.replayIndex()
	if !replaying {
		at = len(msgs) - 1
	}
	activeID := ""
	var shown []adapter.Message
	if at >= 0 && at < len(msgs) {
		shown = msgs[:at+1]
		activeID = msgs[at].ID
	}

	hidden := map[string]bool{}
	if m.hideRetired {
		for _, a := range m.st.Agents {
			if m.groupOf(a) == groupRetired {
				hidden[a.Handle] = true
			}
		}
	}

	type pair struct{ from, to string }
	aggs := map[pair]*graphTUIEdge{}
	add := func(from, to string, count int, style graph.EdgeStyle, active bool) {
		if from == "" || to == "" || from == to {
			return
		}
		k := pair{from, to}
		e, ok := aggs[k]
		if !ok {
			e = &graphTUIEdge{to: to, style: style}
			aggs[k] = e
		}
		e.count += count
		e.style = max(e.style, style)
		if active {
			e.active = true
		}
		outTotal[from] += count
		inTotal[to] += count
	}

	msgIdx := make(map[string]int, len(msgs))
	for i, msg := range msgs {
		msgIdx[msg.ID] = i
	}
	for _, msg := range shown {
		st := graphMessageEdgeStyle(msg)
		act := msg.ID == activeID
		for _, to := range msg.To {
			add(msg.From, to, 1, st, act)
		}
	}

	if v := m.view(); v != nil {
		for _, ob := range v.Obligations {
			if ob.State != "open" {
				continue
			}
			if idx, ok := msgIdx[ob.Ref]; ok {
				if idx <= at {
					msg := msgs[idx]
					for _, to := range msg.To {
						add(msg.From, to, 0, graph.StyleObligation, false)
					}
				}
				continue
			}
			if !replaying && !hidden[ob.OwedTo] && !hidden[ob.OwedBy] {
				add(ob.OwedTo, ob.OwedBy, 0, graph.StyleObligation, false)
			}
		}
	}

	order := make(map[string]int, len(nodes))
	for i, a := range nodes {
		order[a.Handle] = i
	}
	for k, e := range aggs {
		outgoing[k.from] = append(outgoing[k.from], *e)
	}
	for from, list := range outgoing {
		slices.SortFunc(list, func(a, b graphTUIEdge) int {
			oa, oka := order[a.to]
			ob, okb := order[b.to]
			if !oka {
				oa = len(nodes)
			}
			if !okb {
				ob = len(nodes)
			}
			return cmp.Or(cmp.Compare(oa, ob), cmp.Compare(a.to, b.to))
		})
		outgoing[from] = list
	}
	return outgoing, outTotal, inTotal
}

func (m Model) graphTUIActivity(a adapter.Agent, retired bool) (string, color.Color, bool) {
	p := m.pal
	if retired {
		return "retired", p.Faint, true
	}
	switch a.Activity.State {
	case "working":
		return "working", p.Green, false
	case "blocked":
		return "blocked", p.Orange, false
	case "error":
		return "error", p.Red, false
	case "idle", "done":
		return a.Activity.State, p.Muted, false
	default:
		return a.Activity.State, p.Faint, false
	}
}

func (m Model) graphTUIArrow(style graph.EdgeStyle) (arrow string, fg color.Color, bold bool, tag string) {
	p := m.pal
	switch style {
	case graph.StyleObligation:
		return "━━▶", p.Yellow, true, " · open"
	case graph.StyleAlert:
		return "┄┄▶", p.Red, false, " · alert"
	case graph.StyleQueued:
		return "┄┄▶", p.Blue, false, " · queued"
	default:
		return "──▶", p.Green, false, ""
	}
}

func (m Model) graphTUIEntries() []entry {
	p := m.pal
	nodes := m.graphTUINodes()
	if len(nodes) == 0 {
		return nil
	}
	outgoing, outTotal, inTotal := m.graphTUIEdges(nodes)
	retiredSet := map[string]bool{}
	for _, a := range m.st.Agents {
		if m.groupOf(a) == groupRetired {
			retiredSet[a.Handle] = true
		}
	}

	var es []entry
	for i, a := range nodes {
		selected := i == m.sel[TabGraph]
		retired := retiredSet[a.Handle]
		headerBg := p.Bg
		edgeBg := p.Bg
		if selected {
			headerBg = p.Overlay
			edgeBg = p.Surface
		}
		ptr := Span{Text: "  "}
		if selected {
			ptr = Span{Text: "▸ ", Fg: p.Aqua, Bold: true}
		}
		dot, dotFg := m.statusDot(a)
		if retired {
			dot, dotFg = "○", p.Faint
		}
		nameFg := p.Fg
		if retired {
			nameFg = p.Faint
		}
		left := []Span{
			{Text: " "},
			ptr,
			{Text: dot + " ", Fg: dotFg},
			{Text: m.who(a.Handle), Fg: nameFg, Bold: selected},
		}
		if act, actFg, italic := m.graphTUIActivity(a, retired); act != "" {
			left = append(left, Span{Text: "  " + act, Fg: actFg, Italic: italic})
		}

		var right []Span
		if out, in := outTotal[a.Handle], inTotal[a.Handle]; out > 0 || in > 0 {
			right = append(right, Span{Text: fmt.Sprintf(" ↑%d ↓%d ", out, in), Fg: p.Muted})
		} else {
			right = append(right, Span{Text: " · ", Fg: p.Faint})
		}
		if b := m.badges(a); len(b) > 0 {
			right = append(b, right...)
		}
		es = append(es, entry{left: left, right: right, bg: headerBg, row: i})

		edges := outgoing[a.Handle]
		for j, e := range edges {
			branch := "     ├─ "
			if j == len(edges)-1 {
				branch = "     └─ "
			}
			arrow, arrowFg, arrowBold, tag := m.graphTUIArrow(e.style)
			toFg := p.Fg
			if retiredSet[e.to] {
				toFg = p.Faint
			}
			spans := []Span{
				{Text: branch, Fg: p.Faint},
				{Text: arrow, Fg: arrowFg, Bold: arrowBold},
				{Text: " " + m.who(e.to), Fg: toFg},
			}
			if e.count > 0 {
				spans = append(spans, Span{Text: fmt.Sprintf(" ×%d", e.count), Fg: p.Muted})
			}
			if tag != "" {
				spans = append(spans, Span{Text: tag, Fg: arrowFg})
			}
			if e.active {
				spans = append(spans, Span{Text: " ◆", Fg: p.Aqua, Bold: true})
			}
			es = append(es, entry{left: spans, bg: edgeBg, row: i})
		}
	}
	return es
}

func (m Model) renderGraphTUI(c *canvas, regH int) {
	if regH <= 0 {
		return
	}
	p := m.pal
	sub := newCanvas(c.w, regH, p.Bg)
	es := m.graphTUIEntries()
	if len(es) == 0 {
		sub.blank()
		sub.add(Span{Text: " No agents in this session.", Fg: p.Faint, Italic: true})
	} else {
		list(sub, es, m.sel[TabGraph], m.scroll[TabGraph])
	}
	for _, r := range sub.rows() {
		if !c.full() {
			c.lines = append(c.lines, r)
		}
	}
}
