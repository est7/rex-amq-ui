package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/est7/rex-amq-ui/internal/apc"
	"github.com/est7/rex-amq-ui/internal/layer"
	"github.com/est7/rex-amq-ui/internal/rexctl"
	"github.com/est7/rex-amq-ui/internal/rexctl/rexctltest"
	"github.com/est7/rex-amq-ui/internal/ui"
)

// syncBuffer is the panel's terminal output.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) snapshot() (raw string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String(), s.b.Len()
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestPanelProgram runs the real panel (Bubble Tea program, APC input
// filter, adapter stream) on pipes against a fake adapter that sends its
// snapshot, then the rest of its stream once the test says so.
func TestPanelProgram(t *testing.T) {
	// The panel forces TrueColor: an environment that asks for no colour
	// must not strip its backgrounds.
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	dir := t.TempDir()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "panel", "edited-crew-attention.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	gate, done := filepath.Join(dir, "go"), filepath.Join(dir, "done")
	script := filepath.Join(dir, "rex-amq-adapter")
	body := "#!/bin/sh\nhead -n 1 '" + fixture + "'\nwhile [ ! -e '" + gate + "' ]; do sleep 0.02; done\n" +
		"tail -n +2 '" + fixture + "'\ntouch '" + done + "'\nexec sleep 60\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inW.Close()
	out := &syncBuffer{}
	errc := make(chan error, 1)
	progc := make(chan *tea.Program, 1)
	go func() {
		errc <- runPanel(context.Background(), panelConfig{
			Session: "session:s", AdapterBin: script, In: inR, Out: out, Width: 70, Height: 48,
			Options: ui.Options{}, started: func(p *tea.Program) { progc <- p },
		})
	}()
	prog := <-progc
	shows := func(s string) func() bool {
		return func() bool { raw, _ := out.snapshot(); return strings.Contains(ansi.Strip(raw), s) }
	}
	waitFor(t, "the snapshot on screen", shows("auth-refresh"))
	waitFor(t, "the claude-02 turn error alert", shows("hit a turn error"))

	inW.WriteString(apc.Encode(apc.Command{Verb: apc.Pause}))
	time.Sleep(300 * time.Millisecond) // the pause lands; a last frame may flush
	_, before := out.snapshot()
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the rest of the stream", func() bool { _, err := os.Stat(done); return err == nil })
	// Rex resizes a hidden layer's block when its window changes size.
	prog.Send(tea.WindowSizeMsg{Width: 80, Height: 40})
	time.Sleep(400 * time.Millisecond)
	if _, after := out.snapshot(); after != before {
		raw, _ := out.snapshot()
		t.Fatalf("the paused panel wrote %d bytes: %q", after-before, raw[before:])
	}
	if shows("is waiting on you")() {
		t.Fatal("frames reached the screen while paused")
	}

	inW.WriteString(apc.Encode(apc.Command{Verb: apc.Resume}))
	waitFor(t, "resume to draw the frames folded while paused", shows("is waiting on you"))

	// A sequence split across writes, then keys: tab:members, Q.
	inW.WriteString("\x1b_amqui;tab:mem")
	time.Sleep(100 * time.Millisecond)
	inW.WriteString("bers\x1b\\")
	waitFor(t, "tab:members", shows("Retired 1"))

	inW.WriteString("Q")
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("panel ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Q did not quit the panel")
	}
	raw, _ := out.snapshot()
	if strings.Contains(raw, "amqui") || strings.Contains(raw, "\x1b_") {
		t.Fatal("APC bytes reached the panel's output")
	}
	if !strings.Contains(raw, "48;2;") {
		t.Fatal("no truecolor background in the output")
	}
	seen, bad := erases(raw)
	if bad != nil {
		t.Fatalf("erases on the default background: %v", bad)
	}
	t.Logf("line erases the renderer used, all under a set background: %v", seen)
}

// viewObserver records the latest ANSI-stripped frame rendered by tea.Model so
// integration tests assert against the actual current screen rather than raw
// incremental terminal diff bytes.
type viewObserver struct {
	mu    sync.Mutex
	frame string
}

func (o *viewObserver) record(v tea.View) {
	o.mu.Lock()
	o.frame = ansi.Strip(v.Content)
	o.mu.Unlock()
}

func (o *viewObserver) current() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.frame
}

