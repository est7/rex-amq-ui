package ui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/est7/rex-amq-ui/internal/adapter"
	"github.com/est7/rex-amq-ui/internal/apc"
	"github.com/est7/rex-amq-ui/internal/layer"
)

var allTabs = []string{"overview", "members", "mail", "graph", "epics"}

// TestPaintNoHoles paints every tab of every fixture at the reference
// sizes, a sweep down to nothing, and in the states keys open, and checks
// every cell has a background.
func TestPaintNoHoles(t *testing.T) {
	dw, dh := refSize(t, layer.Drawer)
	bw, bh := refSize(t, layer.Board)
	sizes := [][2]int{{dw, dh}, {bw, bh}, {0, 0}, {0, 5}, {5, 0}, {1, 1}, {1, 5}, {5, 1}, {2, 2}, {2, 5}, {3, 2}, {8, 3}, {20, 6}, {33, 9}, {48, 14}, {200, 50}, {300, 70}}
	modes := map[string][]string{
		"plain":   nil,
		"moved":   {"j", "j", "G"},
		"retired": {"r"},
		"search":  {"/", "界", "s"},
		"filter":  {"p"},
		"excerpt": {"g", "space"},
		"replay":  {"[", "[", "h"},
		"hint":    {"o"}, // no hand-back or no viewer: a hint in the footer
	}
	for fixture := range fixtures {
		for _, tab := range allTabs {
			for mode, keys := range modes {
				for _, sz := range sizes {
					m := build(t, fixture, sz[0], sz[1], Deps{}, Options{Tab: tab})
					m = press(t, m, keys...)
					if bad := holes(m.Frame(), sz[0], sz[1]); bad != nil {
						t.Fatalf("%s/%s/%s %dx%d: %v\n%s", fixture, tab, mode, sz[0], sz[1], bad[0], plain(m.Frame()))
					}
				}
			}
		}
	}
	t.Run("untrusted text", func(t *testing.T) {
		m := build(t, "edited-crew-attention", dw, dh, Deps{}, Options{Tab: "mail"})
		evil := adapter.Message{ID: "z", From: "claude-01\x1b[2J", To: []string{"界面🧑‍💻"}, Subject: "a\tb\nc\x1b]8;;http://x\x07link\x9b31m",
			State: adapter.MsgQueued, CreatedAt: m.now()}
		m = step(t, m, adapter.FrameUpdate{Frame: adapter.MessageUpsert{Header: adapter.Header{Stream: m.st.Stream, Seq: m.st.Seq + 1}, Message: evil}})
		for _, tab := range allTabs[:4] {
			m = step(t, m, apc.Command{Verb: apc.Tab, Tab: tab})
			if bad := holes(m.Frame(), dw, dh); bad != nil {
				t.Fatalf("%s: %v", tab, bad)
			}
		}
		if !strings.Contains(plain(m.Frame()), "界面") {
			t.Fatal("the CJK recipient is missing from the graph summary")
		}
	})
	t.Run("drilled-down breadcrumb and views", func(t *testing.T) {
		for _, sz := range sizes {
			m := build(t, "edited-personal-cockpit", sz[0], sz[1], Deps{}, Options{})
			m = press(t, m, "enter") // Connecting state with breadcrumb
			if bad := holes(m.Frame(), sz[0], sz[1]); bad != nil {
				t.Fatalf("drilled connecting %dx%d: %v\n%s", sz[0], sz[1], bad[0], plain(m.Frame()))
			}
			m = step(t, m, StreamUpdate{Gen: 1, Update: adapter.StatusUpdate{Status: adapter.Connected}})
			for _, f := range frames(t, "edited-crew-attention") {
				m = step(t, m, StreamUpdate{Gen: 1, Update: adapter.FrameUpdate{Frame: f}})
			}
			for _, tab := range allTabs[:4] {
				m = step(t, m, apc.Command{Verb: apc.Tab, Tab: tab})
				if bad := holes(m.Frame(), sz[0], sz[1]); bad != nil {
					t.Fatalf("drilled/%s %dx%d: %v\n%s", tab, sz[0], sz[1], bad[0], plain(m.Frame()))
				}
			}
		}
		// Untrusted control bytes and CJK in breadcrumb labels.
		m := build(t, "edited-personal-cockpit", dw, dh, Deps{}, Options{})
		p := *m.st.Panel
		p.SessionLabel = "hub\x1b[2J界面"
		p.Cockpit = slices.Clone(p.Cockpit)
		p.Cockpit[0].Name = "epic\t\x1b[31m🧑‍💻"
		m = step(t, m, adapter.FrameUpdate{Frame: adapter.PanelUpsert{Header: adapter.Header{Stream: m.st.Stream, Seq: m.st.Seq + 1}, Panel: p}})
		m = press(t, m, "enter")
		if bad := holes(m.Frame(), dw, dh); bad != nil {
			t.Fatalf("drilled untrusted breadcrumb: %v", bad)
		}
	})
}

func lines(frame string) []string { return strings.Split(plain(frame), "\n") }

func contains(frame, s string) bool { return strings.Contains(plain(frame), s) }

