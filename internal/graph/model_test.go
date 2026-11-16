package graph_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/est7/rex-amq-ui/internal/adapter"
	"github.com/est7/rex-amq-ui/internal/graph"
	"github.com/est7/rex-amq-ui/internal/raster"
)

var t0 = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newModel() (*graph.Model, *clock) {
	c := &clock{now: t0.Add(time.Hour)}
	return graph.New(c.Now), c
}

func agent(handle, role, activity string) adapter.Agent {
	return adapter.Agent{
		Handle: handle, Kind: "claude", Role: role, Pane: "present",
		Lifecycle: adapter.Lifecycle{State: adapter.LifeLive, AdoptedAt: t0},
		Activity:  adapter.Activity{State: activity},
	}
}

func retired(handle, role string, at time.Time) adapter.Agent {
	a := agent(handle, role, "done")
	a.Lifecycle.State = adapter.LifeDeparted
	a.Lifecycle.DepartedAt = &at
	a.Pane = "gone"
	return a
}

// msg is a message at minute min of the session; id orders differently
// from time on purpose in some tests.
func msg(id string, min int, from string, to []string, state string) adapter.Message {
	return adapter.Message{ID: id, From: from, To: to, Subject: "subject " + id, State: state, CreatedAt: t0.Add(time.Duration(min) * time.Minute)}
}

func state(agents []adapter.Agent, msgs []adapter.Message, obligations ...adapter.Obligation) adapter.State {
	st := adapter.State{Stream: "s1", Seq: 1, Agents: slices.Clone(agents), Messages: slices.Clone(msgs)}
	slices.SortFunc(st.Agents, func(a, b adapter.Agent) int { return strings.Compare(a.Handle, b.Handle) })
	slices.SortFunc(st.Messages, func(a, b adapter.Message) int { return strings.Compare(a.ID, b.ID) })
	st.Panel = &adapter.Panel{SessionLabel: "epic", View: &adapter.SessionView{Obligations: obligations}}
	return st
}

func to(h ...string) []string { return h }

var board = graph.Size{Cols: 150, Rows: 34, Cell: raster.Metrics{CellW: 9, CellH: 22}}

func nodeIDs(s graph.Scene) []string {
	var ids []string
	for _, n := range s.Nodes {
		ids = append(ids, n.ID)
	}
	return ids
}

func node(t *testing.T, s graph.Scene, id string) graph.Node {
	t.Helper()
	for _, n := range s.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("no node %q in %v", id, nodeIDs(s))
	return graph.Node{}
}

func edge(s graph.Scene, from, to string) (graph.Edge, bool) {
	for _, e := range s.Edges {
		if e.From == from && e.To == to {
			return e, true
		}
	}
	return graph.Edge{}, false
}

func mustEdge(t *testing.T, s graph.Scene, from, to string) graph.Edge {
	t.Helper()
	e, ok := edge(s, from, to)
	if !ok {
		t.Fatalf("no edge %s->%s in %v", from, to, edgeNames(s))
	}
	return e
}

func edgeNames(s graph.Scene) []string {
	var out []string
	for _, e := range s.Edges {
		out = append(out, fmt.Sprintf("%s->%s x%d style %d", e.From, e.To, e.Count, e.Style))
	}
	return out
}

