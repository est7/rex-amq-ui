package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/est7/rex-amq-ui/internal/apc"
	"github.com/est7/rex-amq-ui/internal/layer"
)

func sendMsg(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, cmd := m.Update(msg)
	return drain(t, next.(Model), cmd)
}

func leftClick(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

func wheel(btn tea.MouseButton) tea.MouseWheelMsg {
	return tea.MouseWheelMsg{X: 10, Y: 10, Button: btn}
}

func TestDirectTabKeys(t *testing.T) {
	w, h := refSize(t, layer.Drawer)

	t.Run("alt+1..5 and f1..f5 jump directly in personal and crew sessions", func(t *testing.T) {
		m := build(t, "edited-personal-cockpit", w, h, Deps{}, Options{})
		if m.tab != TabEpics {
			t.Fatalf("start tab = %v, want TabEpics", m.tab)
		}
		wantPersonal := []Tab{TabEpics, TabOverview, TabMembers, TabMail, TabGraph}
		fKeys := []rune{tea.KeyF1, tea.KeyF2, tea.KeyF3, tea.KeyF4, tea.KeyF5}
		for i, want := range wantPersonal {
			m = step(t, m, tea.KeyPressMsg{Code: rune('1' + i), Mod: tea.ModAlt})
			if m.tab != want {
				t.Fatalf("alt+%d went to %v, want %v", i+1, m.tab, want)
			}
		}
		for i := len(wantPersonal) - 1; i >= 0; i-- {
			m = step(t, m, tea.KeyPressMsg{Code: fKeys[i]})
			if m.tab != wantPersonal[i] {
				t.Fatalf("f%d went to %v, want %v", i+1, m.tab, wantPersonal[i])
			}
		}

		// Crew session has 4 tabs; alt+5 / f5 are no-ops.
		crew := build(t, "edited-crew-attention", w, h, Deps{}, Options{})
		crew = step(t, crew, tea.KeyPressMsg{Code: '4', Mod: tea.ModAlt})
		if crew.tab != TabGraph {
			t.Fatalf("crew alt+4 = %v, want TabGraph", crew.tab)
		}
		crew = step(t, crew, tea.KeyPressMsg{Code: '5', Mod: tea.ModAlt})
		if crew.tab != TabGraph {
			t.Fatalf("crew alt+5 changed tab to %v", crew.tab)
		}
		crew = step(t, crew, tea.KeyPressMsg{Code: tea.KeyF2})
		if crew.tab != TabMembers {
			t.Fatalf("crew f2 = %v, want TabMembers", crew.tab)
		}
	})

	t.Run("brackets and H/L switch tabs while preserving Graph replay keys", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "overview"})
		m = press(t, m, "]")
		if m.tab != TabMembers {
			t.Fatalf("] from Overview went to %v, want TabMembers", m.tab)
		}
		m = press(t, m, "[")
		if m.tab != TabOverview {
			t.Fatalf("[ from Members went to %v, want TabOverview", m.tab)
		}
		m = press(t, m, "H")
		if m.tab != TabGraph {
			t.Fatalf("H from Overview wrapped to %v, want TabGraph", m.tab)
		}
		// On Graph, [ and ] stay replay step keys rather than switching tabs.
		m = press(t, m, "[")
		if m.tab != TabGraph || m.graphAt == "" {
			t.Fatalf("[ on Graph: tab=%v graphAt=%q, want replay on TabGraph", m.tab, m.graphAt)
		}
		// L while replaying returns to Live without leaving Graph.
		m = press(t, m, "L")
		if m.tab != TabGraph || m.graphAt != "" {
			t.Fatalf("L while replaying: tab=%v graphAt=%q, want Live on TabGraph", m.tab, m.graphAt)
		}
		// L when already Live switches to the next tab.
		m = press(t, m, "L")
		if m.tab != TabOverview {
			t.Fatalf("L from Live Graph went to %v, want TabOverview", m.tab)
		}
		// H on Graph switches to previous tab (Mail).
		m = step(t, m, mustTab("graph"))
		m = press(t, m, "H")
		if m.tab != TabMail {
			t.Fatalf("H on Graph went to %v, want TabMail", m.tab)
		}
	})

	t.Run("Mail search typing captures brackets and H/L instead of switching tabs", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "mail"})
		m = press(t, m, "/", "[", "]", "H", "L")
		if m.tab != TabMail || !m.mail.typing || m.mail.query != "[]HL" {
			t.Fatalf("tab=%v typing=%v query=%q", m.tab, m.mail.typing, m.mail.query)
		}
	})
}