// TestViewMapping checks what the reducer's state maps to on screen,
// independently of the goldens.
func TestViewMapping(t *testing.T) {
	w, h := refSize(t, layer.Board)
	t.Run("no alerts, no epics on a bare session", func(t *testing.T) {
		m := build(t, "recorded-bare", w, h, Deps{}, Options{})
		f := m.Frame()
		if contains(f, "SYSTEM ALERTS") || contains(f, "Epics") || !contains(f, "No narrator summary yet") {
			t.Fatalf("bare session:\n%s", plain(f))
		}
		if !contains(f, "rex-amq-ui") || contains(f, "crew:") {
			t.Fatalf("header: %q", lines(f)[1])
		}
		ls := lines(f)
		if !strings.HasPrefix(ls[0], "╭") || !strings.HasSuffix(ls[0], "╮") ||
			!strings.HasPrefix(ls[h-1], "╰") || !strings.HasSuffix(ls[h-1], "╯") {
			t.Fatalf("missing rounded border: top=%q bottom=%q", ls[0], ls[h-1])
		}
	})
	t.Run("alerts: bounced kept, removed dead letter gone, waiting first", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{})
		f := m.Frame()
		for _, s := range []string{"SYSTEM ALERTS  5", "bounced claude-01 → codex-03: review s1", "implementer(claude-02) hit a turn error",
			"claude-02 owes claude-01 a request: build it", "from narrator (pi-04) · 4m ago", "Overview ●5", "crew:pair"} {
			if !contains(f, s) {
				t.Errorf("missing %q", s)
			}
		}
		if contains(f, "lost note") {
			t.Error("the dead letter removed at seq 6 is still shown")
		}
		ls := lines(f)
		if !strings.Contains(ls[4], "is waiting on you") {
			t.Errorf("first alert %q: waiting on the human goes first", ls[4])
		}
		// Alerts sit above the narrator's prose.
		ia, in := slices.IndexFunc(ls, func(s string) bool { return strings.Contains(s, "SYSTEM ALERTS") }), slices.IndexFunc(ls, func(s string) bool { return strings.Contains(s, "from narrator") })
		if ia < 0 || in < ia {
			t.Errorf("alerts at %d, narrator at %d", ia, in)
		}
	})
	t.Run("hand-back card picks out Blocked on me", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{})
		var blocked, plainLine string
		for _, l := range strings.Split(m.Frame(), "\n") {
			switch {
			case strings.Contains(plain(l), "## Blocked on me"):
				blocked = l
			case strings.Contains(plain(l), "run-goal: auth refresh"):
				plainLine = l
			}
		}
		yellow := sgrFg(m.pal.Yellow)
		if !strings.Contains(blocked, yellow) || strings.Contains(plainLine, yellow) {
			t.Fatalf("blocked line %q, other line %q", blocked, plainLine)
		}
	})
	t.Run("a long hand-back renders uncapped with Blocked on me highlighted", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{})
		p := *m.st.Panel
		v := *p.View
		var body strings.Builder
		for i := 1; i <= 12; i++ {
			fmt.Fprintf(&body, "item %02d\n", i)
		}
		body.WriteString("\n## Blocked on me\n- sign the release")
		v.HandBack = &adapter.HandBack{By: "claude-01", Seq: 2, At: m.now(), Body: body.String()}
		p.View = &v
		m = step(t, m, adapter.FrameUpdate{Frame: adapter.PanelUpsert{Header: adapter.Header{Stream: m.st.Stream, Seq: m.st.Seq + 1}, Panel: p}})
		f := m.Frame()
		if !contains(f, "item 01") || !contains(f, "item 12") || !contains(f, "## Blocked on me") || !contains(f, "- sign the release") || contains(f, "more lines") {
			t.Fatalf("\n%s", plain(f))
		}
		yellow := sgrFg(m.pal.Yellow)
		var blockedLine, signLine string
		for _, l := range strings.Split(f, "\n") {
			switch {
			case strings.Contains(plain(l), "## Blocked on me"):
				blockedLine = l
			case strings.Contains(plain(l), "- sign the release"):
				signLine = l
			}
		}
		if !strings.Contains(blockedLine, yellow) || !strings.Contains(signLine, yellow) {
			t.Fatalf("expected yellow Blocked on me lines: header=%q item=%q", blockedLine, signLine)
		}
	})
	t.Run("no-view session shows the quiet narrator line", func(t *testing.T) {
		m := build(t, "edited-no-view", w, h, Deps{}, Options{})
		if f := m.Frame(); !contains(f, "No narrator summary yet") || !contains(f, "Hand-back #2 · claude-07") {
			t.Fatalf("\n%s", plain(f))
		}
	})
	t.Run("members grouped, lead on top, focused pane marked", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "members"})
		ls := lines(m.Frame())
		order := []string{"Lead", "orchestrator(claude-01)", "Active", "implementer(claude-02)", "narrator(pi-04)", "Retired 1"}
		at := 0
		for _, l := range ls {
			if at < len(order) && strings.Contains(l, order[at]) {
				at++
			}
		}
		if at != len(order) {
			t.Fatalf("order stopped before %q:\n%s", order[at], strings.Join(ls, "\n"))
		}
		for _, l := range ls {
			if strings.Contains(l, "▶") != strings.Contains(l, "claude-02") && strings.Contains(l, "claude-0") {
				t.Errorf("▶ on the wrong row: %q", l)
			}
		}
		if contains(m.Frame(), "codex-03") {
			t.Error("retired member shown while the group is closed")
		}
		m = press(t, m, "r")
		if f := m.Frame(); !contains(f, "reviewer(codex-03)") || !contains(f, "retired · 25m ago") {
			t.Fatalf("retired group opened:\n%s", plain(f))
		}
		// claude-02 goes working in the stream: it moved from Standing by
		// (in the snapshot) to Active.
		if a, _ := m.agent("claude-02"); m.groupOf(a) != groupActive {
			t.Fatalf("claude-02 in group %v", m.groupOf(a))
		}
	})
	t.Run("mail: chronological, states, excerpt only where authorised", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "mail"})
		ls := lines(m.Frame())
		i1 := slices.IndexFunc(ls, func(s string) bool { return strings.Contains(s, "build it") })
		i3 := slices.IndexFunc(ls, func(s string) bool { return strings.Contains(s, "review s1") })
		if i1 < 0 || i3 < i1 || !strings.Contains(ls[i1], "drained") || !strings.Contains(ls[i3], "bounced") || !strings.Contains(ls[i1], "▸") {
			t.Fatalf("\n%s", strings.Join(ls, "\n"))
		}
		m = press(t, m, "g", "enter")
		if !contains(m.Frame(), "Wire the token refresh.") {
			t.Fatalf("m1's excerpt did not open:\n%s", plain(m.Frame()))
		}
		m = press(t, m, "esc", "j", "space")
		if contains(m.Frame(), "Wire the token refresh.") || !contains(m.Frame(), "not allowed to read") {
			t.Fatalf("m2 has no excerpt to open:\n%s", plain(m.Frame()))
		}
	})
	t.Run("mail search and participant filter", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "mail"})
		// Search matches Subject, From, To, and authorized excerpt Body (case-insensitive).
		m = press(t, m, "/", "S", "C", "O", "enter")
		if f := m.Frame(); !contains(f, "question about scope") || contains(f, "build it") {
			t.Fatalf("search subject:\n%s", plain(f))
		}
		m = press(t, m, "esc") // clears the search
		m = press(t, m, "/", "C", "O", "D", "E", "X", "enter")
		if f := m.Frame(); !contains(f, "review s1") || contains(f, "build it") {
			t.Fatalf("search To:\n%s", plain(f))
		}
		m = press(t, m, "esc")
		m = press(t, m, "/", "T", "O", "K", "E", "N", "enter")
		if f := m.Frame(); !contains(f, "build it") || contains(f, "review s1") {
			t.Fatalf("search excerpt Body:\n%s", plain(f))
		}
		m = press(t, m, "esc")

		// p cycles the currently selected message's participants: From -> primary To -> clear,
		// regardless of TabMembers's cursor. Move TabMembers cursor to pi-04 (row 3) to prove independence.
		m = step(t, m, apc.Command{Verb: apc.Tab, Tab: "members"})
		m = press(t, m, "3")
		m = step(t, m, apc.Command{Verb: apc.Tab, Tab: "mail"})
		// Select m3 (claude-01 -> codex-03, "review s1") explicitly and press p.
		m = press(t, m, "G", "p")
		if f := m.Frame(); !contains(f, "from/to claude-01") || !contains(f, "review s1") || !contains(f, "build it") {
			t.Fatalf("1st p (From=claude-01):\n%s", plain(f))
		}
		if cur, ok := m.selectedMail(); !ok || cur.ID != "2026-11-16T09-04-00.000Z_m3" {
			t.Fatalf("selection not anchored on m3 after 1st p: %+v", cur)
		}
		m = press(t, m, "p")
		if f := m.Frame(); !contains(f, "from/to codex-03") || !contains(f, "review s1") || contains(f, "build it") {
			t.Fatalf("2nd p (To=codex-03):\n%s", plain(f))
		}
		if cur, ok := m.selectedMail(); !ok || cur.ID != "2026-11-16T09-04-00.000Z_m3" {
			t.Fatalf("selection not anchored on m3 after 2nd p: %+v", cur)
		}
		m = press(t, m, "p")
		if f := m.Frame(); contains(f, "from/to ") || !contains(f, "build it") || !contains(f, "review s1") {
			t.Fatalf("3rd p (clear):\n%s", plain(f))
		}
		if cur, ok := m.selectedMail(); !ok || cur.ID != "2026-11-16T09-04-00.000Z_m3" {
			t.Fatalf("selection not anchored on m3 after 3rd p: %+v", cur)
		}
	})
	t.Run("epics: first and default in a personal session", func(t *testing.T) {
		m := build(t, "edited-personal-cockpit", w, h, Deps{}, Options{})
		ls := lines(m.Frame())
		if !strings.HasPrefix(strings.TrimSpace(strings.Trim(ls[2], "│")), "Epics 2") || m.tab != TabEpics {
			t.Fatalf("tab bar %q, tab %v", ls[2], m.tab)
		}
		f := m.Frame()
		for _, s := range []string{"auth-refresh", "/Users/human/projects/auth", "opened", "hand-back", "!2", "billing", "creating", "no narrator yet"} {
			if !contains(f, s) {
				t.Errorf("missing %q", s)
			}
		}
	})
	t.Run("epics: present but empty with an empty cockpit", func(t *testing.T) {
		fs := frames(t, "edited-personal-cockpit")
		snap := fs[0].(adapter.Snapshot)
		p := *snap.Panel
		p.Cockpit = []adapter.Epic{}
		snap.Panel = &p
		m := New(Deps{Clock: fixtures["edited-personal-cockpit"]}, Options{})
		m = step(t, m, tea.WindowSizeMsg{Width: w, Height: h})
		m = step(t, m, adapter.FrameUpdate{Frame: snap})
		if f := m.Frame(); !contains(f, "Epics 0") || !contains(f, "No Epics are linked") {
			t.Fatalf("\n%s", plain(f))
		}
	})
	t.Run("status line follows the stream", func(t *testing.T) {
		m := build(t, "recorded-bare", w, h, Deps{}, Options{})
		m = step(t, m, adapter.StatusUpdate{Status: adapter.Reconnecting, Err: errors.New("adapter: exit status 1")})
		if !strings.Contains(lines(m.Frame())[1], "◌ Reconnecting") {
			t.Fatalf("%q", lines(m.Frame())[1])
		}
		fresh := New(Deps{Clock: fixtures["recorded-bare"]}, Options{})
		fresh = step(t, fresh, tea.WindowSizeMsg{Width: w, Height: h})
		fresh = step(t, fresh, adapter.StatusUpdate{Status: adapter.Reconnecting, Err: errors.New("adapter: rex-amq-adapter not found")})
		if f := fresh.Frame(); !contains(f, "Waiting for rex-amq-adapter") || !contains(f, "not found") {
			t.Fatalf("\n%s", plain(f))
		}
		refused := New(Deps{Clock: fixtures["recorded-bare"]}, Options{})
		refused = step(t, refused, tea.WindowSizeMsg{Width: w, Height: h})
		refused = step(t, refused, adapter.StatusUpdate{Status: adapter.Refused, Err: &adapter.EnvelopeError{Code: "guard_denied", Message: "this command is the human's"}})
		if f := refused.Frame(); !strings.Contains(lines(f)[1], "✕ Refused") || !contains(f, "guard_denied: this command is the human's") || contains(f, "retrying in") {
			t.Fatalf("\n%s", plain(f))
		}
	})
	t.Run("graph region and replay bar", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "graph"})
		r := m.GraphRegion()
		if r != (Rect{X: 1, Y: 4, W: w - 2, H: h - 8}) {
			t.Fatalf("region %+v", r)
		}
		ls := lines(m.Frame())
		for y := r.Y; y < r.Y+r.H; y++ {
			if ls[y] != "│"+strings.Repeat(" ", w-2)+"│" {
				t.Fatalf("region row %d not framed blank: %q", y, ls[y])
			}
		}
		if !strings.Contains(ls[3], "3 / 3 messages · 4m00s") || !strings.Contains(ls[r.Y+r.H], "drained") {
			t.Fatalf("bar %q legend %q", ls[3], ls[r.Y+r.H])
		}
		m = press(t, m, "[")
		if !strings.Contains(lines(m.Frame())[3], "2 / 3") || !contains(m.Frame(), "question about scope") {
			t.Fatalf("replay step back:\n%s", plain(m.Frame()))
		}
		m = press(t, m, "h")
		if !strings.Contains(lines(m.Frame())[3], "/ 2 messages") {
			t.Fatalf("hide retired: %q", lines(m.Frame())[3])
		}
		m = press(t, m, "L")
		if m.graphAt != "" {
			t.Fatal("L did not return to Live")
		}
	})
}