type observedModel struct {
	tea.Model
	obs *viewObserver
}

func (m observedModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.Model.Update(msg)
	return observedModel{Model: next, obs: m.obs}, cmd
}

func (m observedModel) View() tea.View {
	v := m.Model.View()
	m.obs.record(v)
	return v
}

// TestPanelProgramDrillDownStreamSwitch verifies live Hub -> Epic drill-down
// stream switching (--session <epic>) and Esc / Backspace return (--session
// <hub>) end to end through runPanel, apc.Reader, ui.Model, and streamManager.
func TestPanelProgramDrillDownStreamSwitch(t *testing.T) {
	dir := t.TempDir()
	hubFixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "panel", "edited-personal-cockpit.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	epicFixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "panel", "edited-crew-attention.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	callsFile := filepath.Join(dir, "calls.log")
	script := filepath.Join(dir, "rex-amq-adapter")
	body := fmt.Sprintf(`#!/bin/sh
echo "$*" >> '%s'
sess=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--session" ]; then sess="$a"; fi
  prev="$a"
done
case "$sess" in
  session:p) cat '%s' ;;
  session:s) cat '%s' ;;
  *) exit 1 ;;
esac
exec sleep 60
`, callsFile, hubFixture, epicFixture)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inW.Close()
	out := &syncBuffer{}
	obs := &viewObserver{}
	errc := make(chan error, 1)
	go func() {
		errc <- runPanel(context.Background(), panelConfig{
			Session:    "session:p",
			AdapterBin: script,
			CacheDir:   filepath.Join(dir, "cache"),
			In:         inR,
			Out:        out,
			Width:      90,
			Height:     48,
			Options:    ui.Options{},
			wrap:       func(m tea.Model) tea.Model { return observedModel{Model: m, obs: obs} },
		})
	}()

	readCalls := func() []string {
		b, err := os.ReadFile(callsFile)
		if err != nil {
			return nil
		}
		s := strings.TrimSpace(string(b))
		if s == "" {
			return nil
		}
		return strings.Split(s, "\n")
	}
	frameHas := func(want ...string) func() bool {
		return func() bool {
			f := obs.current()
			for _, s := range want {
				if !strings.Contains(f, s) {
					return false
				}
			}
			return true
		}
	}

	// 1. Initial Hub stream starts on --session session:p and shows Epics.
	waitFor(t, "initial Hub Epics view", frameHas("Epics 2"))
	waitFor(t, "initial Hub stream call", func() bool { return len(readCalls()) >= 1 })

	// 2. Enter drills down in-place into auth-refresh (session:s).
	inW.WriteString("\r")
	waitFor(t, "Epic stream subscription", func() bool { return len(readCalls()) >= 2 })
	waitFor(t, "drilled breadcrumb and Epic Overview alerts", frameHas("← me › auth-refresh", "SYSTEM ALERTS  5"))

	// 3. Tab into Epic Members tab verifies live Epic data.
	inW.WriteString("\t")
	waitFor(t, "drilled Epic Members tab", frameHas("Retired 1", "implementer(claude-02)"))

	// 4. Esc returns cleanly to Hub Epics and resubscribes --session session:p.
	inW.WriteString("\x1b")
	waitFor(t, "Hub stream resubscription after Esc", func() bool { return len(readCalls()) >= 3 })
	waitFor(t, "Hub Epics restored after Esc", func() bool {
		f := obs.current()
		return strings.Contains(f, "Epics 2") && !strings.Contains(f, "← me › auth-refresh")
	})

	// 5. Drill in again and verify Backspace also returns to Hub Epics.
	inW.WriteString("\r")
	waitFor(t, "second Epic stream subscription", func() bool { return len(readCalls()) >= 4 })
	waitFor(t, "second drilled breadcrumb", frameHas("← me › auth-refresh"))

	inW.WriteString("\x7f")
	waitFor(t, "Hub stream resubscription after Backspace", func() bool { return len(readCalls()) >= 5 })
	waitFor(t, "Hub Epics restored after Backspace", func() bool {
		f := obs.current()
		return strings.Contains(f, "Epics 2") && !strings.Contains(f, "← me › auth-refresh")
	})

	// 6. Quit cleanly.
	inW.WriteString("Q")
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("panel ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Q did not quit the panel")
	}

	got := readCalls()
	want := []string{
		"stream panel --session session:p",
		"stream panel --session session:s",
		"stream panel --session session:p",
		"stream panel --session session:s",
		"stream panel --session session:p",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("adapter stream calls:\n got: %v\nwant: %v", got, want)
	}
}