func TestMouseTabBarAndWheel(t *testing.T) {
	w, h := refSize(t, layer.Drawer)

	t.Run("clicking tab pills switches tabs directly", func(t *testing.T) {
		m := build(t, "edited-personal-cockpit", w, h, Deps{}, Options{})
		pills := m.tabPills()
		if len(pills) != 5 {
			t.Fatalf("got %d pills, want 5", len(pills))
		}
		tabY := borderWidth + 1
		for _, p := range pills {
			m = sendMsg(t, m, leftClick((p.start+p.end)/2, tabY))
			if m.tab != p.tab {
				t.Fatalf("click pill %v at x=%d landed on %v", p.tab, (p.start+p.end)/2, m.tab)
			}
		}
		// Clicking gap between pills leaves current tab intact.
		cur := m.tab
		m = sendMsg(t, m, leftClick(pills[0].end, tabY))
		if m.tab != cur {
			t.Fatalf("click on gap changed tab from %v to %v", cur, m.tab)
		}
	})

	t.Run("wheel scrolls lists and unfollows Mail", func(t *testing.T) {
		m := build(t, "edited-crew-attention", w, h, Deps{}, Options{Tab: "members"})
		m = sendMsg(t, m, wheel(tea.MouseWheelDown))
		if m.sel[TabMembers] != 1 {
			t.Fatalf("wheel down on Members: sel=%d, want 1", m.sel[TabMembers])
		}
		m = sendMsg(t, m, wheel(tea.MouseWheelUp))
		if m.sel[TabMembers] != 0 {
			t.Fatalf("wheel up on Members: sel=%d, want 0", m.sel[TabMembers])
		}
		m = step(t, m, mustTab("mail"))
		if !m.mail.follow || m.sel[TabMail] != 2 {
			t.Fatalf("start Mail: follow=%v sel=%d", m.mail.follow, m.sel[TabMail])
		}
		m = sendMsg(t, m, wheel(tea.MouseWheelUp))
		if m.mail.follow || m.sel[TabMail] != 1 {
			t.Fatalf("wheel up on Mail: follow=%v sel=%d, want false, 1", m.mail.follow, m.sel[TabMail])
		}
	})
}