// sgrFg is the SGR fragment Lipgloss writes for a foreground colour.
func sgrFg(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
}

func TestKeys(t *testing.T) {
	w, h := refSize(t, layer.Drawer)
	t.Run("tab cycles through the shown tabs", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{})
		var seen []Tab
		for range 5 {
			seen = append(seen, m.tab)
			m = press(t, m, "tab")
		}
		if want := []Tab{TabOverview, TabMembers, TabMail, TabGraph, TabOverview}; !reflect.DeepEqual(seen, want) {
			t.Fatalf("tab order %v", seen)
		}
		m = press(t, m, "shift+tab", "shift+tab")
		if m.tab != TabGraph {
			t.Fatalf("shift+tab went to %v", m.tab)
		}
	})
	t.Run("z zooms, enter jumps, q steps back then hides", func(t *testing.T) {
		rec := &recorder{}
		m := build(t, "edited-crew-attention", w, h, Deps{Layer: rec}, Options{Tab: "members"})
		m = press(t, m, "z")
		if m.size != layer.Board || !strings.Contains(lines(m.Frame())[1], "Board") {
			t.Fatalf("size %v", m.size)
		}
		m = press(t, m, "z", "2", "enter")
		if want := []string{"resize Board", "resize Drawer", "jump block:claude-02"}; !reflect.DeepEqual(rec.got(), want) {
			t.Fatalf("calls %v", rec.got())
		}
		if !m.Paused() {
			t.Fatal("the panel did not pause after jumping away")
		}
		m = step(t, m, apc.Command{Verb: apc.Resume})
		m = press(t, m, "r", "q")
		if m.retired || len(rec.got()) != 3 {
			t.Fatalf("q should close Retired first: retired=%v calls %v", m.retired, rec.got())
		}
		m = press(t, m, "esc")
		if got := rec.got(); got[len(got)-1] != "minimize" || !m.Paused() {
			t.Fatalf("esc at the top: %v paused=%v", got, m.Paused())
		}
	})
	t.Run("a member without a pane gets a hint, not a jump", func(t *testing.T) {
		rec := &recorder{}
		m := build(t, "edited-crew-attention", w, h, Deps{Layer: rec}, Options{Tab: "members"})
		m = press(t, m, "r", "4", "enter")
		if len(rec.got()) != 0 || !contains(m.Frame(), "codex-03 has no pane to go to") || m.Paused() {
			t.Fatalf("calls %v\n%s", rec.got(), plain(m.Frame()))
		}
	})
	t.Run("enter on an alert jumps to its agent", func(t *testing.T) {
		rec := &recorder{}
		m := build(t, "edited-crew-attention", w, h, Deps{Layer: rec}, Options{})
		m = press(t, m, "j", "enter")
		if !reflect.DeepEqual(rec.got(), []string{"jump block:claude-02"}) {
			t.Fatalf("calls %v", rec.got())
		}
	})
	t.Run("s on an epic switches; refusal leaves a hint and the panel up", func(t *testing.T) {
		rec := &recorder{fail: map[string]error{"switch": layer.ErrRemoteControlOff}}
		m := build(t, "edited-personal-cockpit", w, h, Deps{Layer: rec}, Options{})
		m = press(t, m, "2", "s")
		if !reflect.DeepEqual(rec.got(), []string{"switch session:t"}) || m.Paused() {
			t.Fatalf("calls %v paused %v", rec.got(), m.Paused())
		}
		if foot := lines(m.Frame())[h-2]; !strings.Contains(foot, "Remote Control") {
			t.Fatalf("footer %q", foot)
		}
	})
	t.Run("a key on a paused panel resumes it; Q quits", func(t *testing.T) {
		rec := &recorder{}
		m := build(t, "edited-crew-attention", w, h, Deps{Layer: rec}, Options{})
		m = step(t, m, apc.Command{Verb: apc.Pause})
		// Lua only writes APC: a key means the human is looking at it.
		m = press(t, m, "tab")
		if m.Paused() || m.tab != TabMembers {
			t.Fatalf("paused=%v tab=%v", m.Paused(), m.tab)
		}
		m = step(t, m, apc.Command{Verb: apc.Pause})
		_, cmd := m.Update(key("Q"))
		if cmd == nil {
			t.Fatal("Q did nothing")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatal("Q did not quit")
		}
	})
	t.Run("apc tab and zoom", func(t *testing.T) {
		rec := &recorder{}
		m := build(t, "edited-crew-attention", w, h, Deps{Layer: rec}, Options{})
		m = step(t, m, apc.Command{Verb: apc.Tab, Tab: "mail"})
		if m.tab != TabMail {
			t.Fatalf("tab %v", m.tab)
		}
		m = step(t, m, apc.Command{Verb: apc.Tab, Tab: "epics"}) // not a personal session
		if m.tab != TabMail {
			t.Fatalf("tab:epics switched to a hidden tab")
		}
		next, cmd := m.Update(apc.Command{Verb: apc.Zoom})
		drain(t, next.(Model), cmd)
		if !reflect.DeepEqual(rec.got(), []string{"resize Board"}) {
			t.Fatalf("calls %v", rec.got())
		}
	})
	t.Run("zoom persists size to CacheDir on z and apc zoom", func(t *testing.T) {
		dir := t.TempDir()
		rec := &recorder{}
		m := build(t, "edited-crew-attention", w, h, Deps{Layer: rec, CacheDir: dir}, Options{})
		m = press(t, m, "z")
		if got, ok := layer.LoadSize(dir); !ok || got != layer.Board {
			t.Fatalf("after z: persisted (%v, %v), want (Board, true)", got, ok)
		}
		next, cmd := m.Update(apc.Command{Verb: apc.Zoom})
		m = drain(t, next.(Model), cmd)
		if got, ok := layer.LoadSize(dir); !ok || got != layer.Drawer {
			t.Fatalf("after apc zoom: persisted (%v, %v), want (Drawer, true)", got, ok)
		}

		// Failed resize must not overwrite persisted size.
		failRec := &recorder{fail: map[string]error{"resize": errors.New("boom")}}
		m2 := build(t, "edited-crew-attention", w, h, Deps{Layer: failRec, CacheDir: dir}, Options{})
		m2 = press(t, m2, "z")
		if got, ok := layer.LoadSize(dir); !ok || got != layer.Drawer {
			t.Fatalf("after failed resize: persisted (%v, %v), want unchanged (Drawer, true)", got, ok)
		}

		// Save failure still updates m.size and reports a footer hint.
		blocker := filepath.Join(dir, "not-a-dir")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		m3 := build(t, "edited-crew-attention", w, h, Deps{Layer: rec, CacheDir: blocker}, Options{})
		m3 = press(t, m3, "z")
		if m3.size != layer.Board || !strings.Contains(m3.hint, "save size failed") || !m3.hintBad {
			t.Fatalf("save failure: size=%v hint=%q bad=%v", m3.size, m3.hint, m3.hintBad)
		}
	})
	t.Run("without Rex, actions say so and why", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{})
		m = press(t, m, "z")
		if !contains(m.Frame(), "not connected to Rex") {
			t.Fatalf("\n%s", plain(m.Frame()))
		}
		down := &recorder{fail: map[string]error{"resize": fmt.Errorf("%w: dial unix /x.sock: connect: no such file", layer.ErrDisconnected)}}
		m = build(t, "edited-crew-attention", w, h, Deps{Layer: down}, Options{})
		m = step(t, m, layer.State{})
		if !strings.Contains(lines(m.Frame())[1], "◌ Rex") {
			t.Fatalf("header %q: a panel that lost Rex should say so", lines(m.Frame())[1])
		}
		m = press(t, m, "z")
		if !contains(m.Frame(), "not connected to Rex: dial unix /x.sock") {
			t.Fatalf("\n%s", plain(m.Frame()))
		}
	})
}

