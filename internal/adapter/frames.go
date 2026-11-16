// Package adapter reads `rex-amq-adapter panel --json`: it finds the adapter
// binary, runs the stream and restarts it when it ends (Stream), decodes its
// NDJSON frames (DecodeFrame), and folds them into the panel's projection
// (State). The adapter owns every state file; this package reads only its
// stdout and never sends, reads, or marks mail.
//
// The wire types mirror the adapter's (internal/app/events.go, panel.go,
// view.go, cockpit.go and internal/core/project.go in rex-amq-adapter, frame
// schema 1). Decoding is a tolerant reader: unknown fields are ignored, and a
// frame of an unknown type or entity decodes as Unknown, so a newer adapter
// that adds either still streams.
package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Schema is the frame format this package reads.
const Schema = 1

// ErrSchema is a frame of a schema this package does not read.
var ErrSchema = errors.New("adapter: unsupported frame schema")

// Header is what every frame carries: the stream it belongs to (random per
// adapter run) and its place in it (from 1, no gaps).
type Header struct {
	Schema int       `json:"schema"`
	Stream string    `json:"stream"`
	Seq    uint64    `json:"seq"`
	Type   string    `json:"type"`
	At     time.Time `json:"at"`
}

// Frame is one decoded line: Snapshot, AgentUpsert, MessageUpsert,
// PanelUpsert, Remove, Event, Heartbeat, or Unknown.
type Frame interface {
	Head() Header
}

// Snapshot starts a stream: the whole scope as it is now.
type Snapshot struct {
	Header
	Scope    Scope     `json:"scope"`
	Agents   []Agent   `json:"agents"`
	Messages []Message `json:"messages"`
	// Panel is nil only when the adapter sent none (`events --json`).
	Panel *Panel `json:"panel"`
}

// AgentUpsert carries an agent, whole, that is new or changed.
type AgentUpsert struct {
	Header
	Agent   Agent
	Changed []string
}

// MessageUpsert carries a message, whole, that is new or changed.
type MessageUpsert struct {
	Header
	Message Message
	Changed []string
}

// PanelUpsert carries the session's panel, whole, after it changed.
type PanelUpsert struct {
	Header
	SessionID string
	Panel     Panel
	Changed   []string
}

// Remove drops the agent or message keyed Key.
type Remove struct {
	Header
	Entity string `json:"entity"`
	Key    string `json:"key"`
}