func TestSceneSixActiveAndFold(t *testing.T) {
	agents := []adapter.Agent{agent("orch", "orchestrator", "idle")}
	var msgs []adapter.Message
	// a1..a7 get 7..1 messages from orch, so a7 has the fewest.
	for i := 1; i <= 7; i++ {
		h := fmt.Sprintf("a%d", i)
		agents = append(agents, agent(h, "implementer", "idle"))
		for k := 0; k < 8-i; k++ {
			msgs = append(msgs, msg(fmt.Sprintf("m%d-%d", i, k), i*10+k, "orch", to(h), adapter.MsgDrained))
		}
	}
	msgs = append(msgs, msg("x1", 99, "a7", to("orch"), adapter.MsgDrained), msg("x2", 98, "est9", to("orch"), adapter.MsgQueued))
	m, _ := newModel()
	m.Update(state(agents, msgs))
	s := m.Scene(board)

	want := []string{"orch", "a1", "a2", "a3", "a4", "a5", "+3"}
	if got := nodeIDs(s); !slices.Equal(got, want) {
		t.Fatalf("nodes %v, want %v: lead first, then the five busiest, the rest folded into +N", got, want)
	}
	more := node(t, s, "+3")
	if more.Kind != graph.KindMore || more.Label != "+3" {
		t.Errorf("fold node %+v, want KindMore labelled +3 (a6, a7, and est9 who is not an agent)", more)
	}
	if e := mustEdge(t, s, "orch", "+3"); e.Count != 3 {
		t.Errorf("orch->+3 count %d, want 3 (2 to a6, 1 to a7)", e.Count)
	}
	if e := mustEdge(t, s, "+3", "orch"); e.Count != 2 || e.Style != graph.StyleQueued {
		t.Errorf("+3->orch %+v, want count 2 styled queued (a7 drained, est9 queued)", e)
	}
}

func TestSceneLeadFromCrew(t *testing.T) {
	// A crew definition names its lead by role.
	agents := []adapter.Agent{agent("a", "implementer", "idle"), agent("b", "reviewer", "idle"), agent("o", "orchestrator", "idle")}
	st := state(agents, nil)
	st.Panel.Crew = &adapter.Crew{Crew: "c", Lead: "reviewer"}
	m, _ := newModel()
	m.Update(st)
	if got := nodeIDs(m.Scene(board)); got[0] != "b" {
		t.Errorf("nodes %v: the member whose role is the crew's lead goes first", got)
	}
}

func TestSceneEdgeStyles(t *testing.T) {
	agents := []adapter.Agent{agent("o", "orchestrator", "working"), agent("i", "implementer", "idle"), agent("r", "reviewer", "blocked"), agent("w", "writer", "idle")}
	bounced := msg("m5", 5, "r", to("o"), adapter.MsgQueued)
	bounced.Bounced = true
	msgs := []adapter.Message{
		msg("m1", 1, "o", to("i", "r"), adapter.MsgDrained),
		msg("m2", 2, "o", to("i"), adapter.MsgDrained),
		msg("m3", 3, "o", to("i"), adapter.MsgDrained),
		msg("m4", 4, "i", to("o"), adapter.MsgSent),
		bounced,
		msg("m6", 6, "w", to("o"), adapter.MsgDeadLettered),
		msg("m7", 7, "o", to("w"), adapter.MsgDrained),
		msg("m8", 8, "o", to("o"), adapter.MsgDrained),
		msg("m9", 9, "r", to("i"), adapter.MsgDrained),
		msg("m10", 10, "r", to("i"), adapter.MsgQueued),
	}
	ob := adapter.Obligation{Ref: "m7", Kind: "todo", OwedBy: "w", OwedTo: "o", State: "open"}
	m, _ := newModel()
	m.Update(state(agents, msgs, ob))
	s := m.Scene(board)

	cases := []struct {
		from, to string
		count    int
		style    graph.EdgeStyle
		why      string
	}{
		{"o", "i", 3, graph.StyleDrained, "three drained, one recipient of m1 among them"},
		{"o", "r", 1, graph.StyleDrained, "m1's second recipient"},
		{"i", "o", 1, graph.StyleQueued, "sent is not drained yet"},
		{"r", "o", 1, graph.StyleAlert, "bounced"},
		{"w", "o", 1, graph.StyleAlert, "dead lettered"},
		{"o", "w", 1, graph.StyleObligation, "its message opened an open obligation"},
		{"r", "i", 2, graph.StyleQueued, "queued outranks drained on one edge"},
	}
	for _, c := range cases {
		e := mustEdge(t, s, c.from, c.to)
		if e.Count != c.count || e.Style != c.style {
			t.Errorf("%s->%s count %d style %d, want %d style %d (%s)", c.from, c.to, e.Count, e.Style, c.count, c.style, c.why)
		}
	}
	if len(s.Edges) != len(cases) {
		t.Errorf("edges %v: a self message draws none", edgeNames(s))
	}
	// Heavier edges are thicker; obligations thicker than their weight.
	if a, b := mustEdge(t, s, "o", "i"), mustEdge(t, s, "o", "r"); a.Width <= b.Width {
		t.Errorf("3-message edge width %.2f not above 1-message %.2f", a.Width, b.Width)
	}
	if a, b := mustEdge(t, s, "o", "w"), mustEdge(t, s, "o", "r"); a.Width <= b.Width {
		t.Errorf("obligation edge width %.2f not above plain 1-message %.2f", a.Width, b.Width)
	}
	if n := node(t, s, "o"); n.Halo <= 0 {
		t.Errorf("working node halo %v, want > 0", n.Halo)
	}
	if n := node(t, s, "r"); n.Halo != 0 {
		t.Errorf("blocked node halo %v, want none (only working breathes)", n.Halo)
	}
}