func TestMouseListClickAndDoubleClick(t *testing.T) {
	w, h := refSize(t, layer.Drawer)

	t.Run("Epics click selects and double-click within 450ms drills down", func(t *testing.T) {
		now := time.Time(fixtures["edited-personal-cockpit"])
		m := build(t, "edited-personal-cockpit", w, h, Deps{Clock: movingClock{&now}}, Options{})
		// Find the screen Y of Epic row 1 (second epic).
		es := m.epicsEntries()
		first1, _ := rowBounds(es, 1)
		y1 := borderWidth + headerLines + first1

		m = sendMsg(t, m, leftClick(5, y1))
		if m.sel[TabEpics] != 1 || m.DrilledDown() {
			t.Fatalf("first click: sel=%d drilled=%v", m.sel[TabEpics], m.DrilledDown())
		}
		// After >450ms, a second click is NOT a double-click.
		now = now.Add(500 * time.Millisecond)
		m = sendMsg(t, m, leftClick(5, y1))
		if m.DrilledDown() {
			t.Fatal("click after 500ms triggered double-click drill-down")
		}
		// Within 450ms, a second click triggers drillDown().
		now = now.Add(200 * time.Millisecond)
		m = sendMsg(t, m, leftClick(5, y1))
		if !m.DrilledDown() || m.drill.epic.SessionID != "session:t" {
			t.Fatalf("double-click did not drill into epic 1: drilled=%v drill=%+v", m.DrilledDown(), m.drill)
		}
	})

	t.Run("Members ignores group headers and double-click focuses pane", func(t *testing.T) {
		now := time.Time(fixtures["edited-crew-attention"])
		rec := &recorder{}
		m := build(t, "edited-crew-attention", w, h, Deps{Clock: movingClock{&now}, Layer: rec}, Options{Tab: "members"})
		es := m.memberEntries()
		// Line 0 of memberEntries is the "Lead" header (row == -1).
		if es[0].row >= 0 {
			t.Fatalf("expected header at entry 0, got row %d", es[0].row)
		}
		m = sendMsg(t, m, leftClick(5, borderWidth+headerLines))
		if len(rec.got()) != 0 {
			t.Fatalf("click on header triggered action: %v", rec.got())
		}
		// Find member row 1 (claude-02).
		first1, _ := rowBounds(es, 1)
		y1 := borderWidth + headerLines + first1
		m = sendMsg(t, m, leftClick(5, y1))
		if m.sel[TabMembers] != 1 || len(rec.got()) != 0 {
			t.Fatalf("first click on member 1: sel=%d calls=%v", m.sel[TabMembers], rec.got())
		}
		now = now.Add(150 * time.Millisecond)
		m = sendMsg(t, m, leftClick(5, y1))
		if got := strings.Join(rec.got(), ","); !strings.Contains(got, "jump block:claude-02") {
			t.Fatalf("double-click member 1 calls=%v, want jump block:claude-02", rec.got())
		}
	})

	t.Run("Overview double-click focuses alert and opens Hand-back card", func(t *testing.T) {
		now := time.Time(fixtures["edited-crew-attention"])
		rec := &recorder{}
		var opened []string
		m := build(t, "edited-crew-attention", w, h, Deps{
			Clock:    movingClock{&now},
			Layer:    rec,
			CacheDir: t.TempDir(),
			LookPath: func(string) (string, error) { return "/usr/local/bin/md-viewer", nil },
			Exec: func(_ context.Context, _ []string, name string, args ...string) error {
				opened = append(opened, name+" "+strings.Join(args, " "))
				return nil
			},
		}, Options{Tab: "overview"})

		// Double-click alert 1 (claude-02 turn error).
		es := m.overviewEntries()
		first1, _ := rowBounds(es, 1)
		y1 := borderWidth + headerLines + first1
		m = sendMsg(t, m, leftClick(5, y1))
		now = now.Add(100 * time.Millisecond)
		m = sendMsg(t, m, leftClick(5, y1))
		if got := strings.Join(rec.got(), ","); !strings.Contains(got, "jump block:claude-02") {
			t.Fatalf("double-click alert 1: calls=%v", rec.got())
		}

		// Resume and double-click the Hand-back card row.
		m = step(t, m, apc.Command{Verb: apc.Resume})
		hbRow, ok := m.overviewHandBackRow()
		if !ok {
			t.Fatal("fixture missing hand-back row")
		}
		es = m.overviewEntries()
		firstHB, _ := rowBounds(es, hbRow)
		start := listStart(es, m.sel[TabOverview], m.scroll[TabOverview], m.contentHeight())
		yHB := borderWidth + headerLines + (firstHB - start)
		m = sendMsg(t, m, leftClick(5, yHB))
		if m.sel[TabOverview] != hbRow || len(opened) != 0 {
			t.Fatalf("first click on hand-back: sel=%d want %d, opened=%v", m.sel[TabOverview], hbRow, opened)
		}
		// Recompute yHB after selection moved (listStart may re-center).
		start = listStart(es, m.sel[TabOverview], m.scroll[TabOverview], m.contentHeight())
		yHB = borderWidth + headerLines + (firstHB - start)
		now = now.Add(100 * time.Millisecond)
		m = sendMsg(t, m, leftClick(5, yHB))
		if len(opened) != 1 || !strings.Contains(opened[0], "hand-back-1.md") {
			t.Fatalf("double-click hand-back opened=%v", opened)
		}
	})

	t.Run("Mail click unfollows when moving up and double-click toggles excerpt", func(t *testing.T) {
		now := time.Time(fixtures["edited-crew-attention"])
		m := build(t, "edited-crew-attention", w, h, Deps{Clock: movingClock{&now}}, Options{Tab: "mail"})
		// Row 0 (m1) has an authorized excerpt in edited-crew-attention.
		es := m.mailEntries()
		first0, _ := rowBounds(es, 0)
		y0 := borderWidth + headerLines + 1 + first0
		m = sendMsg(t, m, leftClick(5, y0))
		if m.sel[TabMail] != 0 || m.mail.follow {
			t.Fatalf("click row 0: sel=%d follow=%v, want 0 false", m.sel[TabMail], m.mail.follow)
		}
		now = now.Add(150 * time.Millisecond)
		m = sendMsg(t, m, leftClick(5, y0))
		if !m.mail.expanded["2026-11-16T09-00-00.000Z_m1"] {
			t.Fatal("double-click row 0 did not expand excerpt")
		}
	})
}

