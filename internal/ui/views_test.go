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
	sizes := [][2]int{{dw, dh}, {bw, bh}, {0, 0}, {0, 5}, {5, 0}, {1, 1}, {3, 2}, {8, 3}, {20, 6}, {33, 9}, {48, 14}, {200, 50}, {300, 70}}
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
			t.Fatalf("header: %q", lines(f)[0])
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
		if !strings.Contains(ls[3], "is waiting on you") {
			t.Errorf("first alert %q: waiting on the human goes first", ls[3])
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
	t.Run("a long hand-back pulls Blocked on me up", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{})
		p := *m.st.Panel
		v := *p.View
		v.HandBack = &adapter.HandBack{By: "claude-01", Seq: 2, At: m.now(), Body: strings.Repeat("line\n", 12) + "## Blocked on me\n- sign the release"}
		p.View = &v
		m = step(t, m, adapter.FrameUpdate{Frame: adapter.PanelUpsert{Header: adapter.Header{Stream: m.st.Stream, Seq: m.st.Seq + 1}, Panel: p}})
		f := m.Frame()
		if !contains(f, "… ## Blocked on me") || !contains(f, "- sign the release") || !contains(f, "more lines") {
			t.Fatalf("\n%s", plain(f))
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
	t.Run("mail search and member filter", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "mail"})
		m = press(t, m, "/", "S", "C", "O", "enter")
		if f := m.Frame(); !contains(f, "question about scope") || contains(f, "build it") {
			t.Fatalf("search:\n%s", plain(f))
		}
		m = press(t, m, "esc") // clears the search
		m = step(t, m, apc.Command{Verb: apc.Tab, Tab: "members"})
		m = press(t, m, "1")
		m = step(t, m, apc.Command{Verb: apc.Tab, Tab: "mail"})
		m = press(t, m, "p")
		if f := m.Frame(); !contains(f, "from/to claude-01") || !contains(f, "review s1") || !contains(f, "build it") {
			t.Fatalf("filter by claude-01:\n%s", plain(f))
		}
	})
	t.Run("epics: first and default in a personal session", func(t *testing.T) {
		m := build(t, "edited-personal-cockpit", w, h, Deps{}, Options{})
		ls := lines(m.Frame())
		if !strings.HasPrefix(strings.TrimSpace(ls[1]), "Epics 2") || m.tab != TabEpics {
			t.Fatalf("tab bar %q, tab %v", ls[1], m.tab)
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
		if !strings.Contains(lines(m.Frame())[0], "◌ Reconnecting") {
			t.Fatalf("%q", lines(m.Frame())[0])
		}
		fresh := New(Deps{Clock: fixtures["recorded-bare"]}, Options{})
		fresh = step(t, fresh, tea.WindowSizeMsg{Width: w, Height: h})
		fresh = step(t, fresh, adapter.StatusUpdate{Status: adapter.Reconnecting, Err: errors.New("adapter: rex-amq-adapter not found")})
		if f := fresh.Frame(); !contains(f, "Waiting for rex-amq-adapter") || !contains(f, "not found") {
			t.Fatalf("\n%s", plain(f))
		}
	})
	t.Run("graph region and replay bar", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "graph"})
		r := m.GraphRegion()
		if r != (Rect{X: 0, Y: 3, W: w, H: h - 6}) {
			t.Fatalf("region %+v", r)
		}
		ls := lines(m.Frame())
		for y := r.Y; y < r.Y+r.H; y++ {
			if strings.TrimSpace(ls[y]) != "" {
				t.Fatalf("region row %d not blank: %q", y, ls[y])
			}
		}
		if !strings.Contains(ls[2], "3 / 3 messages · 4m00s") || !strings.Contains(ls[r.Y+r.H], "drained") {
			t.Fatalf("bar %q legend %q", ls[2], ls[r.Y+r.H])
		}
		m = press(t, m, "[")
		if !strings.Contains(lines(m.Frame())[2], "2 / 3") || !contains(m.Frame(), "question about scope") {
			t.Fatalf("replay step back:\n%s", plain(m.Frame()))
		}
		m = press(t, m, "h")
		if !strings.Contains(lines(m.Frame())[2], "/ 2 messages") {
			t.Fatalf("hide retired: %q", lines(m.Frame())[2])
		}
		m = press(t, m, "L")
		if m.graph != -1 {
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
		if m.size != layer.Board || !strings.Contains(lines(m.Frame())[0], "Board") {
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
	t.Run("enter on an epic switches; refusal leaves a hint and the panel up", func(t *testing.T) {
		rec := &recorder{fail: map[string]error{"switch": layer.ErrRemoteControlOff}}
		m := build(t, "edited-personal-cockpit", w, h, Deps{Layer: rec}, Options{})
		m = press(t, m, "2", "enter")
		if !reflect.DeepEqual(rec.got(), []string{"switch session:t"}) || m.Paused() {
			t.Fatalf("calls %v paused %v", rec.got(), m.Paused())
		}
		if foot := lines(m.Frame())[h-1]; !strings.Contains(foot, "Remote Control") {
			t.Fatalf("footer %q", foot)
		}
	})
	t.Run("Q quits even while paused; other keys wait", func(t *testing.T) {
		rec := &recorder{}
		m := build(t, "edited-crew-attention", w, h, Deps{Layer: rec}, Options{})
		m = step(t, m, apc.Command{Verb: apc.Pause})
		next, cmd := m.Update(key("z"))
		if cmd != nil || next.(Model).size != m.size {
			t.Fatal("a paused panel took z")
		}
		_, cmd = m.Update(key("Q"))
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
	t.Run("without Rex, actions say so", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{})
		m = press(t, m, "z")
		if !contains(m.Frame(), "not connected to Rex") {
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
	m := build(t, "recorded-bare", w, h, Deps{}, Options{})
	next, cmd := m.Update(layer.State{Connected: true})
	if cmd != nil {
		t.Fatal("asked for colours before an app owns the block")
	}
	m = next.(Model)
	m = step(t, m, apc.Command{Verb: apc.Pause})
	next, cmd = m.Update(layer.State{Connected: true, Owned: true})
	if cmd != nil {
		t.Fatal("asked for colours while paused")
	}
	next, cmd = next.(Model).Update(apc.Command{Verb: apc.Resume})
	if cmd == nil {
		t.Fatal("did not ask for colours on resume once owned")
	}
	m = next.(Model)
	if _, again := m.Update(layer.State{Connected: true, Owned: true, Focused: "b"}); again != nil {
		t.Fatal("asked twice")
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

	// Owned already when the panel attaches: it asks at once.
	fresh := build(t, "recorded-bare", w, h, Deps{}, Options{})
	if _, cmd := fresh.Update(layer.State{Connected: true, Owned: true}); cmd == nil {
		t.Fatal("did not ask when owned at attach")
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
}
