package adapter

import (
	"slices"
	"strings"
)

// MaxEvents is how many recent events State keeps.
const MaxEvents = 64

// State is the panel's projection of one stream: what the latest snapshot
// said, with every later frame of the same stream folded in. It is a value:
// Apply returns a new State and never changes the one it is called on, so a
// UI can keep the previous one while it renders.
type State struct {
	// Stream is the id of the stream the state follows; "" until the first
	// snapshot.
	Stream string
	// Seq is the last frame folded in.
	Seq   uint64
	Scope Scope
	// Agents are sorted by handle, Messages by id (the adapter's order).
	Agents   []Agent
	Messages []Message
	// Panel is nil when the stream carried none.
	Panel *Panel
	// Events are the most recent events, oldest first, at most MaxEvents.
	Events []Event
}

// Ready reports whether a snapshot has been folded in.
func (s State) Ready() bool { return s.Stream != "" }

// Apply folds f into s. A snapshot replaces the whole state, since it starts
// a new stream; any other frame counts only when it belongs to the stream of
// the last snapshot. Gaps are the Stream's to detect: it restarts the
// adapter on one, and the restart's snapshot resynchronizes.
func (s State) Apply(f Frame) State {
	if snap, ok := f.(Snapshot); ok {
		return State{
			Stream: snap.Stream, Seq: snap.Seq, Scope: snap.Scope,
			Agents:   sortedBy(snap.Agents, agentKey),
			Messages: sortedBy(snap.Messages, messageKey),
			Panel:    snap.Panel,
		}
	}
	h := f.Head()
	if !s.Ready() || h.Stream != s.Stream {
		return s
	}
	s.Seq = h.Seq
	switch f := f.(type) {
	case AgentUpsert:
		s.Agents = upsert(s.Agents, f.Agent, agentKey)
	case MessageUpsert:
		s.Messages = upsert(s.Messages, f.Message, messageKey)
	case PanelUpsert:
		p := f.Panel
		s.Panel = &p
	case Remove:
		switch f.Entity {
		case EntityAgent:
			s.Agents = remove(s.Agents, f.Key, agentKey)
		case EntityMessage:
			s.Messages = remove(s.Messages, f.Key, messageKey)
		}
	case Event:
		n := min(len(s.Events), MaxEvents-1)
		s.Events = append(slices.Clone(s.Events[len(s.Events)-n:]), f)
	}
	return s
}

func agentKey(a Agent) string     { return a.Handle }
func messageKey(m Message) string { return m.ID }

func sortedBy[T any](xs []T, key func(T) string) []T {
	out := slices.Clone(xs)
	slices.SortStableFunc(out, func(a, b T) int { return strings.Compare(key(a), key(b)) })
	return out
}

// upsert returns a copy of xs, sorted by key, with x in place of the element
// of the same key, or inserted where its key sorts.
func upsert[T any](xs []T, x T, key func(T) string) []T {
	i, found := slices.BinarySearchFunc(xs, key(x), func(e T, k string) int { return strings.Compare(key(e), k) })
	out := slices.Clone(xs)
	if found {
		out[i] = x
		return out
	}
	return slices.Insert(out, i, x)
}

// remove returns a copy of xs without the element keyed k.
func remove[T any](xs []T, k string, key func(T) string) []T {
	return slices.DeleteFunc(slices.Clone(xs), func(e T) bool { return key(e) == k })
}