// Event is something that happened: a doorbell's outcome, a message drained,
// or a message dead-lettered.
type Event struct {
	Header
	Kind       string    `json:"kind"`
	Handle     string    `json:"handle,omitempty"`
	MsgID      string    `json:"msg_id,omitempty"`
	Sender     string    `json:"sender,omitempty"`
	Thread     string    `json:"thread,omitempty"`
	Progress   string    `json:"progress,omitempty"`
	Code       string    `json:"code,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

// Event kinds the adapter sends today.
const (
	EventDoorbell     = "doorbell"
	EventDrained      = "drained"
	EventDeadLettered = "dead_lettered"
)

// Heartbeat says the stream is alive (every 5 s by default).
type Heartbeat struct{ Header }

// Unknown is a frame of a type, or an upsert of an entity, this package does
// not know. It still holds its place in the stream.
type Unknown struct {
	Header
	Raw json.RawMessage
}

func (h Header) Head() Header { return h }

// Entities a frame names.
const (
	EntityAgent   = "agent"
	EntityMessage = "message"
	EntityPanel   = "panel"
)

// DecodeFrame parses one NDJSON line. A line that is not a JSON object, a
// frame of another schema, or a known frame whose declared fields do not
// parse is an error; unknown types, entities, and fields are not.
func DecodeFrame(line []byte) (Frame, error) {
	var h Header
	if err := json.Unmarshal(line, &h); err != nil {
		return nil, fmt.Errorf("adapter: decode frame header: %w", err)
	}
	if h.Schema != Schema {
		return nil, fmt.Errorf("%w %d (want %d)", ErrSchema, h.Schema, Schema)
	}
	switch h.Type {
	case "snapshot":
		var f Snapshot
		return decodeInto(line, &f, h)
	case "upsert":
		return decodeUpsert(line, h)
	case "remove":
		var f Remove
		return decodeInto(line, &f, h)
	case "event":
		var f Event
		return decodeInto(line, &f, h)
	case "heartbeat":
		return Heartbeat{Header: h}, nil
	}
	return Unknown{Header: h, Raw: append(json.RawMessage(nil), line...)}, nil
}

func decodeInto[T Frame](line []byte, f *T, h Header) (Frame, error) {
	if err := json.Unmarshal(line, f); err != nil {
		return nil, fmt.Errorf("adapter: decode %s frame %d: %w", h.Type, h.Seq, err)
	}
	return *f, nil
}

func decodeUpsert(line []byte, h Header) (Frame, error) {
	var u struct {
		Entity  string          `json:"entity"`
		Key     string          `json:"key"`
		Value   json.RawMessage `json:"value"`
		Changed []string        `json:"changed"`
	}
	if err := json.Unmarshal(line, &u); err != nil {
		return nil, fmt.Errorf("adapter: decode upsert frame %d: %w", h.Seq, err)
	}
	bad := func(err error) error {
		return fmt.Errorf("adapter: decode %s upsert %q (frame %d): %w", u.Entity, u.Key, h.Seq, err)
	}
	switch u.Entity {
	case EntityAgent:
		f := AgentUpsert{Header: h, Changed: u.Changed}
		if err := json.Unmarshal(u.Value, &f.Agent); err != nil {
			return nil, bad(err)
		}
		return f, nil
	case EntityMessage:
		f := MessageUpsert{Header: h, Changed: u.Changed}
		if err := json.Unmarshal(u.Value, &f.Message); err != nil {
			return nil, bad(err)
		}
		return f, nil
	case EntityPanel:
		f := PanelUpsert{Header: h, SessionID: u.Key, Changed: u.Changed}
		if err := json.Unmarshal(u.Value, &f.Panel); err != nil {
			return nil, bad(err)
		}
		return f, nil
	}
	return Unknown{Header: h, Raw: append(json.RawMessage(nil), line...)}, nil
}

// Scope is what the stream watches: one Rex session for a panel.
type Scope struct {
	Kind      string `json:"kind"`
	Server    string `json:"server"`
	SessionID string `json:"session_id,omitempty"`
	WindowID  string `json:"window_id,omitempty"`
}

// Agent is an agent of the scope, keyed by Handle.
type Agent struct {
	Handle    string    `json:"handle"`
	Kind      string    `json:"kind"`
	Server    string    `json:"server"`
	SessionID string    `json:"session_id"`
	WindowID  string    `json:"window_id"`
	Tab       string    `json:"tab"`
	BlockID   string    `json:"block_id"`
	Pane      string    `json:"pane"` // present | gone
	Task      string    `json:"task"`
	Role      string    `json:"role"`
	Model     string    `json:"model,omitempty"`
	Effort    string    `json:"effort,omitempty"`
	Lifecycle Lifecycle `json:"lifecycle"`
	Activity  Activity  `json:"activity"`
	Delivery  Delivery  `json:"delivery"`
}

// Agent lifecycle states.
const (
	LifeAdopting  = "adopting"
	LifeLive      = "live"
	LifeDeparting = "departing"
	LifeDeparted  = "departed"
)

type Lifecycle struct {
	State        string     `json:"state"`
	DepartReason string     `json:"depart_reason,omitempty"`
	AdoptedAt    time.Time  `json:"adopted_at"`
	DepartedAt   *time.Time `json:"departed_at,omitempty"`
}

// Activity is what the agent reports doing: absent, idle, working, blocked,
// done, or error.
type Activity struct {
	State      string     `json:"state"`
	Kind       string     `json:"kind,omitempty"`
	Source     string     `json:"source"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

// Delivery is the state of the agent's mail.
type Delivery struct {
	Unread          int            `json:"unread"`
	Announced       int            `json:"announced"`
	Doorbell        *DoorbellState `json:"doorbell"`
	PendingDoorbell bool           `json:"pending_doorbell"`
	InProcess       bool           `json:"in_process"`
}

// DoorbellState is the injector's latest outcome and since when it repeats.
type DoorbellState struct {
	Progress string    `json:"progress"`
	Code     string    `json:"code,omitempty"`
	Since    time.Time `json:"since"`
}

// Message states. A bounced message is one a departed agent holds unread
// (Message.Bounced), not a state of its own.
const (
	MsgSent         = "sent"
	MsgQueued       = "queued"
	MsgDrained      = "drained"
	MsgDeadLettered = "dead_lettered"
)

// Message is a message's envelope, keyed by ID; the adapter never sends a
// body here.
type Message struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        []string  `json:"to"`
	Thread    string    `json:"thread"`
	Subject   string    `json:"subject"`
	State     string    `json:"state"`
	Announced bool      `json:"announced"`
	Bounced   bool      `json:"bounced"`
	CreatedAt time.Time `json:"created_at"`
}

