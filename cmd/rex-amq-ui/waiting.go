package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/est7/rex-amq-ui/internal/adapter"
)

// waitingTimeout is how long `waiting` gives the adapter to send its
// snapshot.
const waitingTimeout = 5 * time.Second

// Waiting is who waits on the human in a session: `rex-amq-ui waiting
// --json` prints it as one line, and the Rex plugin's waiting key acts on
// it (nobody, one pane to focus, or the panel's alerts).
type Waiting struct {
	Session string   `json:"session"`
	Members []Waiter `json:"members"`
}

// Waiter is a member with attention marks: the mark kinds, each once, in
// the order they first appear. BlockID is its pane, left out when the pane
// is gone and there is nothing to focus.
type Waiter struct {
	Handle  string   `json:"handle"`
	BlockID string   `json:"block_id,omitempty"`
	Reasons []string `json:"reasons"`
}

// waitingOf reads who waits from a stream's snapshot: the members the
// panel's attention marks, in handle order. It is never nil-membered, so
// the JSON says [] and not null.
func waitingOf(snap adapter.Snapshot, session string) Waiting {
	w := Waiting{Session: session, Members: []Waiter{}}
	if snap.Scope.SessionID != "" {
		w.Session = snap.Scope.SessionID
	}
	if snap.Panel == nil {
		return w
	}
	panes := map[string]string{}
	for _, a := range snap.Agents {
		if a.Pane == "present" {
			panes[a.Handle] = a.BlockID
		}
	}
	for handle, marks := range snap.Panel.Attention {
		if len(marks) == 0 {
			continue
		}
		m := Waiter{Handle: handle, BlockID: panes[handle]}
		for _, mark := range marks {
			if !slices.Contains(m.Reasons, mark.Kind) {
				m.Reasons = append(m.Reasons, mark.Kind)
			}
		}
		w.Members = append(w.Members, m)
	}
	sort.Slice(w.Members, func(i, j int) bool { return w.Members[i].Handle < w.Members[j].Handle })
	return w
}

// errNoSnapshot is an adapter that sent no snapshot in time.
var errNoSnapshot = errors.New("the adapter sent no snapshot")

// firstSnapshot runs the adapter until it sends its snapshot, and stops it.
// An adapter run that ends or fails first is that run's error: no retry,
// since the human is waiting on a key press.
func firstSnapshot(ctx context.Context, s *adapter.Stream, timeout time.Duration) (adapter.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out := make(chan adapter.Update)
	done := make(chan struct{})
	go func() {
		s.Run(ctx, out)
		close(done)
	}()
	// Run returns only once its adapter is stopped: never leave one behind.
	defer func() { cancel(); <-done }()
	for {
		select {
		case u := <-out:
			switch u := u.(type) {
			case adapter.FrameUpdate:
				if snap, ok := u.Frame.(adapter.Snapshot); ok {
					return snap, nil
				}
			case adapter.StatusUpdate:
				if u.Status == adapter.Reconnecting {
					if u.Err == nil {
						return adapter.Snapshot{}, adapter.ErrStreamEnded
					}
					return adapter.Snapshot{}, u.Err
				}
			}
		case <-ctx.Done():
			return adapter.Snapshot{}, fmt.Errorf("%w within %v", errNoSnapshot, timeout)
		}
	}
}

// runWaiting is `rex-amq-ui waiting`.
func runWaiting(args []string, getenv func(string) string, stdout, stderr io.Writer, timeout time.Duration) int {
	fs := flag.NewFlagSet("rex-amq-ui waiting", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print one JSON line (required: it is the only format)")
	session := fs.String("session", "", "Rex session `S` (default: $REX_SESSION)")
	adapterBin := fs.String("adapter-bin", "", "rex-amq-adapter binary `path` (default: $REX_AMQ_ADAPTER_BIN, Homebrew, then PATH)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: rex-amq-ui waiting --json [flags]")
		fmt.Fprintln(stderr, "Print who in the session waits on the human, from the adapter's first snapshot:")
		fmt.Fprintln(stderr, `  {"session":"…","members":[{"handle":"…","block_id":"…","reasons":["turn_error"]}]}`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if !*asJSON || fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	bin := *adapterBin
	if bin == "" {
		var err error
		if bin, err = adapter.ResolveBin(); err != nil {
			fmt.Fprintln(stderr, "rex-amq-ui waiting:", err)
			return 1
		}
	}
	s := &adapter.Stream{Bin: bin, Session: cmp(*session, getenv("REX_SESSION"))}
	snap, err := firstSnapshot(context.Background(), s, timeout)
	if err != nil {
		fmt.Fprintln(stderr, "rex-amq-ui waiting:", strings.TrimSpace(err.Error()))
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(waitingOf(snap, s.Session)); err != nil {
		fmt.Fprintln(stderr, "rex-amq-ui waiting:", err)
		return 1
	}
	return 0
}
