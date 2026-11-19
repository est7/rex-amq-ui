package ui

import (
	"fmt"
	"image/color"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/est7/rex-amq-ui/internal/adapter"
	"github.com/est7/rex-amq-ui/internal/apc"
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
		if !strings.Contains(lines(m.Frame())[3], "2 / 3") {
			t.Fatalf("bar %q", lines(m.Frame())[3])
		}
		return m
	}
	t.Run("messages removed under the cursor", func(t *testing.T) {
		m := replaying(t)
		for _, id := range []string{"2026-11-16T09-04-00.000Z_m3", "2026-11-16T09-02-00.000Z_m2"} {
			m = step(t, m, adapter.FrameUpdate{Frame: adapter.Remove{Header: next(m), Entity: adapter.EntityMessage, Key: id}})
			m.Frame()
		}
		if !strings.Contains(lines(m.Frame())[3], "1 / 1") {
			t.Fatalf("bar %q: the cursor's message is gone, so Live", lines(m.Frame())[3])
		}
	})
	t.Run("a replacement snapshot with no messages", func(t *testing.T) {
		m := replaying(t)
		snap := frames(t, "recorded-bare")[0]
		m = step(t, m, adapter.FrameUpdate{Frame: snap})
		m = step(t, m, mustTab("graph"))
		if !strings.Contains(lines(m.Frame())[3], "0 / 0") {
			t.Fatalf("bar %q", lines(m.Frame())[3])
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
		if bar := lines(m.Frame())[3]; !strings.Contains(bar, "3 / 3") {
			t.Fatalf("bar %q: the cursor's message is hidden, so Live", bar)
		}
	})
}

func TestLateActionDoesNotRepause(t *testing.T) {
	w, h := refSize(t, layer.Drawer)
	rec := &recorder{}
	m := build(t, "edited-crew-attention", w, h, Deps{Layer: rec}, Options{})
	next, held := m.Update(key("q")) // minimize; its result arrives later
	m = next.(Model)
	m = step(t, m, layer.State{Connected: true, Minimized: true})
	m = step(t, m, layer.State{Connected: true})
	m = step(t, m, mustTab("overview"))
	if m.Paused() {
		t.Fatal("restored layer paused")
	}
	m = step(t, m, held())
	if m.Paused() {
		t.Fatal("the minimize that finished after the panel came back paused it again")
	}
	// A completion that is still current does pause it.
	next, cmd := m.Update(key("q"))
	if m = step(t, next.(Model), cmd()); !m.Paused() {
		t.Fatal("a current minimize did not pause")
	}
}

func TestOverviewScrollsToTheSelection(t *testing.T) {
	m := build(t, "edited-crew-attention", 70, 10, Deps{}, Options{})
	p := *m.st.Panel
	p.Attention = map[string][]adapter.Mark{}
	for i := range 20 {
		p.Attention[fmt.Sprintf("agent-%02d", i)] = []adapter.Mark{{Kind: adapter.MarkStalled, At: m.now().Add(time.Duration(i-30) * time.Minute)}}
	}
	m = step(t, m, adapter.FrameUpdate{Frame: adapter.PanelUpsert{Header: next(m), Panel: p}})
	m = press(t, m, "g")
	for range 19 {
		m = press(t, m, "j")
	}
	if !contains(m.Frame(), "agent-19 stalled") {
		t.Fatalf("the selected alert is off screen:\n%s", plain(m.Frame()))
	}
	m = press(t, m, "G")
	if !contains(m.Frame(), "Hand-back #1") {
		t.Fatalf("G does not reach the hand-back below the alerts:\n%s", plain(m.Frame()))
	}
	m = press(t, m, "g")
	if !contains(m.Frame(), "SYSTEM ALERTS") || !contains(m.Frame(), "agent-00 stalled") {
		t.Fatalf("g does not return to the top:\n%s", plain(m.Frame()))
	}
	if bad := holes(m.Frame(), 70, 10); bad != nil {
		t.Fatal(bad)
	}
}

type movingClock struct{ t *time.Time }

func (c movingClock) Now() time.Time { return *c.t }

