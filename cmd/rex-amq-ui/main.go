// Command rex-amq-ui is the human's floating Crew and Cockpit panel for a Rex
// session. It reads `rex-amq-adapter stream panel` and drives its own
// floating layer over Rex's control API.
//
//	rex-amq-ui [--session S] [--adapter-bin P] [--focus B] [--tab T] [--board]
//	    the panel itself, run inside the floating layer's terminal block
//	rex-amq-ui open [--session S] [--tab T] [--board] [--adapter-bin P]
//	    the launcher: open the session's panel, or restore the open one,
//	    print {"layer_id","block_id","restored"} as JSON, and exit
//	rex-amq-ui waiting --json [--session S] [--adapter-bin P]
//	    print who in the session waits on the human, from the adapter's
//	    first snapshot, as one JSON line, and exit (the Rex plugin's
//	    waiting key reads it)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/est7/rex-amq-ui/internal/adapter"
	"github.com/est7/rex-amq-ui/internal/apc"
	"github.com/est7/rex-amq-ui/internal/kitty"
	"github.com/est7/rex-amq-ui/internal/layer"
	"github.com/est7/rex-amq-ui/internal/rexctl"
	"github.com/est7/rex-amq-ui/internal/ui"
)

// version is set at build time with -ldflags "-X main.version=<v>".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	cacheDir, _ := os.UserCacheDir()
	if len(args) > 0 && args[0] == "open" {
		return runOpen(args[1:], os.Getenv, cacheDir, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "waiting" {
		return runWaiting(args[1:], os.Getenv, stdout, stderr, waitingTimeout)
	}
	fs := flag.NewFlagSet("rex-amq-ui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print the version and exit")
	session := fs.String("session", "", "watch Rex session `S` (default: $REX_SESSION)")
	adapterBin := fs.String("adapter-bin", "", "rex-amq-adapter binary `path` (default: $REX_AMQ_ADAPTER_BIN, Homebrew, then PATH)")
	focus := fs.String("focus", "", "the pane `block` the human was in when the panel opened")
	tab := fs.String("tab", "", "the tab to show first: "+strings.Join(apc.Tabs, ", "))
	board := fs.Bool("board", false, "the layer starts at Board size")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: rex-amq-ui [flags]        the panel (runs in its floating layer)")
		fmt.Fprintln(stderr, "       rex-amq-ui open [flags]   open or restore the session's panel")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, "rex-amq-ui", version)
		return 0
	}
	if *tab != "" && !slices.Contains(apc.Tabs, *tab) {
		fmt.Fprintf(stderr, "rex-amq-ui: no tab %q (want one of %s)\n", *tab, strings.Join(apc.Tabs, ", "))
		return 2
	}
	cfg := panelConfig{
		Server: os.Getenv("REX_SERVER"), Session: cmp(*session, os.Getenv("REX_SESSION")), Block: os.Getenv("REX_BLOCK"),
		AdapterBin: *adapterBin, CacheDir: cacheDir,
		Options: ui.Options{Tab: *tab, Focused: *focus, Size: startPanelSize(*board, cacheDir)},
		In: os.Stdin, Out: stdout,
		Graphics: &ui.Graphics{Dir: kitty.DefaultDir, PID: os.Getpid()},
	}
	if err := runPanel(context.Background(), cfg); err != nil {
		fmt.Fprintln(stderr, "rex-amq-ui:", err)
		return 1
	}
	return 0
}

func startPanelSize(board bool, cacheDir string) layer.Size {
	if board {
		return layer.Board
	}
	sz, _ := layer.LoadSize(cacheDir)
	return sz
}

func cmp(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// panelConfig is everything the panel reads from its process.
type panelConfig struct {
	Server, Session, Block string
	AdapterBin             string
	CacheDir               string
	Options                ui.Options
	In                     *os.File
	Out                    io.Writer
	// Size, when set, is the terminal's size (tests: the output is no
	// terminal to ask).
	Width, Height int
	// Graphics, when set, draws the Graph tab's image (Kitty frame files
	// in its Dir); nil leaves the Graph region background.
	Graphics *ui.Graphics
	// started, when set, is given the program before it runs (tests).
	started func(*tea.Program)
	// wrap, when set, wraps the panel's model (tests: inject a fault).
	wrap func(tea.Model) tea.Model
}

// runPanel runs the panel until the human quits it (Q).
func runPanel(parent context.Context, cfg panelConfig) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	// Outside Rex (no server or session) the panel still shows the stream;
	// its actions say "not connected to Rex". In Rex, Watch makes the
	// connection and keeps it, and the header shows while it is down.
	var ctl ui.Controller
	var panel *layer.Panel
	if cfg.Server != "" && cfg.Session != "" {
		panel = layer.New(cfg.Server, cfg.Session, cfg.Block, cfg.Options.Focused)
		ctl = panel
	}
	// The renderer and the Graph tab's Kitty commands share the terminal;
	// one lock keeps each write whole.
	out := lockOutput(cfg.Out)
	var gfx *ui.Graphics
	if cfg.Graphics != nil {
		g := *cfg.Graphics
		g.Out = out
		gfx = &g
	}
	model := ui.New(ui.Deps{
		Layer: ctl, Clock: ui.SystemClock{}, Exec: execTool, LookPath: exec.LookPath,
		CacheDir: cfg.CacheDir, Editor: os.Getenv("EDITOR"), Graphics: gfx,
	}, cfg.Options)

	in := apc.NewReader(cfg.In)
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out),
		// Every cell's background is the panel's surface integrity: never
		// let NO_COLOR or a dumb TERM strip it.
		tea.WithColorProfile(colorprofile.TrueColor),
		// A hidden panel writes nothing, a resize's repaint included.
		tea.WithFilter(ui.Filter)}
	if cfg.Width > 0 && cfg.Height > 0 {
		opts = append(opts, tea.WithWindowSize(cfg.Width, cfg.Height))
	}
	var root tea.Model = model
	if cfg.wrap != nil {
		root = cfg.wrap(model)
	}
	prog := tea.NewProgram(root, opts...)
	in.On = func(c apc.Command) { prog.Send(c) }
	if cfg.started != nil {
		cfg.started(prog)
	}

	var wg sync.WaitGroup
	wg.Go(func() { stream(ctx, cfg, prog) })
	if panel != nil {
		wg.Go(func() { panel.Watch(ctx, func(s layer.State) { prog.Send(s) }) })
	}
	_, err := prog.Run()
	cancel()
	wg.Wait()
	return endPanel(parent, err, model)
}

