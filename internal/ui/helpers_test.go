package ui

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/est7/rex-amq-ui/internal/adapter"
	"github.com/est7/rex-amq-ui/internal/apc"
	"github.com/est7/rex-amq-ui/internal/layer"
	"github.com/est7/rex-amq-ui/internal/rexctl"
)

type fixedClock time.Time

func (c fixedClock) Now() time.Time { return time.Time(c) }

func at(s string) fixedClock {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return fixedClock(t)
}

// fixtures are the s1 streams, with the time each is viewed at.
var fixtures = map[string]fixedClock{
	"edited-crew-attention":   at("2026-11-16T09:10:00Z"),
	"edited-personal-cockpit": at("2026-11-16T09:12:00Z"),
	"edited-no-view":          at("2026-11-16T07:10:00Z"),
	"recorded-bare":           at("2026-11-16T12:05:00Z"),
	"recorded-crew-lifecycle": at("2026-11-16T12:30:00Z"),
}

func frames(t testing.TB, name string) []adapter.Frame {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "panel", name+".ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []adapter.Frame
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			fr, derr := adapter.DecodeFrame(line)
			if derr != nil {
				t.Fatalf("%s: %v", name, derr)
			}
			out = append(out, fr)
		}
		if err != nil {
			return out
		}
	}
}

// reference sizes: the panel's cells in a 200x50 window, as `open` starts
// it.
func refSize(t testing.TB, s layer.Size) (int, int) {
	t.Helper()
	ss, err := rexctl.StartSizeFor(rexctl.TerminalSize{Columns: 200, Rows: 50}, rexctl.Bounds{0, 0, 1, 1}, s.Bounds())
	if err != nil {
		t.Fatal(err)
	}
	return ss.Columns, ss.Rows
}

// focusedPane is the pane the goldens mark ▶.
const focusedPane = "block:claude-02"

// build is the panel after the whole fixture streamed in, w x h, with the
// human last in focusedPane.
func build(t testing.TB, fixture string, w, h int, d Deps, o Options) Model {
	t.Helper()
	clock, ok := fixtures[fixture]
	if !ok {
		t.Fatalf("no fixture %s", fixture)
	}
	if d.Clock == nil {
		d.Clock = clock
	}
	m := New(d, o)
	m = step(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	m = step(t, m, adapter.StatusUpdate{Status: adapter.Connected})
	m = step(t, m, layer.State{Connected: true, Focused: focusedPane, Size: o.Size})
	for _, f := range frames(t, fixture) {
		m = step(t, m, adapter.FrameUpdate{Frame: f})
	}
	return m
}

func step(t testing.TB, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(Model)
}

// press sends a key, runs what it starts to completion, and feeds the
// results back.
func press(t testing.TB, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		next, cmd := m.Update(key(k))
		m = drain(t, next.(Model), cmd)
	}
	return m
}

func drain(t testing.TB, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			m = drain(t, m, c)
		}
	case actionDone:
		next, more := m.Update(msg)
		m = drain(t, next.(Model), more)
	}
	return m
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	}
	r := []rune(s)
	if len(r) != 1 {
		panic("key " + s)
	}
	k := tea.KeyPressMsg{Code: r[0], Text: s}
	if r[0] >= 'A' && r[0] <= 'Z' {
		k.Code, k.ShiftedCode, k.Mod = r[0]+('a'-'A'), r[0], tea.ModShift
	}
	return k
}

// recorder is a Controller that records its calls and fails those named
// in fail.
type recorder struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error
}

func (r *recorder) note(call string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
	name, _, _ := strings.Cut(call, " ")
	return r.fail[name]
}

func (r *recorder) Resize(_ context.Context, s layer.Size) error {
	return r.note("resize " + s.String())
}
func (r *recorder) Minimize(context.Context) error             { return r.note("minimize") }
func (r *recorder) Jump(_ context.Context, b string) error     { return r.note("jump " + b) }
func (r *recorder) JumpToSession(_ context.Context, s, b string) error {
	return r.note("jump-session " + s + " " + b)
}
func (r *recorder) SwitchTo(_ context.Context, s string) error { return r.note("switch " + s) }
func (r *recorder) OpenBeside(_ context.Context, anchor, label string, argv []string) error {
	return r.note(fmt.Sprintf("beside %s %s %v", anchor, label, argv))
}

// CellSize reports no cell size: the Graph tab draws no image.
func (r *recorder) CellSize(context.Context) (int, int, error) { return 0, 0, r.note("cellsize") }

func (r *recorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func mustTab(name string) apc.Command { return apc.Command{Verb: apc.Tab, Tab: name} }