func TestObligationAgeAdvances(t *testing.T) {
	now := time.Time(fixtures["edited-crew-attention"])
	m := build(t, "edited-crew-attention", 176, 44, Deps{Clock: movingClock{&now}}, Options{})
	owes := func() string {
		for _, l := range lines(m.Frame()) {
			if strings.Contains(l, "claude-02 owes claude-01") {
				fs := strings.Fields(strings.Trim(l, "│"))
				return fs[len(fs)-1]
			}
		}
		t.Fatalf("no obligation alert:\n%s", plain(m.Frame()))
		return ""
	}
	// m1 was sent at 09:00; at 09:10 the request has waited 10 minutes.
	if got := owes(); got != "10m" {
		t.Fatalf("age %s at 09:10", got)
	}
	now = now.Add(10 * time.Minute)
	m = step(t, m, adapter.FrameUpdate{Frame: adapter.Heartbeat{Header: next(m)}})
	if got := owes(); got != "20m" {
		t.Fatalf("age %s at 09:20: the adapter sends no upsert for an age change", got)
	}
}

func sgrBg(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("48;2;%d;%d;%d", r>>8, g>>8, b>>8)
}

// styleBefore is the last SGR sequence before text on line.
func styleBefore(t *testing.T, line, text string) string {
	t.Helper()
	i := strings.Index(line, text)
	if i < 0 {
		t.Fatalf("%q not on line %q", text, line)
	}
	j := strings.LastIndex(line[:i], "\x1b[")
	return line[j:i]
}

func TestActiveTabAndSelectedRowColours(t *testing.T) {
	w, h := refSize(t, layer.Board)
	m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "members"})
	m = press(t, m, "2")
	ls := strings.Split(m.Frame(), "\n")
	overlay, surface := sgrBg(m.pal.Overlay), sgrBg(m.pal.Surface)
	if st := styleBefore(t, ls[2], " Members"); !strings.Contains(st, overlay) || !strings.HasPrefix(st, "\x1b[1;") {
		t.Errorf("active tab style %q: want bold on the overlay", st)
	}
	if st := styleBefore(t, ls[2], " Overview"); !strings.Contains(st, surface) || strings.Contains(st, overlay) {
		t.Errorf("inactive tab style %q: want the surface", st)
	}
	rightBorder := render(Span{Text: "│", Fg: m.pal.Faint}, m.pal.Bg)
	for _, l := range ls[borderWidth+headerLines:] {
		switch p := plain(l); {
		case strings.Contains(p, "implementer(claude-02)"):
			if !strings.Contains(l, overlay) || !strings.HasSuffix(l, overlay+"m \x1b[m"+rightBorder) {
				t.Errorf("selected row is not on the overlay to its end: %q", l)
			}
		case strings.Contains(p, "orchestrator(claude-01)"), strings.Contains(p, "narrator(pi-04)"):
			if strings.Contains(l, overlay) {
				t.Errorf("unselected row on the overlay: %q", p)
			}
		}
	}
}

func TestResizeWhilePausedWaitsForResume(t *testing.T) {
	w, h := refSize(t, layer.Drawer)
	m := build(t, "edited-crew-attention", w, h, Deps{}, Options{})
	size := tea.WindowSizeMsg{Width: 90, Height: 30}
	if got := Filter(m, size); got != tea.Msg(size) {
		t.Fatalf("a showing panel's resize was filtered: %#v", got)
	}
	m = step(t, m, mustTab("overview"))
	m = step(t, m, apcPause)
	held := Filter(m, size)
	if _, ok := held.(tea.WindowSizeMsg); ok {
		t.Fatal("a paused panel's resize reached the renderer")
	}
	m = step(t, m, held)
	if m.View().Content != m.frozen {
		t.Fatal("the paused frame changed")
	}
	next, cmd := m.Update(apcResume)
	m = next.(Model)
	if cmd == nil {
		t.Fatal("resume did not pass the held size on")
	}
	var passed bool
	for _, c := range flatten(cmd) {
		if s, ok := c().(tea.WindowSizeMsg); ok && s == size {
			passed = true
		}
	}
	if !passed {
		t.Fatal("the held size was not passed to the renderer on resume")
	}
	if bad := holes(m.Frame(), 90, 30); bad != nil {
		t.Fatal(bad)
	}
}

var (
	apcPause  = apc.Command{Verb: apc.Pause}
	apcResume = apc.Command{Verb: apc.Resume}
)

func flatten(cmd tea.Cmd) []tea.Cmd {
	if cmd == nil {
		return nil
	}
	if b, ok := cmd().(tea.BatchMsg); ok {
		var out []tea.Cmd
		for _, c := range b {
			out = append(out, flatten(c)...)
		}
		return out
	}
	return []tea.Cmd{cmd}
}
