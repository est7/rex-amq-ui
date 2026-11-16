package adapter

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fixture decodes testdata/panel/<name>, one frame per line.
func fixture(t *testing.T, name string) []Frame {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "panel", name))
	if err != nil {
		t.Fatal(err)
	}
	var out []Frame
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		f, err := DecodeFrame(sc.Bytes())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out = append(out, f)
	}
	return out
}

func fold(frames ...Frame) State {
	var s State
	for _, f := range frames {
		s = s.Apply(f)
	}
	return s
}

func agentOf(t *testing.T, s State, handle string) Agent {
	t.Helper()
	for _, a := range s.Agents {
		if a.Handle == handle {
			return a
		}
	}
	t.Fatalf("no agent %s in %v", handle, handles(s))
	return Agent{}
}

func handles(s State) []string {
	var out []string
	for _, a := range s.Agents {
		out = append(out, a.Handle)
	}
	return out
}

func messageIDs(s State) []string {
	var out []string
	for _, m := range s.Messages {
		out = append(out, m.ID)
	}
	return out
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// The stream the fresh-built adapter wrote for a session with a crew claim
// and a lifecycle but no narrator: a no_view view, then the hand-back
// recorded mid-stream arriving as a panel upsert.
func TestReducerFoldsRecordedCrewLifecycle(t *testing.T) {
	frames := fixture(t, "recorded-crew-lifecycle.ndjson")
	s := fold(frames[0])
	if !s.Ready() || s.Seq != 1 || s.Scope.SessionID != "session:01a1e4aa-0000-7000-8000-0000000000e1" {
		t.Fatalf("after snapshot: ready=%v seq=%d scope=%+v", s.Ready(), s.Seq, s.Scope)
	}
	p := s.Panel
	if p == nil || p.SessionLabel != "rex-amq-ui" || p.Crew == nil || p.Crew.Crew != "rex-crew" || p.Crew.ClaimedBy != "claude-98" {
		t.Fatalf("panel %+v", p)
	}
	if p.View == nil || !p.View.NoView() || p.View.HandBack != nil || p.View.Lifecycle == nil || p.View.Lifecycle.State != "open" {
		t.Fatalf("view before the hand-back: %+v", p.View)
	}
	if p.Personal() {
		t.Fatal("a session with no personal agent has no cockpit")
	}
	s = fold(frames...)
	if s.Seq != 3 || s.Panel.View.HandBack == nil || s.Panel.View.HandBack.By != "claude-98" || !strings.Contains(s.Panel.View.HandBack.Body, "**Blocked on me**: None") {
		t.Fatalf("after the upsert: seq=%d view=%+v", s.Seq, s.Panel.View)
	}
	if p.View.HandBack != nil {
		t.Fatal("folding the upsert changed the state it was applied to")
	}
}

// A crew session with attention, mail in every state, and frames this
// reader does not know.
func TestReducerFoldsCrewAttention(t *testing.T) {
	s := fold(fixture(t, "edited-crew-attention.ndjson")...)
	if s.Seq != 10 {
		t.Fatalf("seq %d, want 10: unknown frames still hold their place", s.Seq)
	}
	if got := handles(s); !slices.Equal(got, []string{"claude-01", "claude-02", "codex-03", "pi-04"}) {
		t.Fatalf("agents %v", got)
	}
	if a := agentOf(t, s, "claude-02"); a.Activity.State != "working" || a.Delivery.Unread != 1 || a.Model != "sonnet" {
		t.Fatalf("claude-02 after its upsert: %+v", a)
	}
	if a := agentOf(t, s, "codex-03"); a.Lifecycle.State != LifeDeparted || a.Lifecycle.DepartReason != "retired" || a.Pane != "gone" {
		t.Fatalf("codex-03: %+v", a)
	}
	if got := messageIDs(s); !slices.Equal(got, []string{"2026-11-16T09-00-00.000Z_m1", "2026-11-16T09-02-00.000Z_m2", "2026-11-16T09-04-00.000Z_m3"}) {
		t.Fatalf("messages %v: m4 was removed", got)
	}
	if m := s.Messages[0]; m.State != MsgDrained || !m.Announced {
		t.Fatalf("m1 after its upsert: %+v", m)
	}
	if m := s.Messages[2]; m.State != MsgQueued || !m.Bounced {
		t.Fatalf("m3: %+v", m)
	}
	if len(s.Events) != 2 || s.Events[0].Kind != EventDrained || s.Events[0].MsgID != "2026-11-16T09-00-00.000Z_m1" ||
		s.Events[1].Kind != EventDoorbell || s.Events[1].Progress != "accepted" {
		t.Fatalf("events %+v", s.Events)
	}
	p := s.Panel
	if p.Crew.Lead != "orchestrator" || !slices.Equal(p.Crew.Members, []string{"orchestrator", "implementer", "reviewer"}) {
		t.Fatalf("crew %+v", p.Crew)
	}
	want := map[string][]Mark{
		"claude-01": {{MarkWaitingOnTheHuman, mustTime(t, "2026-11-16T09:06:01Z")}},
		"claude-02": {{MarkTurnError, mustTime(t, "2026-11-16T09:00:00Z")}},
		"pi-04":     {{MarkStalled, mustTime(t, "2026-11-16T09:01:00Z")}},
	}
	if fmt.Sprint(p.Attention) != fmt.Sprint(want) {
		t.Fatalf("attention %v, want %v", p.Attention, want)
	}
	v := p.View
	if v.NoView() || v.Narrative == nil || v.Narrator != "pi-04" || len(v.Obligations) != 1 || v.Obligations[0].OwedBy != "claude-02" ||
		v.HandBack.Seq != 1 || v.Lifecycle.State != "opened" || len(v.Members) != 3 || v.Members[1].Attention[0].Kind != MarkTurnError {
		t.Fatalf("view %+v", v)
	}
}

func TestReducerPersonalCockpit(t *testing.T) {
	s := fold(fixture(t, "edited-personal-cockpit.ndjson")...)
	p := s.Panel
	if !p.Personal() || len(p.Cockpit) != 2 || p.View != nil || p.Crew != nil {
		t.Fatalf("panel %+v", p)
	}
	e := p.Cockpit[0]
	if e.Name != "auth-refresh" || !e.HasView || e.ActiveMembers != 2 || e.OpenAttention != 2 || e.OpenObligations != 1 || e.Narrative == nil || e.HandBack == nil {
		t.Fatalf("epic 0 %+v", e)
	}
	if e := p.Cockpit[1]; e.Name != "billing" || e.HasView || e.Lifecycle.State != "creating" || e.Narrative != nil {
		t.Fatalf("epic 1 %+v", e)
	}
	empty, err := DecodeFrame([]byte(`{"schema":1,"stream":"x","seq":1,"type":"snapshot","at":"2026-11-16T09:00:00Z","scope":{"kind":"session","server":"s","session_id":"session:p"},"agents":[],"messages":[],"panel":{"session_label":"me","attention":{},"cockpit":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p := fold(empty).Panel; !p.Personal() || len(p.Cockpit) != 0 {
		t.Fatalf("a personal session with no Epic linked: personal=%v cockpit=%v", p.Personal(), p.Cockpit)
	}
	if p := fold(fixture(t, "recorded-bare.ndjson")...).Panel; p.Personal() || p.Crew != nil || p.View != nil || p.SessionLabel != "rex-amq-ui" {
		t.Fatalf("bare session %+v", p)
	}
}

func TestReducerNoViewHandBackOnly(t *testing.T) {
	p := fold(fixture(t, "edited-no-view.ndjson")...).Panel
	if p.View == nil || !p.View.NoView() || p.View.HandBack == nil || p.View.HandBack.Seq != 2 || p.View.Lifecycle != nil || p.View.Narrative != nil {
		t.Fatalf("view %+v", p.View)
	}
}

// A new stream (the adapter restarted) replaces everything: nothing the old
// one said survives, not even its events.
func TestReducerSnapshotReplacesEverything(t *testing.T) {
	s := fold(fixture(t, "edited-crew-attention.ndjson")...)
	s = s.Apply(fixture(t, "edited-personal-cockpit.ndjson")[0])
	if s.Stream != "0f1e2d3c4b5a6978" || s.Seq != 1 || !slices.Equal(handles(s), []string{"pi-05"}) || len(s.Messages) != 0 || len(s.Events) != 0 ||
		s.Panel.SessionLabel != "me" || s.Scope.SessionID != "session:p" {
		t.Fatalf("state after a new snapshot: %+v", s)
	}
}

// Frames before the first snapshot, or of a stream other than the
// snapshot's, change nothing.
func TestReducerIgnoresFramesOutsideItsStream(t *testing.T) {
	frames := fixture(t, "edited-crew-attention.ndjson")
	if s := fold(frames[1:]...); s.Ready() || s.Seq != 0 || len(s.Agents) != 0 || len(s.Events) != 0 {
		t.Fatalf("frames before any snapshot were folded: %+v", s)
	}
	s := fold(fixture(t, "edited-personal-cockpit.ndjson")[0])
	before := fmt.Sprintf("%+v", s)
	for _, f := range frames[1:] {
		s = s.Apply(f)
	}
	if after := fmt.Sprintf("%+v", s); after != before {
		t.Fatalf("frames of another stream changed the state:\n%s\n%s", before, after)
	}
}

func TestReducerUpsertInsertsInKeyOrderAndLeavesReceiverAlone(t *testing.T) {
	s0 := fold(fixture(t, "edited-crew-attention.ndjson")[0])
	f, err := DecodeFrame([]byte(`{"schema":1,"stream":"a1b2c3d4e5f60718","seq":2,"type":"upsert","at":"2026-11-16T09:07:00Z","entity":"agent","key":"claude-015","value":{"handle":"claude-015","role":"implementer","activity":{"state":"idle","source":"none"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	s1 := s0.Apply(f)
	if got := handles(s1); !slices.Equal(got, []string{"claude-01", "claude-015", "claude-02", "codex-03", "pi-04"}) {
		t.Fatalf("agents %v", got)
	}
	g, err := DecodeFrame([]byte(`{"schema":1,"stream":"a1b2c3d4e5f60718","seq":3,"type":"upsert","at":"2026-11-16T09:07:00Z","entity":"agent","key":"claude-01","value":{"handle":"claude-01","role":"orchestrator","activity":{"state":"blocked","source":"rex_status"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	s2 := s1.Apply(g)
	if agentOf(t, s2, "claude-01").Activity.State != "blocked" {
		t.Fatal("upsert did not replace the agent")
	}
	if agentOf(t, s1, "claude-01").Activity.State != "working" || len(s0.Agents) != 4 {
		t.Fatal("Apply changed the state it was called on")
	}
}

func TestReducerKeepsTheLatestEvents(t *testing.T) {
	s := fold(fixture(t, "edited-crew-attention.ndjson")[0])
	for i := range MaxEvents + 5 {
		f, err := DecodeFrame(fmt.Appendf(nil, `{"schema":1,"stream":"a1b2c3d4e5f60718","seq":%d,"type":"event","at":"2026-11-16T09:07:00Z","kind":"drained","msg_id":"m%d","occurred_at":"2026-11-16T09:07:00Z"}`, i+2, i))
		if err != nil {
			t.Fatal(err)
		}
		s = s.Apply(f)
	}
	if len(s.Events) != MaxEvents || s.Events[0].MsgID != "m5" || s.Events[MaxEvents-1].MsgID != fmt.Sprintf("m%d", MaxEvents+4) {
		t.Fatalf("%d events, first %s", len(s.Events), s.Events[0].MsgID)
	}
}

func TestDecodeFrameRejectsWhatItCannotRead(t *testing.T) {
	for _, c := range []struct{ name, line string }{
		{"not json", `heartbeat`},
		{"declared field of the wrong type", `{"schema":1,"stream":"x","seq":"1","type":"heartbeat","at":"2026-11-16T09:00:00Z"}`},
		{"bad attention mark", `{"schema":1,"stream":"x","seq":2,"type":"upsert","at":"2026-11-16T09:00:00Z","entity":"panel","key":"s","value":{"session_label":"x","attention":{"a":["stalled"]}}}`},
		{"agent value of the wrong shape", `{"schema":1,"stream":"x","seq":2,"type":"upsert","at":"2026-11-16T09:00:00Z","entity":"agent","key":"a","value":[1]}`},
	} {
		if _, err := DecodeFrame([]byte(c.line)); err == nil {
			t.Errorf("%s: decoded", c.name)
		}
	}
	if _, err := DecodeFrame([]byte(`{"schema":2,"stream":"x","seq":1,"type":"heartbeat","at":"2026-11-16T09:00:00Z"}`)); !errors.Is(err, ErrSchema) {
		t.Errorf("schema 2: %v", err)
	}
	f, err := DecodeFrame([]byte(`{"schema":1,"stream":"x","seq":4,"type":"upsert","at":"2026-11-16T09:00:00Z","entity":"widget","key":"w","value":{"a":1},"novel":true}`))
	if u, ok := f.(Unknown); err != nil || !ok || u.Seq != 4 || u.Type != "upsert" {
		t.Errorf("unknown entity: %#v %v", f, err)
	}
}
