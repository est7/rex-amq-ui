package ui

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/est7/rex-amq-ui/internal/adapter"
)

// What each tab lists, derived from the folded stream. The views render
// these; keys move over them.

// alertKind orders alerts: what waits on the human first.
type alertKind int

const (
	alertWaiting alertKind = iota
	alertTurnError
	alertStalled
	alertDeadLetter
	alertBounced
	alertObligation
)

// alert is one System Alerts row. Handle is the agent whose pane Enter
// goes to.
type alert struct {
	kind   alertKind
	handle string
	text   string
	since  time.Time
}

// alerts are the session's deterministic alerts: attention marks, mail
// that bounced or was dead-lettered, and open obligations.
func (m Model) alerts() []alert {
	var out []alert
	p := m.st.Panel
	if p != nil {
		for _, h := range sortedKeys(p.Attention) {
			for _, mk := range p.Attention[h] {
				a := alert{handle: h, since: mk.At}
				switch mk.Kind {
				case adapter.MarkWaitingOnTheHuman:
					a.kind, a.text = alertWaiting, m.who(h)+" is waiting on you"
				case adapter.MarkTurnError:
					a.kind, a.text = alertTurnError, m.who(h)+" hit a turn error"
				case adapter.MarkStalled:
					a.kind, a.text = alertStalled, m.who(h)+" stalled"
				default:
					a.kind, a.text = alertStalled, m.who(h)+" "+mk.Kind
				}
				out = append(out, a)
			}
		}
	}
	for _, msg := range m.st.Messages {
		switch {
		case msg.State == adapter.MsgDeadLettered:
			out = append(out, alert{kind: alertDeadLetter, handle: msg.From, since: msg.CreatedAt,
				text: fmt.Sprintf("dead letter %s → %s: %s", msg.From, recipients(msg), msg.Subject)})
		case msg.Bounced:
			out = append(out, alert{kind: alertBounced, handle: msg.From, since: msg.CreatedAt,
				text: fmt.Sprintf("bounced %s → %s: %s", msg.From, recipients(msg), msg.Subject)})
		}
	}
	if p != nil && p.View != nil {
		for _, o := range p.View.Obligations {
			if o.State != "open" {
				continue
			}
			out = append(out, alert{kind: alertObligation, handle: o.OwedBy,
				since: m.owedSince(o),
				text:  fmt.Sprintf("%s owes %s a %s: %s", o.OwedBy, o.OwedTo, o.Kind, o.Subject)})
		}
	}
	slices.SortStableFunc(out, func(a, b alert) int {
		return cmp.Or(cmp.Compare(a.kind, b.kind), a.since.Compare(b.since))
	})
	return out
}

func (m Model) now() time.Time { return m.deps.Clock.Now() }

