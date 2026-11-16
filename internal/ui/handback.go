package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

var errNoViewer = errors.New("install md-viewer or set $EDITOR to read it")

// openHandBack writes the hand-back to a read-only Markdown file in the
// cache and shows it beside the pane the human was in: with md-viewer, or
// failing that in $EDITOR split in beside that pane. The panel minimizes
// once it shows.
func (m Model) openHandBack() tea.Cmd {
	if m.st.Panel == nil || m.st.Panel.View == nil || m.st.Panel.View.HandBack == nil {
		return func() tea.Msg { return actionDone{what: "open hand-back", err: errors.New("no hand-back yet")} }
	}
	hb := *m.st.Panel.View.HandBack
	session := m.st.Scope.SessionID
	d, anchor := m.deps, m.lay.Focused
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		path, err := writeHandBack(d.CacheDir, session, hb.Seq, hb.Body)
		if err != nil {
			return actionDone{what: "open hand-back", err: err}
		}
		err = show(ctx, d, anchor, path)
		if err == nil && d.Layer != nil {
			err = d.Layer.Minimize(ctx)
		}
		return actionDone{what: "open hand-back", leaves: err == nil, err: err}
	}
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// writeHandBack writes body to <dir>/rex-amq-ui/<session>-hand-back-<seq>.md,
// read-only, replacing an earlier copy.
func writeHandBack(dir, session string, seq int, body string) (string, error) {
	if dir == "" {
		return "", errors.New("no cache directory to write the hand-back to")
	}
	dir = filepath.Join(dir, "rex-amq-ui")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-hand-back-%d.md", unsafeName.ReplaceAllString(session, "_"), seq)
	path := filepath.Join(dir, name)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, os.WriteFile(path, []byte(body), 0o444)
}

// show opens path for reading next to anchor.
func show(ctx context.Context, d Deps, anchor, path string) error {
	if d.LookPath != nil && d.Exec != nil {
		if bin, err := d.LookPath("md-viewer"); err == nil {
			// md-viewer splits the pane REX_BLOCK names; the panel's own block
			// is about to be minimized, so it names the human's pane.
			var env []string
			if anchor != "" {
				env = []string{"REX_BLOCK=" + anchor}
			}
			return d.Exec(ctx, env, bin, "open", path)
		}
	}
	editor := strings.Fields(d.Editor)
	if len(editor) == 0 || d.Layer == nil || anchor == "" {
		return errNoViewer
	}
	return d.Layer.OpenBeside(ctx, anchor, "hand-back", append(editor, path))
}