// Panel is the session's panel (the adapter's PanelProjection).
type Panel struct {
	SessionLabel string `json:"session_label"`
	// Crew is nil when the session claimed none.
	Crew *Crew `json:"crew,omitempty"`
	// Attention maps each live agent's handle to what waits on someone in
	// its pane; an empty list when nothing does.
	Attention map[string][]Mark `json:"attention"`
	// View is nil when the session has no view, lifecycle, or hand-back.
	View *SessionView `json:"view,omitempty"`
	// Cockpit is nil when the session holds no live personal agent, and
	// empty (not nil) when one does but no Epic is linked to it. Personal
	// tells the two apart.
	Cockpit []Epic `json:"cockpit,omitempty"`
}

// Personal reports whether the session holds a live personal agent, so the
// panel has an Epics view (possibly empty).
func (p Panel) Personal() bool { return p.Cockpit != nil }

// Crew is the session's crew claim; Lead and Members are empty when the
// crew's definition did not resolve.
type Crew struct {
	Crew      string    `json:"crew"`
	ClaimedBy string    `json:"claimed_by,omitempty"`
	ClaimedAt time.Time `json:"claimed_at"`
	Lead      string    `json:"lead,omitempty"`
	Members   []string  `json:"members,omitempty"`
}

// Attention mark kinds.
const (
	MarkTurnError         = "turn_error"
	MarkWaitingOnTheHuman = "waiting_on_the_human"
	MarkStalled           = "stalled"
)

// Mark is one attention mark, `<kind>@<RFC 3339 time>` on the wire.
type Mark struct {
	Kind string
	At   time.Time
}

func (m Mark) String() string { return m.Kind + "@" + m.At.UTC().Format(time.RFC3339) }

func (m Mark) MarshalJSON() ([]byte, error) { return json.Marshal(m.String()) }

func (m *Mark) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	kind, at, ok := strings.Cut(s, "@")
	if !ok || kind == "" {
		return fmt.Errorf("attention mark %q: want <kind>@<time>", s)
	}
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return fmt.Errorf("attention mark %q: %w", s, err)
	}
	m.Kind, m.At = kind, t
	return nil
}

// SessionView is the session's stored view, as the narrator wrote it with
// the recorded hand-back and lifecycle, or before it wrote one, the
// lifecycle or hand-back alone with a no_view gap.
type SessionView struct {
	Schema       int            `json:"schema"`
	AsOf         ViewCursor     `json:"as_of"`
	Session      ViewSession    `json:"session"`
	Narrator     string         `json:"narrator"`
	Narrative    *ViewNarrative `json:"narrative,omitempty"`
	HandBack     *HandBack      `json:"hand_back,omitempty"`
	Lifecycle    *EpicLifecycle `json:"lifecycle,omitempty"`
	Members      []ViewMember   `json:"members"`
	Obligations  []Obligation   `json:"obligations"`
	Activity     []ViewMessage  `json:"activity"`
	ControlPlane []ViewMessage  `json:"control_plane"`
	CoverageGaps []CoverageGap  `json:"coverage_gaps"`
	Limits       ViewLimits     `json:"limits"`
	Sources      []string       `json:"sources"`
}

// GapNoView is the coverage gap of a view the narrator has not written yet.
const GapNoView = "no_view"

// NoView reports whether the narrator has not written the view yet, so only
// the recorded lifecycle or hand-back is in it.
func (v SessionView) NoView() bool {
	for _, g := range v.CoverageGaps {
		if g.Kind == GapNoView {
			return true
		}
	}
	return false
}

type ViewCursor struct {
	At          time.Time `json:"at"`
	LastMessage string    `json:"last_message,omitempty"`
	Pulse       string    `json:"pulse"`
}