func TestPause(t *testing.T) {
	w, h := refSize(t, layer.Drawer)
	fs := frames(t, "edited-crew-attention")
	m := New(Deps{Clock: fixtures["edited-crew-attention"]}, Options{})
	m = step(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	m = step(t, m, adapter.FrameUpdate{Frame: fs[0]})
	shown := m.View().Content
	m = step(t, m, apc.Command{Verb: apc.Pause})
	for _, f := range fs[1:] {
		m = step(t, m, adapter.FrameUpdate{Frame: f})
		if m.View().Content != shown {
			t.Fatal("a paused panel drew a new frame")
		}
	}
	if m.st.Seq != fs[len(fs)-1].Head().Seq {
		t.Fatalf("frames stopped folding while paused: seq %d", m.st.Seq)
	}
	m = step(t, m, apc.Command{Verb: apc.Resume})
	if got := m.View().Content; got == shown || !contains(got, "is waiting on you") {
		t.Fatal("resume did not draw the current state")
	}

	// Minimized by hand: the layer's own state pauses and resumes it.
	m = step(t, m, layer.State{Connected: true, Minimized: true})
	if !m.Paused() {
		t.Fatal("minimized layer still drawing")
	}
	m = step(t, m, layer.State{Connected: true, Minimized: true, Focused: "block:x"})
	if !m.Paused() {
		t.Fatal("another minimized state resumed it")
	}
	m = step(t, m, layer.State{Connected: true})
	if m.Paused() {
		t.Fatal("restored layer still paused")
	}
	if m.lay.Focused != "block:x" {
		t.Fatalf("focus %q", m.lay.Focused)
	}
}

func TestTheme(t *testing.T) {
	w, h := refSize(t, layer.Drawer)
	asks := func(m Model, msg tea.Msg) (Model, bool) {
		next, cmd := m.Update(msg)
		return next.(Model), cmd != nil
	}
	m := build(t, "recorded-bare", w, h, Deps{}, Options{})
	m, asked := asks(m, layer.State{Connected: true})
	if asked {
		t.Fatal("asked for colours before anyone owns the block")
	}
	m = step(t, m, apc.Command{Verb: apc.Pause})
	if m, asked = asks(m, layer.State{Connected: true, Owner: "client:app"}); asked {
		t.Fatal("asked for colours while paused")
	}
	if m, asked = asks(m, apc.Command{Verb: apc.Resume}); !asked {
		t.Fatal("did not ask on resume once owned")
	}
	if m, asked = asks(m, layer.State{Connected: true, Owner: "client:app", Focused: "b"}); asked {
		t.Fatal("asked the same owner twice")
	}
	dark := m.Frame()
	m = step(t, m, tea.ForegroundColorMsg{Color: color.RGBA{0x30, 0x30, 0x30, 0xff}})
	m = step(t, m, tea.BackgroundColorMsg{Color: color.RGBA{0xfa, 0xfa, 0xfa, 0xff}})
	if m.pal.Dark || !strings.Contains(m.Frame(), "48;2;250;250;250") || m.Frame() == dark {
		t.Fatal("a light terminal background did not switch the palette")
	}
	if bad := holes(m.Frame(), w, h); bad != nil {
		t.Fatal(bad)
	}
	if _, asked = asks(m, layer.State{Connected: true, Owner: "client:other"}); asked {
		t.Fatal("asked again after the terminal answered")
	}

	// The launcher owns the block for a moment before Rex.app takes it:
	// a query then goes unanswered, so the app's claim asks again.
	fresh := build(t, "recorded-bare", w, h, Deps{}, Options{})
	fresh, asked = asks(fresh, layer.State{Connected: true, Owner: "client:launcher"})
	if !asked {
		t.Fatal("did not ask when owned at attach")
	}
	fresh, _ = asks(fresh, layer.State{Connected: true})
	if _, asked = asks(fresh, layer.State{Connected: true, Owner: "client:app"}); !asked {
		t.Fatal("did not ask the app that took the block over")
	}
}

func TestHandBack(t *testing.T) {
	w, h := refSize(t, layer.Drawer)
	type ran struct {
		env  []string
		name string
		args []string
	}
	t.Run("md-viewer beside the human's pane, then minimize", func(t *testing.T) {
		dir := t.TempDir()
		rec := &recorder{}
		var runs []ran
		d := Deps{Layer: rec, CacheDir: dir,
			LookPath: func(string) (string, error) { return "/opt/bin/md-viewer", nil },
			Exec: func(_ context.Context, env []string, name string, args ...string) error {
				runs = append(runs, ran{env, name, args})
				return nil
			}}
		m := build(t, "edited-crew-attention", w, h, d, Options{})
		m = press(t, m, "o")
		path := filepath.Join(dir, "rex-amq-ui", "session_s-hand-back-1.md")
		if len(runs) != 1 || runs[0].name != "/opt/bin/md-viewer" || !reflect.DeepEqual(runs[0].args, []string{"open", path}) ||
			!reflect.DeepEqual(runs[0].env, []string{"REX_BLOCK=" + focusedPane}) {
			t.Fatalf("runs %+v", runs)
		}
		b, err := os.ReadFile(path)
		fi, _ := os.Stat(path)
		if err != nil || string(b) != "run-goal: auth refresh\n\n## Blocked on me\n- release v2 tag" || fi.Mode().Perm() != 0o444 {
			t.Fatalf("file %q %v %v", b, err, fi.Mode())
		}
		if !reflect.DeepEqual(rec.got(), []string{"minimize"}) || !m.Paused() {
			t.Fatalf("calls %v", rec.got())
		}
		// Again: the read-only copy is replaced.
		m = step(t, m, apc.Command{Verb: apc.Resume})
		m = press(t, m, "o")
		if len(runs) != 2 || m.hint != "" {
			t.Fatalf("second open: runs %d hint %q", len(runs), m.hint)
		}
	})
	t.Run("editor split when md-viewer is missing", func(t *testing.T) {
		rec := &recorder{}
		d := Deps{Layer: rec, CacheDir: t.TempDir(), Editor: "nvim -R",
			LookPath: func(string) (string, error) { return "", errors.New("not found") },
			Exec:     func(context.Context, []string, string, ...string) error { t.Fatal("ran md-viewer"); return nil }}
		m := build(t, "edited-crew-attention", w, h, d, Options{})
		m = press(t, m, "o")
		got := rec.got()
		if len(got) != 2 || !strings.HasPrefix(got[0], "beside "+focusedPane+" hand-back [nvim -R ") || got[1] != "minimize" {
			t.Fatalf("calls %v", got)
		}
	})
	t.Run("neither: a hint, the panel stays", func(t *testing.T) {
		rec := &recorder{}
		d := Deps{Layer: rec, CacheDir: t.TempDir(), LookPath: func(string) (string, error) { return "", errors.New("no") }}
		m := build(t, "edited-crew-attention", w, h, d, Options{})
		m = press(t, m, "o")
		if len(rec.got()) != 0 || m.Paused() || !contains(m.Frame(), "install md-viewer") {
			t.Fatalf("calls %v\n%s", rec.got(), plain(m.Frame()))
		}
	})
	t.Run("no hand-back yet", func(t *testing.T) {
		m := build(t, "recorded-bare", w, h, Deps{CacheDir: t.TempDir()}, Options{})
		m = press(t, m, "o")
		if !contains(m.Frame(), "no hand-back yet") {
			t.Fatalf("\n%s", plain(m.Frame()))
		}
	})
	t.Run("o on Epics opens selected epic hand-back and hints when absent", func(t *testing.T) {
		dir := t.TempDir()
		rec := &recorder{}
		var runs []ran
		d := Deps{Layer: rec, CacheDir: dir,
			LookPath: func(string) (string, error) { return "/opt/bin/md-viewer", nil },
			Exec: func(_ context.Context, env []string, name string, args ...string) error {
				runs = append(runs, ran{env, name, args})
				return nil
			}}
		m := build(t, "edited-personal-cockpit", w, h, d, Options{})
		m = press(t, m, "o")
		path := filepath.Join(dir, "rex-amq-ui", "session_s-hand-back-1.md")
		if len(runs) != 1 || runs[0].name != "/opt/bin/md-viewer" || !reflect.DeepEqual(runs[0].args, []string{"open", path}) ||
			!reflect.DeepEqual(runs[0].env, []string{"REX_BLOCK=" + focusedPane}) {
			t.Fatalf("runs %+v", runs)
		}
		b, err := os.ReadFile(path)
		fi, _ := os.Stat(path)
		if err != nil || string(b) != "run-goal: auth refresh\n\n## Blocked on me\n- release v2 tag" || fi.Mode().Perm() != 0o444 {
			t.Fatalf("file %q %v %v", b, err, fi.Mode())
		}
		if !reflect.DeepEqual(rec.got(), []string{"minimize"}) || !m.Paused() {
			t.Fatalf("calls %v paused %v", rec.got(), m.Paused())
		}

		// Row 2 (billing) has no hand-back yet.
		m = step(t, m, apc.Command{Verb: apc.Resume})
		m = press(t, m, "2", "o")
		if len(runs) != 1 || m.Paused() || !contains(m.Frame(), "no hand-back yet") {
			t.Fatalf("row 2 without hand-back: runs %d paused %v\n%s", len(runs), m.Paused(), plain(m.Frame()))
		}
	})
}

func TestDrillDown(t *testing.T) {
	w, h := refSize(t, layer.Drawer)
	type subCall struct {
		session string
		gen     uint64
	}
	t.Run("enter drills in-place, guards stale updates, and pops back on esc/backspace/q", func(t *testing.T) {
		rec := &recorder{}
		var subs []subCall
		d := Deps{
			Layer: rec,
			Subscribe: func(session string, gen uint64) {
				subs = append(subs, subCall{session, gen})
			},
		}
		m := build(t, "edited-personal-cockpit", w, h, d, Options{Session: "session:p"})
		// Select row 2 first to verify hub selection preservation across popBack,
		// then back to row 1 (auth-refresh) and drill in.
		m = press(t, m, "enter")
		if !m.DrilledDown() || m.tab != TabOverview || m.Paused() || len(rec.got()) != 0 {
			t.Fatalf("drilled=%v tab=%v paused=%v calls=%v", m.DrilledDown(), m.tab, m.Paused(), rec.got())
		}
		if want := []subCall{{"session:s", 1}}; !reflect.DeepEqual(subs, want) {
			t.Fatalf("subs %+v, want %+v", subs, want)
		}
		if want := []Tab{TabOverview, TabMembers, TabMail, TabGraph}; !reflect.DeepEqual(m.tabs(), want) {
			t.Fatalf("tabs %v, want %v", m.tabs(), want)
		}
		// Header shows breadcrumb and Connecting before Epic snapshot arrives.
		hdr := lines(m.Frame())[1]
		if !strings.Contains(hdr, "← me › auth-refresh") || !strings.Contains(hdr, "◌ Connecting") {
			t.Fatalf("header before snapshot: %q", hdr)
		}
		foot := lines(m.Frame())[h-2]
		if !strings.Contains(foot, "s switch") || !strings.Contains(foot, "esc back") {
			t.Fatalf("drilled footer: %q", foot)
		}
		// Stale Hub updates (gen 0) are dropped and must not corrupt the Epic view.
		hubSnap := frames(t, "edited-personal-cockpit")[0]
		m = step(t, m, StreamUpdate{Gen: 0, Update: adapter.StatusUpdate{Status: adapter.Connected}})
		m = step(t, m, StreamUpdate{Gen: 0, Update: adapter.FrameUpdate{Frame: hubSnap}})
		if m.st.Ready() || m.status.Status != adapter.Connecting {
			t.Fatalf("stale gen 0 corrupted drilled state: ready=%v status=%v", m.st.Ready(), m.status.Status)
		}
		// Current Epic updates (gen 1) populate the view and add crew to breadcrumb.
		m = step(t, m, StreamUpdate{Gen: 1, Update: adapter.StatusUpdate{Status: adapter.Connected}})
		for _, f := range frames(t, "edited-crew-attention") {
			m = step(t, m, StreamUpdate{Gen: 1, Update: adapter.FrameUpdate{Frame: f}})
		}
		hdr = lines(m.Frame())[1]
		if !strings.Contains(hdr, "← me › auth-refresh · crew:pair") || !strings.Contains(hdr, "● Live") {
			t.Fatalf("header after snapshot: %q", hdr)
		}
		if !contains(m.Frame(), "SYSTEM ALERTS  5") {
			t.Fatalf("epic overview missing alerts:\n%s", plain(m.Frame()))
		}
		// APC tab:epics is ignored while drilled down.
		m = step(t, m, mustTab("epics"))
		if m.tab == TabEpics {
			t.Fatal("tab:epics must stay unavailable while drilled down")
		}
		// Tab-local stepBack unwinds before popping back to Hub.
		m = step(t, m, mustTab("members"))
		m = press(t, m, "r", "backspace")
		if !m.DrilledDown() || m.retired {
			t.Fatalf("backspace should close Retired before popping: drilled=%v retired=%v", m.DrilledDown(), m.retired)
		}
		// Enter on a drilled-down member calls JumpToSession, not same-session Jump.
		m = press(t, m, "2", "enter")
		if want := []string{"jump-session session:s block:claude-02"}; !reflect.DeepEqual(rec.got(), want) {
			t.Fatalf("drilled member enter calls %v, want %v", rec.got(), want)
		}
		m = step(t, m, apc.Command{Verb: apc.Resume})
		// s inside drilled-down Epic switches to the Epic session.
		m = press(t, m, "s")
		if got := rec.got(); got[len(got)-1] != "switch session:s" {
			t.Fatalf("s inside drilled-down epic: calls %v", got)
		}
		m = step(t, m, apc.Command{Verb: apc.Resume})
		// Typing 's' in Mail search does not trigger session switch.
		beforeCalls := len(rec.got())
		m = step(t, m, mustTab("mail"))
		m = press(t, m, "/", "s", "enter")
		if len(rec.got()) != beforeCalls || m.mail.query != "s" {
			t.Fatalf("typing s in mail search switched or failed: calls %v query %q", rec.got(), m.mail.query)
		}
		// First esc clears Mail search query, stays drilled down.
		m = press(t, m, "esc")
		if !m.DrilledDown() || m.mail.query != "" {
			t.Fatalf("esc should clear search first: drilled=%v query=%q", m.DrilledDown(), m.mail.query)
		}
		// Second esc pops back to Hub Epics immediately with saved state and resubscribes.
		m = press(t, m, "esc")
		if m.DrilledDown() || m.tab != TabEpics || !m.st.Ready() || m.status.Status != adapter.Connected {
			t.Fatalf("after esc pop: drilled=%v tab=%v ready=%v status=%v", m.DrilledDown(), m.tab, m.st.Ready(), m.status.Status)
		}
		if want := []subCall{{"session:s", 1}, {"session:p", 2}}; !reflect.DeepEqual(subs, want) {
			t.Fatalf("subs %+v, want %+v", subs, want)
		}
		// Stale Epic update (gen 1) is dropped after returning to Hub.
		m = step(t, m, StreamUpdate{Gen: 1, Update: adapter.FrameUpdate{Frame: frames(t, "edited-crew-attention")[0]}})
		if !m.personal() || len(m.epics()) != 2 {
			t.Fatalf("stale epic update corrupted restored hub state: personal=%v epics=%d", m.personal(), len(m.epics()))
		}
		// Backspace at root does NOT minimize the panel.
		beforeCalls = len(rec.got())
		m = press(t, m, "backspace")
		if len(rec.got()) != beforeCalls || m.Paused() {
			t.Fatalf("backspace at root minimized panel: calls %v paused=%v", rec.got(), m.Paused())
		}
		// Verify Backspace and q also pop back while preserving row 2 selection.
		for _, popKey := range []string{"backspace", "q"} {
			m = press(t, m, "2", "enter")
			if !m.DrilledDown() || !strings.Contains(lines(m.Frame())[1], "← me › billing") {
				t.Fatalf("drill row 2 (%s): drilled=%v hdr=%q", popKey, m.DrilledDown(), lines(m.Frame())[1])
			}
			m = press(t, m, popKey)
			if m.DrilledDown() || m.tab != TabEpics || m.sel[TabEpics] != 1 {
				t.Fatalf("pop on %s: drilled=%v tab=%v sel=%d", popKey, m.DrilledDown(), m.tab, m.sel[TabEpics])
			}
		}
	})

	t.Run("hub session fallback from Options.Session and breadcrumb label fallbacks", func(t *testing.T) {
		var subs []subCall
		d := Deps{Subscribe: func(session string, gen uint64) { subs = append(subs, subCall{session, gen}) }}
		fs := frames(t, "edited-personal-cockpit")
		snap := fs[0].(adapter.Snapshot)
		snap.Scope.SessionID = "" // empty scope session: must fall back to Options.Session
		p := *snap.Panel
		p.SessionLabel = "" // empty hub label: must fall back to "hub"
		p.Cockpit = slices.Clone(p.Cockpit)
		p.Cockpit[0].Name = "" // empty epic name: falls back to stream SessionLabel once arrived
		snap.Panel = &p

		m := New(d, Options{Session: "session:hub-opt"})
		m = step(t, m, tea.WindowSizeMsg{Width: w, Height: h})
		m = step(t, m, adapter.StatusUpdate{Status: adapter.Connected})
		m = step(t, m, adapter.FrameUpdate{Frame: snap})
		m = press(t, m, "enter")
		if hdr := lines(m.Frame())[1]; !strings.Contains(hdr, "← hub › session:s") {
			t.Fatalf("before epic snapshot header: %q", hdr)
		}
		m = step(t, m, StreamUpdate{Gen: 1, Update: adapter.FrameUpdate{Frame: frames(t, "edited-crew-attention")[0]}})
		if hdr := lines(m.Frame())[1]; !strings.Contains(hdr, "← hub › auth-refresh") {
			t.Fatalf("after epic snapshot header: %q", hdr)
		}
		m = press(t, m, "esc")
		if want := []subCall{{"session:s", 1}, {"session:hub-opt", 2}}; !reflect.DeepEqual(subs, want) {
			t.Fatalf("subs %+v, want %+v", subs, want)
		}
	})
}

func TestUncappedWrappingAndLineScrolling(t *testing.T) {
	t.Run("wrap preserves paragraphs, blank lines, and lists", func(t *testing.T) {
		got := wrap("# Title\n\n- first bullet item\n- second bullet\r\n\r\nfinal paragraph\n", 40)
		want := []string{"# Title", "", "- first bullet item", "- second bullet", "", "final paragraph"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("wrap = %#v, want %#v", got, want)
		}
	})

	t.Run("uncapped mail excerpt shows ellipsis only when adapter truncated it", func(t *testing.T) {
		w, h := refSize(t, layer.Board)
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "mail"})
		p := *m.st.Panel
		v := *p.View
		var body strings.Builder
		for i := 1; i <= 10; i++ {
			fmt.Fprintf(&body, "paragraph %02d\n", i)
		}
		v.ControlPlane = []adapter.ViewMessage{
			{Ref: "2026-11-16T09-00-00.000Z_m1", Body: body.String(), Truncated: false},
			{Ref: "2026-11-16T09-02-00.000Z_m2", Body: "cut short body", Truncated: true},
		}
		p.View = &v
		m = step(t, m, adapter.FrameUpdate{Frame: adapter.PanelUpsert{Header: next(m), Panel: p}})

		// Expand m1 (10 lines, Truncated=false): all 10 lines shown, NO trailing ellipsis line.
		m = press(t, m, "g", "space")
		f := m.Frame()
		if !contains(f, "paragraph 01") || !contains(f, "paragraph 10") {
			t.Fatalf("m1 excerpt truncated unexpectedly:\n%s", plain(f))
		}
		for _, l := range lines(f) {
			if strings.TrimSpace(strings.Trim(l, "│")) == "…" {
				t.Fatalf("unexpected ellipsis for non-truncated excerpt:\n%s", plain(f))
			}
		}

		// Expand m2 (Truncated=true): trailing ellipsis line IS shown.
		m = press(t, m, "j", "space")
		foundEllipsis := false
		for _, l := range lines(m.Frame()) {
			if strings.TrimSpace(strings.Trim(l, "│")) == "…" {
				foundEllipsis = true
			}
		}
		if !foundEllipsis {
			t.Fatalf("expected ellipsis for adapter-truncated excerpt:\n%s", plain(m.Frame()))
		}
	})

	t.Run("tall expanded mail scrolls line-by-line before advancing row", func(t *testing.T) {
		// 80x12 => innerH=10, contentHeight=7, mail list viewport=6 lines.
		const w, h = 80, 12
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "mail"})
		p := *m.st.Panel
		v := *p.View
		var body strings.Builder
		for i := 1; i <= 10; i++ {
			fmt.Fprintf(&body, "mail-line-%02d\n", i)
		}
		v.ControlPlane = []adapter.ViewMessage{
			{Ref: "2026-11-16T09-00-00.000Z_m1", Body: body.String(), Truncated: false},
		}
		p.View = &v
		m = step(t, m, adapter.FrameUpdate{Frame: adapter.PanelUpsert{Header: next(m), Panel: p}})

		// Row 0 expanded: 1 header + 10 body lines = 11 lines; viewport = 6 => maxOff = 5.
		m = press(t, m, "g", "space")
		if m.sel[TabMail] != 0 || m.scroll[TabMail] != 0 {
			t.Fatalf("initial expand: sel=%d scroll=%d", m.sel[TabMail], m.scroll[TabMail])
		}
		if !contains(m.Frame(), "build it") || !contains(m.Frame(), "mail-line-05") || contains(m.Frame(), "mail-line-06") {
			t.Fatalf("top of tall mail:\n%s", plain(m.Frame()))
		}

		// Step down 5 lines within row 0.
		for stepIdx := 1; stepIdx <= 5; stepIdx++ {
			m = press(t, m, "j")
			if m.sel[TabMail] != 0 || m.scroll[TabMail] != stepIdx {
				t.Fatalf("step %d: sel=%d scroll=%d, want sel=0 scroll=%d", stepIdx, m.sel[TabMail], m.scroll[TabMail], stepIdx)
			}
			if bad := holes(m.Frame(), w, h); bad != nil {
				t.Fatalf("step %d holes: %v", stepIdx, bad)
			}
		}
		if !contains(m.Frame(), "mail-line-10") || contains(m.Frame(), "build it") {
			t.Fatalf("bottom of tall mail:\n%s", plain(m.Frame()))
		}

		// Next j advances to row 1 at scroll 0.
		m = press(t, m, "j")
		if m.sel[TabMail] != 1 || m.scroll[TabMail] != 0 {
			t.Fatalf("advance to row 1: sel=%d scroll=%d", m.sel[TabMail], m.scroll[TabMail])
		}
		// Short row 1 -> row 2 takes a single j.
		m = press(t, m, "j")
		if m.sel[TabMail] != 2 || m.scroll[TabMail] != 0 {
			t.Fatalf("short row advance: sel=%d scroll=%d", m.sel[TabMail], m.scroll[TabMail])
		}
		// k back to row 1 (short), then k into row 0 lands at bottom of row 0 (scroll=5).
		m = press(t, m, "k")
		if m.sel[TabMail] != 1 || m.scroll[TabMail] != 0 {
			t.Fatalf("back to row 1: sel=%d scroll=%d", m.sel[TabMail], m.scroll[TabMail])
		}
		m = press(t, m, "k")
		if m.sel[TabMail] != 0 || m.scroll[TabMail] != 5 || !contains(m.Frame(), "mail-line-10") {
			t.Fatalf("enter tall row 0 from below: sel=%d scroll=%d\n%s", m.sel[TabMail], m.scroll[TabMail], plain(m.Frame()))
		}
		// k steps up line-by-line back to top of row 0.
		for stepIdx := 4; stepIdx >= 0; stepIdx-- {
			m = press(t, m, "k")
			if m.sel[TabMail] != 0 || m.scroll[TabMail] != stepIdx {
				t.Fatalf("up step %d: sel=%d scroll=%d", stepIdx, m.sel[TabMail], m.scroll[TabMail])
			}
		}
		if !contains(m.Frame(), "build it") {
			t.Fatalf("back at top of row 0:\n%s", plain(m.Frame()))
		}
	})

	t.Run("tall overview narrative and hand-back scroll line-by-line with j/k/g/G", func(t *testing.T) {
		// 80x12 => innerH=10, contentHeight=7.
		const w, h = 80, 12
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "overview"})
		p := *m.st.Panel
		v := *p.View
		var nar, hb strings.Builder
		for i := 1; i <= 10; i++ {
			fmt.Fprintf(&nar, "nar-line-%02d\n", i)
			fmt.Fprintf(&hb, "hb-line-%02d\n", i)
		}
		v.Narrative = &adapter.ViewNarrative{Text: nar.String(), At: m.now()}
		v.HandBack = &adapter.HandBack{By: "claude-01", Seq: 2, At: m.now(), Body: hb.String()}
		p.View = &v
		m = step(t, m, adapter.FrameUpdate{Frame: adapter.PanelUpsert{Header: next(m), Panel: p}})

		// Move past the 5 alerts to the narrative row (row 5).
		for range 5 {
			m = press(t, m, "j")
		}
		if m.sel[TabOverview] != 5 || !contains(m.Frame(), "nar-line-01") || contains(m.Frame(), "nar-line-10") {
			t.Fatalf("top of narrative: sel=%d\n%s", m.sel[TabOverview], plain(m.Frame()))
		}
		// Scroll down within narrative until nar-line-10 is visible, without leaving row 5.
		for !contains(m.Frame(), "nar-line-10") {
			m = press(t, m, "j")
			if m.sel[TabOverview] != 5 {
				t.Fatalf("left narrative row 5 before showing nar-line-10: sel=%d", m.sel[TabOverview])
			}
		}

		// G jumps to the last row (hand-back) with its bottom line visible.
		m = press(t, m, "G")
		if m.sel[TabOverview] != m.rowCount(TabOverview)-1 || !contains(m.Frame(), "hb-line-10") {
			t.Fatalf("G on tall hand-back: sel=%d scroll=%d\n%s", m.sel[TabOverview], m.scroll[TabOverview], plain(m.Frame()))
		}
		if bad := holes(m.Frame(), w, h); bad != nil {
			t.Fatalf("G holes: %v", bad)
		}

		// g resets to row 0 top.
		m = press(t, m, "g")
		if m.sel[TabOverview] != 0 || m.scroll[TabOverview] != 0 || !contains(m.Frame(), "SYSTEM ALERTS") {
			t.Fatalf("g reset: sel=%d scroll=%d\n%s", m.sel[TabOverview], m.scroll[TabOverview], plain(m.Frame()))
		}
	})
}