func sortedKeys[V any](mp map[string]V) []string {
	ks := make([]string, 0, len(mp))
	for k := range mp {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

func recipients(msg adapter.Message) string {
	switch len(msg.To) {
	case 0:
		return "?"
	case 1:
		return msg.To[0]
	}
	return fmt.Sprintf("%s +%d", msg.To[0], len(msg.To)-1)
}

// who names an agent as role(handle) when its role is known.
func (m Model) who(handle string) string {
	if a, ok := m.agent(handle); ok && a.Role != "" {
		return a.Role + "(" + handle + ")"
	}
	return handle
}

func (m Model) agent(handle string) (adapter.Agent, bool) {
	for _, a := range m.st.Agents {
		if a.Handle == handle {
			return a, true
		}
	}
	return adapter.Agent{}, false
}

// Member groups, top to bottom.
type group int

const (
	groupLead group = iota
	groupActive
	groupStandingBy
	groupRetired
)

var groupNames = map[group]string{groupLead: "Lead", groupActive: "Active", groupStandingBy: "Standing by", groupRetired: "Retired"}

func (m Model) groupOf(a adapter.Agent) group {
	switch {
	case a.Lifecycle.State == adapter.LifeDeparted || a.Lifecycle.State == adapter.LifeDeparting:
		return groupRetired
	case a.Role == "orchestrator" || (m.st.Panel != nil && m.st.Panel.Crew != nil && m.st.Panel.Crew.Lead != "" && a.Role == m.st.Panel.Crew.Lead):
		return groupLead
	case a.Activity.State == "working" || a.Activity.State == "blocked":
		return groupActive
	}
	return groupStandingBy
}

// members are the agents in display order: by group, then handle. Retired
// agents are left out while their group is closed.
func (m Model) members() []adapter.Agent {
	out := slices.Clone(m.st.Agents)
	slices.SortStableFunc(out, func(a, b adapter.Agent) int {
		return cmp.Or(cmp.Compare(m.groupOf(a), m.groupOf(b)), cmp.Compare(a.Handle, b.Handle))
	})
	if !m.retired {
		out = slices.DeleteFunc(out, func(a adapter.Agent) bool { return m.groupOf(a) == groupRetired })
	}
	return out
}

func (m Model) retiredCount() int {
	n := 0
	for _, a := range m.st.Agents {
		if m.groupOf(a) == groupRetired {
			n++
		}
	}
	return n
}

func (m Model) liveCount() int { return len(m.st.Agents) - m.retiredCount() }

func (m Model) memberHandle(i int) string {
	ms := m.members()
	if i < 0 || i >= len(ms) {
		return ""
	}
	return ms[i].Handle
}

// mailRows are the messages the Mail tab lists: oldest first, filtered by
// the search and the member filter.
func (m Model) mailRows() []adapter.Message {
	out := make([]adapter.Message, 0, len(m.st.Messages))
	for _, msg := range m.st.Messages {
		if !matches(msg.Subject, m.mail.query) {
			continue
		}
		if f := m.mail.from; f != "" && msg.From != f && !slices.Contains(msg.To, f) {
			continue
		}
		out = append(out, msg)
	}
	return chronological(out)
}

func chronological(ms []adapter.Message) []adapter.Message {
	slices.SortStableFunc(ms, func(a, b adapter.Message) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	return ms
}

// excerpt is the body the narrator was allowed to read for message id:
// only messages the view lists under control_plane or activity carry one.
func (m Model) excerpt(id string) (adapter.ViewMessage, bool) {
	if m.st.Panel == nil || m.st.Panel.View == nil {
		return adapter.ViewMessage{}, false
	}
	v := m.st.Panel.View
	for _, list := range [][]adapter.ViewMessage{v.ControlPlane, v.Activity} {
		for _, vm := range list {
			if vm.Ref == id && vm.Body != "" {
				return vm, true
			}
		}
	}
	return adapter.ViewMessage{}, false
}

func (m *Model) toggleExcerpt() {
	rows := m.mailRows()
	i := m.sel[TabMail]
	if i < 0 || i >= len(rows) {
		return
	}
	id := rows[i].ID
	if _, ok := m.excerpt(id); !ok {
		m.hint = "no excerpt: the narrator was not allowed to read this message"
		return
	}
	if m.mail.expanded[id] {
		delete(m.mail.expanded, id)
	} else {
		m.mail.expanded[id] = true
	}
	m.scroll[TabMail] = 0
}

// graphMessages are the messages the Graph replays, oldest first; with
// Hide retired, those to or from a retired agent are left out.
func (m Model) graphMessages() []adapter.Message {
	out := slices.Clone(m.st.Messages)
	if m.hideRetired {
		retired := map[string]bool{}
		for _, a := range m.st.Agents {
			if m.groupOf(a) == groupRetired {
				retired[a.Handle] = true
			}
		}
		out = slices.DeleteFunc(out, func(msg adapter.Message) bool {
			return retired[msg.From] || slices.ContainsFunc(msg.To, func(h string) bool { return retired[h] })
		})
	}
	return chronological(out)
}

func (m Model) epics() []adapter.Epic {
	if !m.personal() {
		return nil
	}
	return m.st.Panel.Cockpit
}

func (m Model) selectedEpic() (adapter.Epic, bool) {
	es := m.epics()
	i := m.sel[TabEpics]
	if i < 0 || i >= len(es) {
		return adapter.Epic{}, false
	}
	return es[i], true
}

// selectedHandle is the agent Enter goes to on Members or Overview.
func (m Model) selectedHandle() string {
	if m.tab == TabMembers {
		return m.memberHandle(m.sel[TabMembers])
	}
	as := m.alerts()
	if i := m.sel[TabOverview]; i >= 0 && i < len(as) {
		return as[i].handle
	}
	return ""
}

func (m Model) rowCount(t Tab) int {
	switch t {
	case TabEpics:
		return len(m.epics())
	case TabOverview:
		return len(m.alerts()) + m.overviewSections()
	case TabMembers:
		return len(m.members())
	case TabMail:
		return len(m.mailRows())
	case TabGraph:
		return len(m.graphMessages())
	}
	return 0
}

// replayIndex is where the replay cursor is in graphMessages; false when it
// follows Live or its message is no longer listed.
func (m Model) replayIndex() (int, bool) {
	if m.graphAt == "" {
		return 0, false
	}
	for i, msg := range m.graphMessages() {
		if msg.ID == m.graphAt {
			return i, true
		}
	}
	return 0, false
}

// owedSince is when an obligation opened. The adapter sends its age as of
// the view and no upsert when only the age changes, so the age is measured
// from a fixed time: the request's envelope, else the view's as_of less the
// age, else the time the panel first saw it (kept in Model.owed).
func (m Model) owedSince(o adapter.Obligation) time.Time {
	for _, msg := range m.st.Messages {
		if msg.ID == o.Ref {
			return msg.CreatedAt
		}
	}
	if v := m.view(); v != nil && !v.AsOf.At.IsZero() {
		return v.AsOf.At.Add(-time.Duration(o.AgeSeconds) * time.Second)
	}
	if t, ok := m.owed[o.Ref]; ok {
		return t
	}
	return m.now().Add(-time.Duration(o.AgeSeconds) * time.Second)
}

// trackOwed records when each open obligation without another anchor was
// first seen, and forgets those that closed.
func (m *Model) trackOwed() {
	v := m.view()
	if v == nil {
		m.owed = nil
		return
	}
	owed := make(map[string]time.Time, len(v.Obligations))
	for _, o := range v.Obligations {
		if o.State == "open" {
			owed[o.Ref] = m.owedSince(o)
		}
	}
	m.owed = owed
}