func TestSceneObligationWithoutOpenerInLiveOnly(t *testing.T) {
	agents := []adapter.Agent{agent("o", "orchestrator", "idle"), agent("i", "implementer", "idle")}
	msgs := []adapter.Message{msg("m1", 1, "i", to("o"), adapter.MsgDrained), msg("m2", 2, "i", to("o"), adapter.MsgDrained)}
	ob := adapter.Obligation{Ref: "gone", OwedBy: "i", OwedTo: "o", State: "open"}
	m, _ := newModel()
	m.Update(state(agents, msgs, ob))
	e := mustEdge(t, m.Scene(board), "o", "i")
	if e.Style != graph.StyleObligation || e.Count != 0 {
		t.Errorf("o->i %+v, want an obligation edge (owed_to -> owed_by, the opener's direction) with no messages", e)
	}
	m.Show("m1", false)
	if _, ok := edge(m.Scene(board), "o", "i"); ok {
		t.Errorf("replay shows an obligation whose opener it cannot place in time")
	}
}

func TestSceneHideRetired(t *testing.T) {
	leaving := agent("bye", "writer", "idle")
	leaving.Lifecycle.State = adapter.LifeDeparting
	agents := []adapter.Agent{
		agent("o", "orchestrator", "idle"), agent("i", "implementer", "idle"), leaving,
		retired("old1", "implementer", t0.Add(1*time.Minute)), retired("old2", "reviewer", t0.Add(3*time.Minute)), retired("old3", "writer", t0.Add(2*time.Minute)),
	}
	msgs := []adapter.Message{
		msg("m1", 1, "o", to("old1"), adapter.MsgDrained), msg("m2", 2, "old2", to("o"), adapter.MsgDrained), msg("m3", 3, "o", to("i"), adapter.MsgDrained),
		msg("m4", 4, "o", to("i", "old2"), adapter.MsgDrained),
	}
	m, _ := newModel()
	m.Update(state(agents, msgs))
	s := m.Scene(board)
	if got, want := nodeIDs(s), []string{"o", "i", "bye", "old2", "+2"}; !slices.Equal(got, want) {
		t.Fatalf("nodes %v, want %v: departing counts as retired; the two latest retired shown, the rest folded", got, want)
	}
	if n := node(t, s, "old2"); n.Kind != graph.KindRetired {
		t.Errorf("old2 kind %v, want KindRetired", n.Kind)
	}
	if n := node(t, s, "bye"); n.Kind != graph.KindRetired {
		t.Errorf("departing bye kind %v, want KindRetired", n.Kind)
	}
	m.Show("", true)
	s = m.Scene(board)
	if got, want := nodeIDs(s), []string{"o", "i"}; !slices.Equal(got, want) {
		t.Fatalf("hidden retired: nodes %v, want %v", got, want)
	}
	// Like the panel's replay list, a message with any retired end is left
	// out whole: m4 to i and old2 does not count on o->i.
	if e := mustEdge(t, s, "o", "i"); e.Count != 1 || len(s.Edges) != 1 {
		t.Errorf("hidden retired: edges %v, want only o->i x1", edgeNames(s))
	}
}