type ViewSession struct {
	Server    string `json:"server"`
	SessionID string `json:"session_id"`
	Epic      string `json:"epic,omitempty"`
}

type ViewNarrative struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

type ViewMember struct {
	Handle       string     `json:"handle"`
	Kind         string     `json:"kind"`
	Role         string     `json:"role,omitempty"`
	Task         string     `json:"task,omitempty"`
	Live         bool       `json:"live"`
	DepartedAt   *time.Time `json:"departed_at,omitempty"`
	DepartReason string     `json:"depart_reason,omitempty"`
	Attention    []Mark     `json:"attention,omitempty"`
}

// Obligation is an open request, question, or todo, owed by one agent to
// another.
type Obligation struct {
	Ref        string `json:"ref"`
	Thread     string `json:"thread"`
	Kind       string `json:"kind"`
	Subject    string `json:"subject"`
	OwedBy     string `json:"owed_by"`
	OwedTo     string `json:"owed_to"`
	State      string `json:"state"`
	AgeSeconds int64  `json:"age_seconds"`
	Provenance string `json:"provenance"`
}

// ViewMessage is a message the view lists, with its body excerpt when the
// narrator was allowed to read it.
type ViewMessage struct {
	Ref       string    `json:"ref"`
	From      string    `json:"from"`
	To        []string  `json:"to"`
	Thread    string    `json:"thread"`
	Subject   string    `json:"subject"`
	Created   time.Time `json:"created"`
	Kind      string    `json:"kind,omitempty"`
	Priority  string    `json:"priority,omitempty"`
	Body      string    `json:"body,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
}

type CoverageGap struct {
	Kind string   `json:"kind"`
	Refs []string `json:"refs,omitempty"`
	Note string   `json:"note,omitempty"`
}

type ViewLimits struct {
	RecentMessages          int   `json:"recent_messages"`
	MessagesOmitted         int   `json:"messages_omitted"`
	ExcerptChars            int   `json:"excerpt_chars"`
	ObligationsOmitted      int   `json:"obligations_omitted"`
	ObligationMaxAgeSeconds int64 `json:"obligation_max_age_seconds,omitempty"`
}

// HandBack is an orchestrator's final report, whole.
type HandBack struct {
	By     string    `json:"by"`
	At     time.Time `json:"at"`
	Body   string    `json:"body"`
	Seq    int       `json:"seq,omitempty"`
	Digest string    `json:"digest,omitempty"`
}

// EpicLifecycle is an Epic's recorded lifecycle (creating, opened, closing,
// closed).
type EpicLifecycle struct {
	SessionID         string        `json:"session_id"`
	Server            string        `json:"server,omitempty"`
	Name              string        `json:"name,omitempty"`
	Dir               string        `json:"dir,omitempty"`
	PersonalHandle    string        `json:"personal_handle,omitempty"`
	PersonalSessionID string        `json:"personal_session_id,omitempty"`
	Crew              string        `json:"crew,omitempty"`
	State             string        `json:"state"`
	OpenedAt          time.Time     `json:"opened_at,omitzero"`
	ClosedAt          *time.Time    `json:"closed_at,omitempty"`
	UpdatedAt         time.Time     `json:"updated_at"`
	HandBacks         []HandBackRef `json:"hand_backs,omitempty"`
}

type HandBackRef struct {
	Seq    int       `json:"seq"`
	Digest string    `json:"digest"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
}

// Epic is an Epic linked to the session's personal agent (the adapter's
// PanelEpic): its summary, its narrator's narrative, and what waits in it.
type Epic struct {
	SessionID       string         `json:"session_id"`
	Server          string         `json:"server,omitempty"`
	Name            string         `json:"name,omitempty"`
	Dir             string         `json:"dir,omitempty"`
	Lifecycle       *EpicLifecycle `json:"lifecycle,omitempty"`
	HandBack        *HandBack      `json:"hand_back,omitempty"`
	HasView         bool           `json:"has_view"`
	Narrative       *ViewNarrative `json:"narrative,omitempty"`
	ActiveMembers   int            `json:"active_members"`
	OpenAttention   int            `json:"open_attention"`
	OpenObligations int            `json:"open_obligations"`
}