func TestMailOpenAndParticipantCycle(t *testing.T) {
	w, h := refSize(t, layer.Drawer)
	type ran struct {
		env  []string
		name string
		args []string
	}

	t.Run("o on Mail writes read-only markdown with metadata and authorized excerpt, opens md-viewer, minimizes", func(t *testing.T) {
		dir := t.TempDir()
		rec := &recorder{}
		var runs []ran
		d := Deps{Layer: rec, CacheDir: dir,
			LookPath: func(string) (string, error) { return "/opt/bin/md-viewer", nil },
			Exec: func(_ context.Context, env []string, name string, args ...string) error {
				runs = append(runs, ran{env, name, args})
				return nil
			}}
		m := build(t, "edited-crew-attention", w, h, d, Options{Tab: "mail"})
		// Select m1 which has an authorized excerpt (Kind=todo, Priority=normal, Body="Wire the token refresh.").
		m = press(t, m, "g", "o")
		path := filepath.Join(dir, "rex-amq-ui", "session_s-mail-2026-11-16T09-00-00.000Z_m1.md")
		if len(runs) != 1 || runs[0].name != "/opt/bin/md-viewer" || !reflect.DeepEqual(runs[0].args, []string{"open", path}) ||
			!reflect.DeepEqual(runs[0].env, []string{"REX_BLOCK=" + focusedPane}) {
			t.Fatalf("runs %+v", runs)
		}
		b, err := os.ReadFile(path)
		fi, _ := os.Stat(path)
		if err != nil || fi.Mode().Perm() != 0o444 {
			t.Fatalf("file err=%v mode=%v", err, fi.Mode())
		}
		got := string(b)
		for _, want := range []string{
			"# build it",
			"- **From:** `claude-01`",
			"- **To:** `claude-02`",
			"- **ID:** `2026-11-16T09-00-00.000Z_m1`",
			"- **Thread:** `p2p/claude-01__claude-02`",
			"- **State:** `drained`",
			"- **Created:** `2026-11-16T09:00:00Z`",
			"- **Kind:** `todo`",
			"- **Priority:** `normal`",
			"Wire the token refresh.",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("mail markdown missing %q:\n%s", want, got)
			}
		}
		if !reflect.DeepEqual(rec.got(), []string{"minimize"}) || !m.Paused() {
			t.Fatalf("calls %v paused=%v", rec.got(), m.Paused())
		}
		// Reopening replaces the read-only file cleanly.
		m = step(t, m, apc.Command{Verb: apc.Resume})
		m = press(t, m, "o")
		if len(runs) != 2 || m.hint != "" {
			t.Fatalf("reopen mail: runs=%d hint=%q", len(runs), m.hint)
		}
	})

	t.Run("o on Mail without authorized excerpt writes explicit notice and never reads AMQ files", func(t *testing.T) {
		dir := t.TempDir()
		rec := &recorder{}
		var openedPath string
		d := Deps{Layer: rec, CacheDir: dir,
			LookPath: func(string) (string, error) { return "/opt/bin/md-viewer", nil },
			Exec: func(_ context.Context, _ []string, _ string, args ...string) error {
				openedPath = args[1]
				return nil
			}}
		m := build(t, "edited-crew-attention", w, h, d, Options{Tab: "mail"})
		// m3 (bounced, no authorized excerpt) is selected by default.
		m = press(t, m, "o")
		b, err := os.ReadFile(openedPath)
		if err != nil {
			t.Fatal(err)
		}
		got := string(b)
		if !strings.Contains(got, "# review s1") || !strings.Contains(got, "bounced") ||
			!strings.Contains(got, "No body excerpt was authorized in the session view.") {
			t.Fatalf("unexpected unauthorized mail markdown:\n%s", got)
		}
	})

	t.Run("o on Mail falls back to $EDITOR via OpenBeside, hints when empty or no viewer, and does not fire while typing search", func(t *testing.T) {
		rec := &recorder{}
		d := Deps{Layer: rec, CacheDir: t.TempDir(), Editor: "nvim -R",
			LookPath: func(string) (string, error) { return "", errors.New("missing") }}
		m := build(t, "edited-crew-attention", w, h, d, Options{Tab: "mail"})
		// Typing 'o' inside / search adds to query instead of opening.
		m = press(t, m, "/", "o", "enter")
		if len(rec.got()) != 0 || m.mail.query != "o" {
			t.Fatalf("typing o in search triggered open: calls=%v query=%q", rec.got(), m.mail.query)
		}
		m = press(t, m, "esc", "o")
		got := rec.got()
		if len(got) != 2 || !strings.HasPrefix(got[0], "beside "+focusedPane+" mail [nvim -R ") || got[1] != "minimize" {
			t.Fatalf("editor fallback calls: %v", got)
		}

		// No messages selected -> hint.
		empty := build(t, "recorded-bare", w, h, Deps{CacheDir: t.TempDir()}, Options{Tab: "mail"})
		empty = press(t, empty, "o")
		if !contains(empty.Frame(), "no message selected") {
			t.Fatalf("empty mail open:\n%s", plain(empty.Frame()))
		}
	})

	t.Run("p skips duplicate/empty To and resets cleanly on move or esc", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "mail"})
		// Add a self-addressed message m5 and a multi-recipient message m6.
		m5 := adapter.Message{ID: "2026-11-16T09-05-00.000Z_m5", From: "pi-04", To: []string{"pi-04"}, Subject: "self note", State: adapter.MsgDrained, CreatedAt: m.now().Add(-time.Minute)}
		m6 := adapter.Message{ID: "2026-11-16T09-06-00.000Z_m6", From: "claude-01", To: []string{"claude-02", "pi-04"}, Subject: "multi", State: adapter.MsgQueued, CreatedAt: m.now()}
		m = step(t, m, adapter.FrameUpdate{Frame: adapter.MessageUpsert{Header: next(m), Message: m5}})
		m = step(t, m, adapter.FrameUpdate{Frame: adapter.MessageUpsert{Header: next(m), Message: m6}})

		// Search matches secondary recipient pi-04 on m6.
		m = press(t, m, "/", "p", "i", "-", "0", "4", "enter")
		if !contains(m.Frame(), "multi") || !contains(m.Frame(), "self note") || contains(m.Frame(), "build it") {
			t.Fatalf("search secondary To:\n%s", plain(m.Frame()))
		}
		m = press(t, m, "esc")

		// Move to m5 (self-addressed pi-04 -> pi-04): p sets pi-04, second p clears immediately (skips duplicate To).
		m = press(t, m, "G", "k")
		if cur, _ := m.selectedMail(); cur.ID != m5.ID {
			t.Fatalf("expected m5 selected, got %+v", cur)
		}
		m = press(t, m, "p")
		if m.mail.from != "pi-04" {
			t.Fatalf("1st p on self-addressed: from=%q", m.mail.from)
		}
		m = press(t, m, "p")
		if m.mail.from != "" {
			t.Fatalf("2nd p on self-addressed should clear, got from=%q", m.mail.from)
		}

		// Move selection during active filter resets cycle cleanly and never no-ops.
		m = press(t, m, "g", "p") // on m1 (claude-01 -> claude-02), sets from=claude-01
		if m.mail.from != "claude-01" {
			t.Fatalf("m1 1st p: from=%q", m.mail.from)
		}
		m = press(t, m, "j") // move to m2 (claude-02 -> claude-01); cycle resets
		if m.mail.cycleID != "" {
			t.Fatalf("cycleID not cleared on move: %q", m.mail.cycleID)
		}
		m = press(t, m, "p") // starts m2's cycle at From=claude-02
		if m.mail.from != "claude-02" {
			t.Fatalf("m2 fresh cycle should filter by claude-02, got %q", m.mail.from)
		}
		m = press(t, m, "esc") // clears filter and resets cycle
		if m.mail.from != "" || m.mail.cycleID != "" {
			t.Fatalf("esc did not reset filter/cycle: from=%q cycle=%q", m.mail.from, m.mail.cycleID)
		}
	})
}

