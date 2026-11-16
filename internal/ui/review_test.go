package ui

import (
	"strings"
	"testing"

	"github.com/est7/rex-amq-ui/internal/adapter"
	"github.com/est7/rex-amq-ui/internal/layer"
)

// next is the frame after s in m's stream.
func next(m Model) adapter.Header {
	return adapter.Header{Schema: 1, Stream: m.st.Stream, Seq: m.st.Seq + 1}
}

func TestReplayCursorSurvivesChanges(t *testing.T) {
	w, h := refSize(t, layer.Drawer)
	replaying := func(t *testing.T) Model {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "graph"})
		m = press(t, m, "[") // m2, the second of three
		if !strings.Contains(lines(m.Frame())[2], "2 / 3") {
			t.Fatalf("bar %q", lines(m.Frame())[2])
		}
		return m
	}
	t.Run("messages removed under the cursor", func(t *testing.T) {
		m := replaying(t)
		for _, id := range []string{"2026-11-16T09-04-00.000Z_m3", "2026-11-16T09-02-00.000Z_m2"} {
			m = step(t, m, adapter.FrameUpdate{Frame: adapter.Remove{Header: next(m), Entity: adapter.EntityMessage, Key: id}})
			m.Frame()
		}
		if !strings.Contains(lines(m.Frame())[2], "1 / 1") {
			t.Fatalf("bar %q: the cursor's message is gone, so Live", lines(m.Frame())[2])
		}
	})
	t.Run("a replacement snapshot with no messages", func(t *testing.T) {
		m := replaying(t)
		snap := frames(t, "recorded-bare")[0]
		m = step(t, m, adapter.FrameUpdate{Frame: snap})
		m = step(t, m, mustTab("graph"))
		if !strings.Contains(lines(m.Frame())[2], "0 / 0") {
			t.Fatalf("bar %q", lines(m.Frame())[2])
		}
	})
	t.Run("hide retired while the cursor is on a retired agent's message", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "graph"})
		// Add a fourth message so [ lands on m3 (to the retired codex-03).
		m4 := adapter.Message{ID: "2026-11-16T09-08-00.000Z_m5", From: "claude-02", To: []string{"claude-01"}, Subject: "done", State: adapter.MsgQueued, CreatedAt: m.now()}
		m = step(t, m, adapter.FrameUpdate{Frame: adapter.MessageUpsert{Header: next(m), Message: m4}})
		m = press(t, m, "[")
		if !contains(m.Frame(), "review s1") {
			t.Fatalf("cursor not on m3:\n%s", plain(m.Frame()))
		}
		m = press(t, m, "h")
		if bar := lines(m.Frame())[2]; !strings.Contains(bar, "3 / 3") {
			t.Fatalf("bar %q: the cursor's message is hidden, so Live", bar)
		}
	})
}