// endPanel is the panel's result once Run returned err. Only the caller's
// cancellation (parent ended) ends it normally; a panic, which Run reports
// as killed too, stays an error. The Graph image and pixel mouse reports
// outlive the program's own terminal reset, so it clears them and the
// unread frame files through model, the model as built: it shares its
// graphics with every later copy, so this holds when Run returned no
// model, after a panic.
func endPanel(parent context.Context, err error, model ui.Model) error {
	if errors.Is(err, tea.ErrProgramKilled) && !errors.Is(err, tea.ErrProgramPanic) && parent.Err() != nil {
		err = nil
	}
	return errors.Join(err, model.CloseGraphics())
}

// lockedWriter serializes writes to one terminal from the renderer and
// from the Graph tab.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (l *lockedWriter) WriteString(s string) (int, error) { return l.Write([]byte(s)) }

// lockedFile is a lockedWriter that is still the terminal's file to Bubble
// Tea, which finds the terminal by its Fd (raw mode, size, colour).
type lockedFile struct {
	*os.File
	l *lockedWriter
}

func (f lockedFile) Write(p []byte) (int, error)       { return f.l.Write(p) }
func (f lockedFile) WriteString(s string) (int, error) { return f.l.WriteString(s) }

// lockOutput is w behind a lock, keeping it a file when it is one.
func lockOutput(w io.Writer) io.Writer {
	l := &lockedWriter{w: w}
	if f, ok := w.(*os.File); ok {
		return lockedFile{File: f, l: l}
	}
	return l
}

// stream runs the adapter and hands its updates to the panel.
func stream(ctx context.Context, cfg panelConfig, prog *tea.Program) {
	bin := cfg.AdapterBin
	if bin == "" {
		var err error
		if bin, err = adapter.ResolveBin(); err != nil {
			prog.Send(adapter.StatusUpdate{Status: adapter.Reconnecting, Err: err})
			return
		}
	}
	updates := make(chan adapter.Update)
	go func() {
		for u := range updates {
			prog.Send(u)
		}
	}()
	s := &adapter.Stream{Bin: bin, Session: cfg.Session}
	s.Run(ctx, updates)
	close(updates)
}

// execTool runs a helper (md-viewer) to completion, its output kept off
// the panel's terminal.
func execTool(ctx context.Context, env []string, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Env = append(os.Environ(), env...)
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runOpen is the launcher.
func runOpen(args []string, getenv func(string) string, cacheDir string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rex-amq-ui open", flag.ContinueOnError)
	fs.SetOutput(stderr)
	session := fs.String("session", "", "open the panel in Rex session `S` (default: $REX_SESSION)")
	tab := fs.String("tab", "", "the tab to show: "+strings.Join(apc.Tabs, ", "))
	board := fs.Bool("board", false, "open at Board size (centred, 88%) instead of Drawer (right, 42%)")
	adapterBin := fs.String("adapter-bin", "", "rex-amq-adapter binary `path` the panel runs")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: rex-amq-ui open [flags]")
		fmt.Fprintln(stderr, "Open the session's rex-amq-ui panel in a floating layer of its active window, or")
		fmt.Fprintln(stderr, "restore the one already open, and print its layer and block as JSON.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *tab != "" && !slices.Contains(apc.Tabs, *tab) {
		fmt.Fprintf(stderr, "rex-amq-ui open: no tab %q (want one of %s)\n", *tab, strings.Join(apc.Tabs, ", "))
		return 2
	}
	l := layer.Launch{Session: cmp(*session, getenv("REX_SESSION")), Tab: *tab, Board: *board}
	if !l.Board {
		l.Size, _ = layer.LoadSize(cacheDir)
	}
	server := getenv("REX_SERVER")
	if server == "" || l.Session == "" {
		fmt.Fprintln(stderr, "rex-amq-ui open: needs $REX_SERVER and a session (--session or $REX_SESSION)")
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "rex-amq-ui open:", err)
		return 1
	}
	l.Command = []string{exe}
	if *adapterBin != "" {
		l.Command = append(l.Command, "--adapter-bin", *adapterBin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := rexctl.Dial(ctx, server)
	if err != nil {
		fmt.Fprintln(stderr, "rex-amq-ui open:", err)
		return 1
	}
	defer c.Close()
	o, err := layer.Open(ctx, c, l)
	if err != nil {
		fmt.Fprintln(stderr, "rex-amq-ui open:", err)
		return 1
	}
	json.NewEncoder(stdout).Encode(o)
	return 0
}