func TestReplayCursor(t *testing.T) {
	agents := []adapter.Agent{agent("o", "orchestrator", "idle"), agent("i", "implementer", "idle")}
	// IDs sort z, y, x, w but time runs w, x, y, z.
	msgs := []adapter.Message{
		msg("z", 40, "i", to("o"), adapter.MsgQueued),
		msg("y", 30, "o", to("i"), adapter.MsgDrained),
		msg("x", 12, "i", to("o"), adapter.MsgDrained),
		msg("w", 10, "o", to("i"), adapter.MsgDrained),
	}
	m, _ := newModel()
	m.Update(state(agents, msgs))
	if e := mustEdge(t, m.Scene(board), "i", "o"); e.Count != 2 || e.Style != graph.StyleQueued {
		t.Fatalf("live i->o %+v, want x and z (queued)", e)
	}
	m.Show("y", false)
	if e := mustEdge(t, m.Scene(board), "i", "o"); e.Count != 1 || e.Style != graph.StyleDrained {
		t.Errorf("at y, i->o %+v: z is after the cursor, x before it", e)
	}
	m.Show("w", false)
	if _, ok := edge(m.Scene(board), "i", "o"); ok {
		t.Errorf("at w (the first in time) only o->i shows")
	}
	// The cursor's message leaves the state: back to Live, no panic.
	m.Update(state(agents, []adapter.Message{msgs[0], msgs[1], msgs[2]}))
	if e := mustEdge(t, m.Scene(board), "i", "o"); e.Count != 2 {
		t.Errorf("after the cursor's message left, i->o %+v, want Live (x and z)", e)
	}
	m.Show("gone", false)
	if e := mustEdge(t, m.Scene(board), "i", "o"); e.Count != 2 {
		t.Errorf("an unknown cursor %+v, want Live", e)
	}
}

func TestReplayEmpty(t *testing.T) {
	m, _ := newModel()
	m.Update(state([]adapter.Agent{agent("o", "orchestrator", "idle")}, nil))
	m.Show("anything", false)
	if s := m.Scene(board); len(s.Nodes) != 1 || len(s.Edges) != 0 {
		t.Errorf("empty scene nodes %v edges %v", nodeIDs(s), edgeNames(s))
	}
	if m.NeedsAnimation() {
		t.Errorf("an empty graph animates")
	}
}

func TestReplayAnimation(t *testing.T) {
	agents := []adapter.Agent{agent("o", "orchestrator", "idle"), agent("i", "implementer", "idle")}
	msgs := []adapter.Message{msg("m1", 1, "o", to("i"), adapter.MsgDrained), msg("m2", 2, "i", to("o"), adapter.MsgDrained)}
	m, c := newModel()
	m.Update(state(agents, msgs))
	if m.NeedsAnimation() {
		t.Fatalf("first state animates: existing mail must not pulse")
	}
	if s := m.Scene(board); len(s.Pulses) != 0 {
		t.Fatalf("first state pulses %v", s.Pulses)
	}

	msgs = append(msgs, msg("m3", 3, "o", to("i"), adapter.MsgQueued))
	m.Update(state(agents, msgs))
	if !m.NeedsAnimation() {
		t.Fatalf("new live mail does not animate")
	}
	s := m.Scene(board)
	if len(s.Pulses) != 1 || s.Edges[s.Pulses[0].Edge].From != "o" || s.Pulses[0].T != 0 {
		t.Fatalf("pulses %+v, want one at the start of o->i", s.Pulses)
	}
	c.Advance(600 * time.Millisecond)
	if p := m.Scene(board).Pulses; len(p) != 1 || p[0].T < 0.45 || p[0].T > 0.55 {
		t.Errorf("pulse halfway through its life at %+v, want T about 0.5", p)
	}
	c.Advance(700 * time.Millisecond)
	if m.NeedsAnimation() || len(m.Scene(board).Pulses) != 0 {
		t.Fatalf("pulse outlives its 1.2s")
	}

	m.Show("m2", false)
	if !m.NeedsAnimation() {
		t.Errorf("a cursor step does not animate its message")
	}
	c.Advance(2 * time.Second)
	if m.NeedsAnimation() {
		t.Errorf("idle after the step's pulse ended still animates")
	}
	// Mail during replay is not drawn, so it does not pulse.
	m.Update(state(agents, append(msgs, msg("m4", 4, "i", to("o"), adapter.MsgQueued))))
	if m.NeedsAnimation() {
		t.Errorf("mail beyond the replay cursor animates")
	}

	agents[1].Activity.State = "working"
	m.Update(state(agents, msgs))
	if !m.NeedsAnimation() {
		t.Errorf("a working member does not breathe")
	}
	h1 := node(t, m.Scene(board), "i").Halo
	c.Advance(400 * time.Millisecond)
	if h2 := node(t, m.Scene(board), "i").Halo; h1 == h2 {
		t.Errorf("halo %v does not change over time", h1)
	}
	m.SetPaused(true)
	if m.NeedsAnimation() {
		t.Errorf("paused (minimized or another tab) still animates")
	}
	m.SetPaused(false)
	if !m.NeedsAnimation() {
		t.Errorf("resume does not animate the working member again")
	}
}