func TestStreamManagerRejectsStaleGeneration(t *testing.T) {
	m := newStreamManager("/bin/false", "session:p")

	// Before run() dequeues initial gen=0: out-of-order Switch(hub, 2) then Switch(epic, 1).
	m.Switch("session:p", 2)
	m.Switch("session:s", 1)
	m.Switch("session:s", 0)
	m.Switch("session:s", 2)

	m.mu.Lock()
	gotQueued := m.next
	m.mu.Unlock()
	if gotQueued == nil || gotQueued.session != "session:p" || gotQueued.gen != 2 {
		t.Fatalf("queued request = %+v, want {session:session:p gen:2}", gotQueued)
	}

	// After run() dequeues gen=2 and starts its child stream: delayed Switch(epic, 1)
	// must neither overwrite m.next nor cancel the running gen=2 child.
	cancelled := false
	m.mu.Lock()
	m.next = nil
	m.cancel = func() { cancelled = true }
	m.mu.Unlock()

	m.Switch("session:s", 1)
	m.Switch("session:s", 2)

	m.mu.Lock()
	afterStale := m.next
	m.mu.Unlock()
	if cancelled {
		t.Fatal("stale Switch cancelled the active higher-generation stream")
	}
	if afterStale != nil {
		t.Fatalf("stale Switch queued %+v, want nil", afterStale)
	}

	// A strictly newer generation still cancels the active child and queues cleanly.
	m.Switch("session:s", 3)
	m.mu.Lock()
	afterNew := m.next
	m.mu.Unlock()
	if !cancelled {
		t.Fatal("newer Switch(gen=3) did not cancel the active gen=2 stream")
	}
	if afterNew == nil || afterNew.session != "session:s" || afterNew.gen != 3 {
		t.Fatalf("newer queued request = %+v, want {session:session:s gen:3}", afterNew)
	}
}

type reorderEnterEscModel struct {
	observedModel
	delayedEnter chan tea.Cmd
}

func (m reorderEnterEscModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.observedModel.Update(msg)
	obs := next.(observedModel)
	if kp, ok := msg.(tea.KeyPressMsg); ok && kp.Code == tea.KeyEnter && cmd != nil {
		select {
		case m.delayedEnter <- cmd:
			cmd = nil
		default:
		}
	}
	return reorderEnterEscModel{observedModel: obs, delayedEnter: m.delayedEnter}, cmd
}