func TestDrawerCompactBadgesAndDynamicWidths(t *testing.T) {
	t.Run("compact model/effort badges in Drawer", func(t *testing.T) {
		cases := []struct {
			model, effort, want string
		}{
			{"gemini-4-neon-high", "high", "neon/hi"},
			{"gemini-4-carbon-high", "high", "carbon/hi"},
			{"claude-opus-5-5", "high", "opus/hi"},
			{"claude-sonnet-4-6", "medium", "sonnet/med"},
			{"claude-haiku-4-5-20251001", "low", "haiku/lo"},
			{"auto/gpt-6.1-sol", "high", "sol/hi"},
			{"openai/gpt-5.4-codex", "xhigh", "codex/xhi"},
			{"gpt-5", "high", "gpt-5/hi"},
			{"gpt-4o", "medium", "gpt-4o/med"},
			{"opus", "high", "opus/hi"},
			{"sonnet", "", "sonnet"},
			{"", "", "—"},
		}
		for _, tc := range cases {
			got := memberModelText(adapter.Agent{Model: tc.model, Effort: tc.effort}, false)
			if got != tc.want {
				t.Errorf("memberModelText(%q, %q, drawer) = %q, want %q", tc.model, tc.effort, got, tc.want)
			}
		}
		// Board keeps the full model/effort.
		if got := memberModelText(adapter.Agent{Model: "gemini-4-neon-high", Effort: "high"}, true); got != "gemini-4-neon-high/high" {
			t.Errorf("board model = %q", got)
		}
	})

	t.Run("dynamic member column widths give Task more room in Drawer", func(t *testing.T) {
		w, h := refSize(t, layer.Drawer)
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "members"})
		// Replace agents with short handles and verbose model IDs + long task descriptions.
		a1 := adapter.Agent{
			Handle: "agy-72", Role: "lead", Model: "gemini-4-neon-high", Effort: "high",
			Task:      "implement responsive drawer column widths",
			Lifecycle: adapter.Lifecycle{State: adapter.LifeLive},
			Activity:  adapter.Activity{State: "working"},
		}
		a2 := adapter.Agent{
			Handle: "codex-37", Role: "rev", Model: "auto/gpt-6.1-sol", Effort: "xhigh",
			Task:      "review slice s3 mail and overview layout",
			Lifecycle: adapter.Lifecycle{State: adapter.LifeLive},
			Activity:  adapter.Activity{State: "working"},
		}
		snap := adapter.Snapshot{
			Header: adapter.Header{Schema: 1, Stream: "s-compact", Seq: 1},
			Scope:  m.st.Scope,
			Agents: []adapter.Agent{a1, a2},
			Panel:  m.st.Panel,
		}
		m = step(t, m, adapter.FrameUpdate{Frame: snap})
		f := m.Frame()
		if bad := holes(f, w, h); bad != nil {
			t.Fatalf("drawer holes: %v", bad)
		}
		for _, want := range []string{
			"neon/hi",
			"sol/xhi",
			"implement responsive drawer column widths",
			"review slice s3 mail and overview layout",
		} {
			if !contains(f, want) {
				t.Fatalf("drawer members missing %q:\n%s", want, plain(f))
			}
		}
		// Narrow drawer (55 cols) still preserves task text and paints with zero holes.
		m = step(t, m, tea.WindowSizeMsg{Width: 55, Height: 20})
		if bad := holes(m.Frame(), 55, 20); bad != nil {
			t.Fatalf("narrow drawer holes: %v", bad)
		}
		if !contains(m.Frame(), "neon/hi") || !contains(m.Frame(), "implement responsive") {
			t.Fatalf("narrow drawer lost task:\n%s", plain(m.Frame()))
		}
	})

	t.Run("long Overview alert reasons wrap cleanly in Drawer", func(t *testing.T) {
		w, h := refSize(t, layer.Drawer)
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "overview"})
		longSubject := "S3 uncapped Mail/Overview scrolling, o open in md-viewer, p participant filter, and responsive Drawer widths"
		longMsg := adapter.Message{
			ID:        "2026-11-16T09-07-00.000Z_m_long",
			From:      "claude-01",
			To:        []string{"codex-03"},
			Subject:   longSubject,
			State:     adapter.MsgQueued,
			Bounced:   true,
			CreatedAt: m.now().Add(-2 * time.Minute),
		}
		m = step(t, m, adapter.FrameUpdate{Frame: adapter.MessageUpsert{Header: next(m), Message: longMsg}})
		f := m.Frame()
		if bad := holes(f, w, h); bad != nil {
			t.Fatalf("wrapped alert holes: %v", bad)
		}
		// Both the start and end of the long bounced subject appear without truncation.
		if !contains(f, "S3 uncapped Mail/Overview") || !contains(f, "responsive Drawer widths") {
			t.Fatalf("long alert reason did not wrap cleanly:\n%s", plain(f))
		}
		// Selecting the wrapped alert highlights all its continuation lines on Overlay.
		for range 4 {
			m = press(t, m, "j")
		}
		overlay := sgrBg(m.pal.Overlay)
		var highlighted int
		for _, l := range strings.Split(m.Frame(), "\n") {
			p := plain(l)
			if strings.Contains(p, "S3 uncapped") || strings.Contains(p, "responsive Drawer widths") {
				if !strings.Contains(l, overlay) {
					t.Errorf("wrapped alert line missing overlay bg: %q", p)
				}
				highlighted++
			}
		}
		if highlighted < 2 {
			t.Fatalf("expected wrapped alert to span >=2 highlighted lines, got %d\n%s", highlighted, plain(m.Frame()))
		}
	})
}
