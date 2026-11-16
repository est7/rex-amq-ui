// Package graph turns the panel's adapter.State into the Graph tab's
// picture: who talks to whom, how much, and in what state, replayable one
// message at a time.
//
// The panel owns the replay cursor and the Hide-retired toggle and hands
// them over with Show; Model owns the selection, the pulses and the
// animation clock. Scene projects it, at a given pixel size, into placed
// nodes and curved edges; Paint draws a Scene on a raster.Canvas.
package graph

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/est7/rex-amq-ui/internal/adapter"
)

const (
	// MaxActive is how many live members get their own node; the rest
	// fold into the "+N" node.
	MaxActive = 6
	// maxRetired is how many departed members get their own (dimmed)
	// node while retired members are shown.
	maxRetired = 2

	pulseLife    = 1200 * time.Millisecond
	breathPeriod = 1600 * time.Millisecond
	// maxPulses bounds the pulses kept at once: a burst of mail pulses
	// its newest messages only.
	maxPulses = 32
	// revealPulses is how many of the messages a jump back to Live
	// reveals get a pulse.
	revealPulses = 8
)

// Model is the Graph tab's state. Its zero value is not usable; use New.
type Model struct {
	clock func() time.Time
	epoch time.Time

	st adapter.State
	// msgs is st.Messages in time order (CreatedAt, then ID).
	msgs []adapter.Message
	// index is each message's place in msgs.
	index map[string]int
	ready bool

	// at is the replay cursor, the id of the last message shown; "" (or an
	// id no longer in msgs) is Live.
	at string

	hideRetired bool
	paused      bool
	selected    string
	pulses      []pulse
}

type pulse struct {
	msg adapter.Message
	at  time.Time
}

// New is a Model reading time from clock. It starts Live.
func New(clock func() time.Time) *Model {
	return &Model{clock: clock, epoch: clock()}
}