// TestPanelProgramOutOfOrderEnterEscKeepsHubLive drives real Enter (drillDown,
// gen=1) and Esc (popBack, gen=2) tea.Cmds out of order — executing gen=2
// before gen=1 — and verifies generation 2 stays active and delivers live Hub
// updates rather than being replaced by stale generation 1.
func TestPanelProgramOutOfOrderEnterEscKeepsHubLive(t *testing.T) {
	dir := t.TempDir()
	hubRaw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "panel", "edited-personal-cockpit.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	hubSnap1Line := strings.SplitN(string(hubRaw), "\n", 2)[0]
	if !strings.Contains(hubSnap1Line, `"seq":1`) || !strings.Contains(hubSnap1Line, `"billing"`) {
		t.Fatalf("unexpected Hub fixture snapshot line")
	}
	hubSnap2Line := strings.Replace(hubSnap1Line, `"seq":1`, `"seq":2`, 1)
	hubSnap2Line = strings.Replace(hubSnap2Line, `"billing"`, `"live-hub-update"`, 2)

	hubSnap1 := filepath.Join(dir, "hub1.ndjson")
	hubSnap2 := filepath.Join(dir, "hub2.ndjson")
	if err := os.WriteFile(hubSnap1, []byte(hubSnap1Line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hubSnap2, []byte(hubSnap2Line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	epicFixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "panel", "edited-crew-attention.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	callsFile := filepath.Join(dir, "calls.log")
	firstHub := filepath.Join(dir, "first-hub")
	gen2Ready := filepath.Join(dir, "gen2-ready")
	releaseLive := filepath.Join(dir, "release-live")
	script := filepath.Join(dir, "rex-amq-adapter")
	body := fmt.Sprintf(`#!/bin/sh
echo "$*" >> '%s'
sess=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--session" ]; then sess="$a"; fi
  prev="$a"
done
case "$sess" in
  session:p)
    cat '%s'
    if [ ! -e '%s' ]; then
      touch '%s'
      exec sleep 60
    fi
    touch '%s'
    while [ ! -e '%s' ]; do sleep 0.02; done
    cat '%s'
    exec sleep 60
    ;;
  session:s)
    cat '%s'
    exec sleep 60
    ;;
  *) exit 1 ;;
esac
`, callsFile, hubSnap1, firstHub, firstHub, gen2Ready, releaseLive, hubSnap2, epicFixture)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inW.Close()
	out := &syncBuffer{}
	obs := &viewObserver{}
	delayedEnter := make(chan tea.Cmd, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- runPanel(context.Background(), panelConfig{
			Session:    "session:p",
			AdapterBin: script,
			CacheDir:   filepath.Join(dir, "cache"),
			In:         inR,
			Out:        out,
			Width:      90,
			Height:     48,
			Options:    ui.Options{},
			wrap: func(m tea.Model) tea.Model {
				return reorderEnterEscModel{
					observedModel: observedModel{Model: m, obs: obs},
					delayedEnter:  delayedEnter,
				}
			},
		})
	}()

	waitFor(t, "initial Hub Epics view", func() bool {
		return strings.Contains(obs.current(), "billing")
	})

	// Send Enter (drillDown -> gen=1), holding its subscribe tea.Cmd.
	inW.WriteString("\r")
	var staleDrillCmd tea.Cmd
	select {
	case staleDrillCmd = <-delayedEnter:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Enter drillDown subscribe cmd")
	}

	// Send Esc (popBack -> gen=2) and wait until generation 2 is actively running.
	inW.WriteString("\x1b")
	waitFor(t, "generation 2 Hub stream active", func() bool {
		_, err := os.Stat(gen2Ready)
		return err == nil
	})

	// Now execute the delayed gen=1 drillDown cmd out of order; it must be ignored.
	staleDrillCmd()

	// Release a live update on the generation 2 Hub stream and verify it renders.
	if err := os.WriteFile(releaseLive, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "live Hub update on generation 2 after stale gen=1 Switch", func() bool {
		return strings.Contains(obs.current(), "live-hub-update")
	})

	inW.WriteString("Q")
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("panel ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Q did not quit the panel")
	}

	b, err := os.ReadFile(callsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{
		"stream panel --session session:p",
		"stream panel --session session:p",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("adapter stream calls:\n got: %v\nwant: %v", got, want)
	}
}

// sgrBackground applies an SGR's parameters to bg (whether a background
// colour is set), refusing what it does not model: reverse video, and
// extended colours whose arguments are not 5;n or 2;r;g;b in 0..255.
func sgrBackground(params string, bg *bool) error {
	ps := strings.Split(params, ";")
	num := func(s string) int {
		n, err := strconv.Atoi(s)
		if err != nil {
			return -1
		}
		return n
	}
	for i := 0; i < len(ps); i++ {
		p := num(ps[i])
		if ps[i] == "" {
			p = 0
		}
		switch {
		case p == 0 || p == 49:
			*bg = false
		case p >= 40 && p <= 47, p >= 100 && p <= 107:
			*bg = true
		case p == 38 || p == 48 || p == 58:
			rest := ps[i+1:]
			args := 0
			switch {
			case len(rest) >= 2 && rest[0] == "5":
				args = 2
			case len(rest) >= 4 && rest[0] == "2":
				args = 4
			default:
				return fmt.Errorf("SGR %q: extended colour without 5;n or 2;r;g;b", params)
			}
			for _, a := range rest[1:args] {
				if v := num(a); v < 0 || v > 255 {
					return fmt.Errorf("SGR %q: colour argument %q", params, a)
				}
			}
			if p == 48 {
				*bg = true
			}
			i += args
		case p == 7:
			return fmt.Errorf("SGR %q: reverse video is not modelled", params)
		case p >= 1 && p <= 9, p >= 21 && p <= 29, p >= 30 && p <= 37, p == 39, p == 59, p >= 90 && p <= 97:
		default:
			return fmt.Errorf("SGR %q: unmodelled parameter %q", params, ps[i])
		}
	}
	return nil
}

func TestErasesCheckDetectsDefaultBackground(t *testing.T) {
	if _, bad := erases("\x1b[48;2;1;2;3mab\x1b[38;2;49;0;0mc\x1b[>4;1m\x1b[K"); bad != nil {
		t.Fatalf("an erase under a background flagged: %v", bad)
	}
	for _, raw := range []string{"ab\x1b[K", "\x1b[48;2;1;2;3mab\x1b[m\x1b[5X", "\x1b[48;5;3ma\x1b[49m\x1b[K",
		"\x1b[10m\x1b[K",                   // a font, not a background
		"\x1b[48;2;1;2;3m\x1b[7m\x1b[K",    // reverse video
		"\x1b[48;2;1;2m\x1b[K",             // truncated colour
		"\x1b[48;2;1;2;300m\x1b[K",         // out of range
		"\x1b[48;2;1;2;3m\x1b[53m\x1b[K"} { // unmodelled

		if _, bad := erases(raw); bad == nil {
			t.Errorf("%q: not detected", raw)
		}
	}
}

var csi = regexp.MustCompile(`\x1b\[([0-9;:?]*)([@-~])`)

// erases checks that the renderer erases within a line (EL K, ECH X) only
// while a background colour is set: the cells it clears then keep the
// panel's background on a terminal with bce (Rex's TERM, xterm-256color,
// declares it). It returns the erase sequences seen and those that erase
// on the default background.
func erases(raw string) (seen map[string]int, bad []string) {
	seen = map[string]int{}
	bg := false
	for _, m := range csi.FindAllStringSubmatch(raw, -1) {
		params, final := m[1], m[2]
		switch final {
		case "m":
			if strings.ContainsAny(params[:min(len(params), 1)], "<=>?") {
				continue // a private mode (XTMODKEYS), not SGR
			}
			if err := sgrBackground(params, &bg); err != nil {
				bad = append(bad, err.Error())
			}
		case "K", "X":
			seen["CSI "+params+final]++
			if !bg {
				bad = append(bad, "CSI "+params+final)
			}
		}
	}
	return seen, bad
}

func TestOpenExitCodes(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	rex := env(map[string]string{"REX_SERVER": "unix:///nonexistent/rex.sock", "REX_SESSION": "session:s"})
	for _, c := range []struct {
		name string
		args []string
		env  func(string) string
		want int
	}{
		{"help", []string{"--help"}, rex, 0},
		{"unknown flag", []string{"--nope"}, rex, 2},
		{"unknown tab", []string{"--tab", "inbox"}, rex, 2},
		{"no server", nil, env(map[string]string{"REX_SESSION": "session:s"}), 2},
		{"no session", nil, env(map[string]string{"REX_SERVER": "unix:///x.sock"}), 2},
		{"Rex unreachable", []string{"--tab", "mail"}, rex, 1},
	} {
		var out, errb bytes.Buffer
		if got := runOpen(c.args, c.env, t.TempDir(), &out, &errb); got != c.want || out.Len() != 0 {
			t.Errorf("%s: exit %d stdout %q, want exit %d and no stdout", c.name, got, out.String(), c.want)
		}
	}
}

func TestOpenUsesPersistedSizeHermetically(t *testing.T) {
	openView := func(withPanel bool, bounds rexctl.Bounds) rexctl.View {
		tiled := rexctl.Layer{LayerID: "layer:tiled", Kind: rexctl.LayerTiled, Bounds: rexctl.Bounds{0, 0, 1, 1},
			Layout: &rexctl.LayoutNode{BlockID: "block:a", Rect: rexctl.Bounds{0, 0, 1, 1}},
			Blocks: []rexctl.Block{{BlockID: "block:a"}}}
		w := rexctl.Window{WindowID: "window:1", FocusedBlockID: "block:a", Layers: []rexctl.Layer{tiled}}
		if withPanel {
			w.Layers = append(w.Layers, rexctl.Layer{LayerID: "layer:p", Kind: rexctl.LayerFloating, Bounds: bounds,
				State:  rexctl.LayerMinimized,
				Layout: &rexctl.LayoutNode{BlockID: "block:p", Rect: rexctl.Bounds{0, 0, 1, 1}},
				Blocks: []rexctl.Block{{BlockID: "block:p", Label: layer.Label, Owner: "client:app"}}})
		}
		return rexctl.View{Revision: 1, SessionID: "session:s", ActiveWindowID: "window:1", Windows: []rexctl.Window{w}}
	}
	handleBlockMethod := func(s *rexctltest.Server) {
		s.Handle("block.method", func(r rexctltest.Request) (any, *rexctltest.Error) {
			var p struct {
				Method string `json:"method"`
				Args   struct {
					Data string `json:"data"`
				} `json:"args"`
			}
			r.Decode(&p)
			if p.Method == "write" {
				return map[string]any{"bytes_written": len(p.Args.Data)}, nil
			}
			return map[string]any{"columns": 200, "rows": 50, "cell_width_px": 9, "cell_height_px": 22, "owner": "client:app"}, nil
		})
	}
	envFor := func(url string) func(string) string {
		kv := map[string]string{"REX_SERVER": url, "REX_SESSION": "session:s"}
		return func(k string) string { return kv[k] }
	}

	t.Run("new layer uses persisted board size", func(t *testing.T) {
		cache := t.TempDir()
		if err := layer.SaveSize(cache, layer.Board); err != nil {
			t.Fatal(err)
		}
		s := rexctltest.NewServer(t)
		handleBlockMethod(s)
		s.SetView(openView(false, rexctl.Bounds{}))
		var out, errb bytes.Buffer
		if code := runOpen(nil, envFor(s.URL), cache, &out, &errb); code != 0 {
			t.Fatalf("exit %d stderr %q", code, errb.String())
		}
		r, ok := s.Last("session.new_layer")
		if !ok {
			t.Fatal("expected session.new_layer")
		}
		var p struct {
			Bounds rexctl.Bounds `json:"bounds"`
		}
		r.Decode(&p)
		if p.Bounds != layer.Board.Bounds() {
			t.Fatalf("new_layer bounds %v, want Board %v", p.Bounds, layer.Board.Bounds())
		}
	})

	t.Run("restored layer keeps current bounds even when state.json says board", func(t *testing.T) {
		cache := t.TempDir()
		if err := layer.SaveSize(cache, layer.Board); err != nil {
			t.Fatal(err)
		}
		s := rexctltest.NewServer(t)
		handleBlockMethod(s)
		s.SetView(openView(true, layer.Drawer.Bounds()))
		var out, errb bytes.Buffer
		if code := runOpen(nil, envFor(s.URL), cache, &out, &errb); code != 0 {
			t.Fatalf("exit %d stderr %q", code, errb.String())
		}
		if _, resized := s.Last("session.resize_layer"); resized {
			t.Fatal("restoring without --board resized an already-open layer")
		}
	})

	t.Run("explicit --board overrides persisted drawer size", func(t *testing.T) {
		cache := t.TempDir()
		if err := layer.SaveSize(cache, layer.Drawer); err != nil {
			t.Fatal(err)
		}
		s := rexctltest.NewServer(t)
		handleBlockMethod(s)
		s.SetView(openView(false, rexctl.Bounds{}))
		var out, errb bytes.Buffer
		if code := runOpen([]string{"--board"}, envFor(s.URL), cache, &out, &errb); code != 0 {
			t.Fatalf("exit %d stderr %q", code, errb.String())
		}
		r, _ := s.Last("session.new_layer")
		var p struct {
			Bounds rexctl.Bounds `json:"bounds"`
		}
		r.Decode(&p)
		if p.Bounds != layer.Board.Bounds() {
			t.Fatalf("new_layer bounds %v, want Board %v", p.Bounds, layer.Board.Bounds())
		}
	})

	t.Run("bare panel startup resolves persisted size", func(t *testing.T) {
		cache := t.TempDir()
		if got := startPanelSize(false, cache); got != layer.Drawer {
			t.Fatalf("missing state: got %v, want Drawer", got)
		}
		if err := layer.SaveSize(cache, layer.Board); err != nil {
			t.Fatal(err)
		}
		if got := startPanelSize(false, cache); got != layer.Board {
			t.Fatalf("persisted board: got %v, want Board", got)
		}
		if err := layer.SaveSize(cache, layer.Drawer); err != nil {
			t.Fatal(err)
		}
		if got := startPanelSize(true, cache); got != layer.Board {
			t.Fatalf("explicit --board: got %v, want Board", got)
		}
	})
}