func TestReplayPulseHiddenEndpoint(t *testing.T) {
	agents := []adapter.Agent{agent("o", "orchestrator", "idle"), retired("old", "implementer", t0)}
	m, _ := newModel()
	m.Update(state(agents, nil))
	m.Show("", true)
	m.Update(state(agents, []adapter.Message{msg("m1", 1, "o", to("old"), adapter.MsgDrained)}))
	if m.NeedsAnimation() {
		t.Errorf("a pulse along a hidden edge animates")
	}
}

func TestSceneSelect(t *testing.T) {
	m, _ := newModel()
	m.Update(state([]adapter.Agent{agent("o", "orchestrator", "idle"), agent("i", "implementer", "idle")}, nil))
	m.Select("i")
	if m.Selected() != "i" || !node(t, m.Scene(board), "i").Selected || node(t, m.Scene(board), "o").Selected {
		t.Errorf("selection not reflected")
	}
}

func TestReplayNoBurstAfterEmptyState(t *testing.T) {
	m, _ := newModel()
	m.Update(adapter.State{})
	m.Update(state([]adapter.Agent{agent("o", "orchestrator", "idle"), agent("i", "implementer", "idle")},
		[]adapter.Message{msg("m1", 1, "o", to("i"), adapter.MsgDrained), msg("m2", 2, "i", to("o"), adapter.MsgDrained)}))
	if m.NeedsAnimation() {
		t.Errorf("the first snapshot after an empty state pulses its history")
	}
}

func TestSceneObligationFollowsOpener(t *testing.T) {
	// The opener's own edge carries the obligation, whatever owed_by and
	// owed_to say.
	agents := []adapter.Agent{agent("o", "orchestrator", "idle"), agent("i", "implementer", "idle")}
	ob := adapter.Obligation{Ref: "m1", OwedBy: "o", OwedTo: "i", State: "open"}
	m, _ := newModel()
	m.Update(state(agents, []adapter.Message{msg("m1", 1, "o", to("i"), adapter.MsgDrained)}, ob))
	s := m.Scene(board)
	if e := mustEdge(t, s, "o", "i"); e.Style != graph.StyleObligation {
		t.Errorf("opener edge o->i style %d, want obligation", e.Style)
	}
	if _, ok := edge(s, "i", "o"); ok {
		t.Errorf("an i->o edge appears though no mail went that way")
	}
}

func TestReplayLiveRevealsPulses(t *testing.T) {
	agents := []adapter.Agent{agent("o", "orchestrator", "idle"), agent("i", "implementer", "idle")}
	msgs := []adapter.Message{msg("m1", 1, "o", to("i"), adapter.MsgDrained), msg("m2", 2, "i", to("o"), adapter.MsgDrained), msg("m3", 3, "o", to("i"), adapter.MsgDrained)}
	m, c := newModel()
	m.Update(state(agents, msgs))
	m.Show("m1", false)
	c.Advance(2 * time.Second)
	m.Show("m1", false)
	if m.NeedsAnimation() {
		t.Fatalf("showing the same cursor again pulses")
	}
	m.Show("", false)
	if got := len(m.Scene(board).Pulses); got != 2 {
		t.Errorf("back to Live pulses %d messages, want the 2 it reveals", got)
	}
}