func TestMouseModeTransitionsAndKittyPixelTabClick(t *testing.T) {
	ctl := &cellRecorder{w: cw, h: ch}
	r := panelOn(t, "edited-crew-attention", ctl, newTerm())
	if got := r.m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("Overview MouseMode = %v, want MouseModeCellMotion", got)
	}
	// Pause records frozenMouse so View() stays unchanged while paused.
	r.deliver(apc.Command{Verb: apc.Pause})
	if got := r.m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("paused Overview MouseMode = %v, want MouseModeCellMotion", got)
	}
	r.deliver(apc.Command{Verb: apc.Resume})

	// Enter Kitty Graph: MouseMode becomes MouseModeNone so Bubble Tea never
	// overrides pixelmouse.Enable (?1002h ?1006h ?1016h).
	r.send(mustTab("graph"), 6)
	if !r.term.showing() {
		t.Fatalf("Kitty graph did not draw: %s", r.term)
	}
	if got := r.m.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("Kitty Graph MouseMode = %v, want MouseModeNone", got)
	}

	// Wheel up on Kitty Graph steps replay back.
	r.send(wheel(tea.MouseWheelUp), 6)
	if r.m.graphAt == "" {
		t.Fatal("wheel up on Kitty Graph did not step replay back")
	}

	// Pixel-coordinate click on the Mail tab pill (outside GraphRegion)
	// switches to Mail, clears Kitty image + pixel mouse, and re-arms
	// Bubble Tea MouseModeCellMotion!
	var mailPill tabPill
	for _, p := range r.m.tabPills() {
		if p.tab == TabMail {
			mailPill = p
		}
	}
	px := ((mailPill.start + mailPill.end) / 2) * cw
	py := (borderWidth + 1) * ch
	out := r.send(leftClick(px, py), 6)
	if r.m.tab != TabMail {
		t.Fatalf("pixel tab bar click landed on %v, want TabMail", r.m.tab)
	}
	if out != clearOff || !r.term.blank() {
		t.Fatalf("leaving Kitty via tab click wrote %q (%s), want ClearAll+pixelmouse.Disable", out, r.term)
	}
	if got := r.m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("after leaving Kitty Graph MouseMode = %v, want MouseModeCellMotion", got)
	}
}