// Update takes the latest state. In Live, mail not seen before pulses
// along its edges, except in the first snapshot, whose mail is history; a
// state before any snapshot (adapter.State.Ready false) does not count as
// one. In replay, the cursor stays on its message; when that message
// leaves the state, the graph is Live.
func (m *Model) Update(st adapter.State) {
	now := m.clock()
	msgs := slices.Clone(st.Messages)
	slices.SortStableFunc(msgs, func(a, b adapter.Message) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	if m.ready && m.live() {
		for _, x := range msgs {
			if _, seen := m.index[x.ID]; !seen {
				m.pulse(x, now)
			}
		}
	}
	m.index = make(map[string]int, len(msgs))
	for i, x := range msgs {
		m.index[x.ID] = i
	}
	m.st, m.msgs, m.ready = st, msgs, st.Ready()
	if m.live() {
		m.at = ""
	}
	m.prune(now)
}

// live reports whether the graph follows the latest mail.
func (m *Model) live() bool {
	_, ok := m.index[m.at]
	return m.at == "" || !ok
}

// n is how many messages, oldest first, the graph shows.
func (m *Model) n() int {
	if m.live() {
		return len(m.msgs)
	}
	return m.index[m.at] + 1
}

// Show sets the replay cursor to the message with id at ("" for Live, as
// is an id not in the state) and whether retired members are hidden. A
// cursor that moved pulses its message; a return to Live pulses the mail
// it reveals.
func (m *Model) Show(at string, hideRetired bool) {
	now := m.clock()
	wasLive, was := m.live(), m.n()
	m.at, m.hideRetired = at, hideRetired
	switch n := m.n(); {
	case m.live():
		m.at = ""
		if !wasLive {
			for _, x := range m.msgs[max(was, len(m.msgs)-revealPulses):] {
				m.pulse(x, now)
			}
		}
	case n != was || wasLive:
		m.pulse(m.msgs[n-1], now)
	}
}

// SetPaused stops (or resumes) animation: the panel is minimized or
// another tab is showing.
func (m *Model) SetPaused(p bool) { m.paused = p }

// Select marks the node id selected ("" clears it).
func (m *Model) Select(id string) { m.selected = id }

// NeedsAnimation reports whether the picture changes with time alone: a
// shown member is working, or a pulse is under way on a drawn edge. It is
// false while paused.
func (m *Model) NeedsAnimation() bool {
	if m.paused || !m.ready {
		return false
	}
	c := m.cast()
	for _, h := range c.order {
		if a, ok := c.agents[h]; ok && breathes(a) {
			return true
		}
	}
	now := m.clock()
	for _, p := range m.pulses {
		if m.drawn(p, now) && len(m.pulseEdges(c, p.msg)) > 0 {
			return true
		}
	}
	return false
}

// drawn reports whether p is under way at now on mail the cursor shows.
func (m *Model) drawn(p pulse, now time.Time) bool {
	i, ok := m.index[p.msg.ID]
	return ok && i < m.n() && now.Sub(p.at) < pulseLife
}

// retiredState is the panel's Retired group: departed or departing.
func retiredState(a adapter.Agent) bool {
	return a.Lifecycle.State == adapter.LifeDeparted || a.Lifecycle.State == adapter.LifeDeparting
}

func breathes(a adapter.Agent) bool { return a.Activity.State == "working" }

func (m *Model) pulse(x adapter.Message, now time.Time) {
	m.pulses = append(m.pulses, pulse{msg: x, at: now})
	if len(m.pulses) > maxPulses {
		m.pulses = slices.Clone(m.pulses[len(m.pulses)-maxPulses:])
	}
}

func (m *Model) prune(now time.Time) {
	m.pulses = slices.DeleteFunc(m.pulses, func(p pulse) bool { return now.Sub(p.at) >= pulseLife })
}

// cast is who gets a node: up to MaxActive live members (the crew lead or
// orchestrator first, then the busiest), up to maxRetired departed ones
// unless hidden, and one "+N" node for every other handle the mail
// names. Nothing in it depends on the replay cursor, so stepping never
// moves a node.
type cast struct {
	order   []string // node ids in drawing order around the ellipse
	agents  map[string]adapter.Agent
	retired map[string]bool
	hidden  map[string]bool
	more    string // the "+N" node's id, "" when nothing folds
	folded  int
}

// node is the node id h is drawn as, or false when h is hidden.
func (c cast) node(h string) (string, bool) {
	if c.hidden[h] {
		return "", false
	}
	if _, ok := c.agents[h]; ok && slices.Contains(c.order, h) {
		return h, true
	}
	return c.more, c.more != ""
}

func (m *Model) cast() cast {
	c := cast{agents: map[string]adapter.Agent{}, retired: map[string]bool{}, hidden: map[string]bool{}}
	traffic := map[string]int{}
	for _, x := range m.msgs {
		traffic[x.From]++
		for _, r := range x.To {
			traffic[r]++
		}
	}
	lead := m.lead()
	var active, departed []adapter.Agent
	for _, a := range m.st.Agents {
		if retiredState(a) {
			departed = append(departed, a)
		} else {
			active = append(active, a)
		}
	}
	slices.SortFunc(active, func(a, b adapter.Agent) int {
		return cmp.Or(
			-cmp.Compare(boolInt(a.Handle == lead), boolInt(b.Handle == lead)),
			-cmp.Compare(traffic[a.Handle], traffic[b.Handle]),
			strings.Compare(a.Handle, b.Handle))
	})
	slices.SortFunc(departed, func(a, b adapter.Agent) int {
		return cmp.Or(-compareTimes(a.Lifecycle.DepartedAt, b.Lifecycle.DepartedAt), strings.Compare(a.Handle, b.Handle))
	})

	folded := map[string]bool{}
	for i, a := range active {
		if i < MaxActive {
			c.order = append(c.order, a.Handle)
			c.agents[a.Handle] = a
		} else {
			folded[a.Handle] = true
		}
	}
	for i, a := range departed {
		switch {
		case m.hideRetired:
			c.hidden[a.Handle] = true
		case i < maxRetired:
			c.order = append(c.order, a.Handle)
			c.agents[a.Handle] = a
			c.retired[a.Handle] = true
		default:
			folded[a.Handle] = true
		}
	}
	named := func(h string) {
		if _, ok := c.agents[h]; !ok && !c.hidden[h] && h != "" {
			folded[h] = true
		}
	}
	for _, x := range m.msgs {
		named(x.From)
		for _, r := range x.To {
			named(r)
		}
	}
	for _, ob := range m.obligations() {
		named(ob.OwedBy)
		named(ob.OwedTo)
	}
	if len(folded) > 0 {
		c.folded = len(folded)
		c.more = fmt.Sprintf("+%d", len(folded))
		c.order = append(c.order, c.more)
	}
	return c
}

// lead is the first member in the crew's lead role (a crew definition
// names its lead by role), or failing that the first orchestrator.
func (m *Model) lead() string {
	role := "orchestrator"
	if p := m.st.Panel; p != nil && p.Crew != nil && p.Crew.Lead != "" {
		role = p.Crew.Lead
	}
	for _, a := range m.st.Agents {
		if a.Role == role {
			return a.Handle
		}
	}
	return ""
}

func (m *Model) obligations() []adapter.Obligation {
	if p := m.st.Panel; p != nil && p.View != nil {
		return p.View.Obligations
	}
	return nil
}

type edgeKey struct{ from, to string }

type edgeAgg struct {
	count int
	style EdgeStyle
}

// edges aggregates the shown mail per drawn from->to pair. An edge takes
// the most actionable style among its messages (obligation > alert >
// queued > drained). An open obligation marks the edges its opener
// travelled once replay has shown the opener. When the opener is not in
// the mail (the adapter keeps a window of it), the obligation shows in
// Live only, on owed_to -> owed_by: the direction a request travels, as
// AMQ owes the reply to the opener's sender.
func (m *Model) edges(c cast) (map[edgeKey]*edgeAgg, []edgeKey) {
	n := m.n()
	aggs := map[edgeKey]*edgeAgg{}
	var keys []edgeKey
	add := func(k edgeKey, count int, s EdgeStyle) {
		a, ok := aggs[k]
		if !ok {
			a = &edgeAgg{style: s}
			aggs[k] = a
			keys = append(keys, k)
		}
		a.count += count
		a.style = max(a.style, s)
	}
	for _, x := range m.msgs[:n] {
		for _, k := range m.pulseEdges(c, x) {
			add(k, 1, messageStyle(x))
		}
	}
	for _, ob := range m.obligations() {
		if i, found := m.index[ob.Ref]; found {
			if i < n {
				for _, k := range m.pulseEdges(c, m.msgs[i]) {
					add(k, 0, StyleObligation)
				}
			}
			continue
		}
		a, ok1 := c.node(ob.OwedTo)
		b, ok2 := c.node(ob.OwedBy)
		if m.live() && ok1 && ok2 && a != b {
			add(edgeKey{a, b}, 0, StyleObligation)
		}
	}
	pos := map[string]int{}
	for i, id := range c.order {
		pos[id] = i
	}
	slices.SortFunc(keys, func(a, b edgeKey) int {
		return cmp.Or(cmp.Compare(pos[a.from], pos[b.from]), cmp.Compare(pos[a.to], pos[b.to]))
	})
	return aggs, keys
}

// pulseEdges are the drawn edges message x travels, one per recipient.
// A message with a hidden end is left out whole, as the panel's replay
// list leaves it out.
func (m *Model) pulseEdges(c cast, x adapter.Message) []edgeKey {
	if c.hidden[x.From] || slices.ContainsFunc(x.To, func(h string) bool { return c.hidden[h] }) {
		return nil
	}
	a, ok := c.node(x.From)
	if !ok {
		return nil
	}
	var out []edgeKey
	for _, r := range x.To {
		if b, ok := c.node(r); ok && a != b {
			out = append(out, edgeKey{a, b})
		}
	}
	return out
}

func messageStyle(x adapter.Message) EdgeStyle {
	switch {
	case x.Bounced || x.State == adapter.MsgDeadLettered:
		return StyleAlert
	case x.State == adapter.MsgDrained:
		return StyleDrained
	default:
		// queued, sent, and any state a newer adapter adds: not delivered
		// yet as far as this panel knows.
		return StyleQueued
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// compareTimes orders departure times, a nil one (still departing) after
// every recorded one.
func compareTimes(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return a.Compare(*b)
}
