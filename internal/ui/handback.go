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

	"github.com/est7/rex-amq-ui/internal/adapter"
)

var errNoViewer = errors.New("install md-viewer or set $EDITOR to read it")

// openHandBack writes the current session's hand-back to a read-only
// Markdown file in the cache and shows it beside the pane the human was
// in: with md-viewer, or failing that in $EDITOR split in beside that
// pane. The panel minimizes once it shows.
func (m Model) openHandBack() tea.Cmd {
	var hb *adapter.HandBack
	if m.st.Panel != nil && m.st.Panel.View != nil {
		hb = m.st.Panel.View.HandBack
	}
	return m.openHandBackFor(m.currentSession(), hb)
}

// openEpicHandBack opens the selected Epic's hand-back directly from the
// Epics tab.
func (m Model) openEpicHandBack() tea.Cmd {
	e, ok := m.selectedEpic()
	if !ok {
		return m.openHandBackFor("", nil)
	}
	return m.openHandBackFor(e.SessionID, e.HandBack)
}

func (m Model) openHandBackFor(session string, hb *adapter.HandBack) tea.Cmd {
	if hb == nil {
		return func() tea.Msg { return actionDone{what: "open hand-back", err: errors.New("no hand-back yet")} }
	}
	copyHB := *hb
	d, anchor, shown := m.deps, m.lay.Focused, m.shown
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		path, err := writeHandBack(d.CacheDir, session, copyHB.Seq, copyHB.Body)
		if err != nil {
			return actionDone{what: "open hand-back", err: err}
		}
		err = show(ctx, d, anchor, "hand-back", path)
		if err == nil && d.Layer != nil {
			err = d.Layer.Minimize(ctx)
		}
		return actionDone{what: "open hand-back", leaves: err == nil, shown: shown, err: err}
	}
}

// openMail writes the selected message's envelope metadata and authorized
// session-view excerpt (if any) to a read-only Markdown file in the cache and
// shows it beside the human's pane, minimizing the panel on success. It never
// reads AMQ mail files directly.
func (m Model) openMail() tea.Cmd {
	msg, ok := m.selectedMail()
	if !ok {
		return func() tea.Msg { return actionDone{what: "open mail", err: errors.New("no message selected")} }
	}
	vm, hasVM := m.viewMessage(msg.ID)
	body := formatMailMarkdown(msg, vm, hasVM)
	session := m.currentSession()
	msgID := msg.ID
	d, anchor, shown := m.deps, m.lay.Focused, m.shown
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		path, err := writeMail(d.CacheDir, session, msgID, body)
		if err != nil {
			return actionDone{what: "open mail", err: err}
		}
		err = show(ctx, d, anchor, "mail", path)
		if err == nil && d.Layer != nil {
			err = d.Layer.Minimize(ctx)
		}
		return actionDone{what: "open mail", leaves: err == nil, shown: shown, err: err}
	}
}

func formatMailMarkdown(msg adapter.Message, vm adapter.ViewMessage, hasVM bool) string {
	var b strings.Builder
	subject := msg.Subject
	if subject == "" {
		subject = "(no subject)"
	}
	fmt.Fprintf(&b, "# %s\n\n", subject)
	fmt.Fprintf(&b, "- **From:** `%s`\n", orDash(msg.From))
	to := "—"
	if len(msg.To) > 0 {
		to = strings.Join(msg.To, ", ")
	}
	fmt.Fprintf(&b, "- **To:** `%s`\n", to)
	fmt.Fprintf(&b, "- **ID:** `%s`\n", orDash(msg.ID))
	fmt.Fprintf(&b, "- **Thread:** `%s`\n", orDash(msg.Thread))
	fmt.Fprintf(&b, "- **State:** `%s`\n", mailStateLabel(msg))
	created := "—"
	if !msg.CreatedAt.IsZero() {
		created = msg.CreatedAt.UTC().Format(time.RFC3339)
	}
	fmt.Fprintf(&b, "- **Created:** `%s`\n", created)
	if hasVM && vm.Kind != "" {
		fmt.Fprintf(&b, "- **Kind:** `%s`\n", vm.Kind)
	}
	if hasVM && vm.Priority != "" {
		fmt.Fprintf(&b, "- **Priority:** `%s`\n", vm.Priority)
	}
	b.WriteString("\n---\n\n")
	if hasVM && vm.Body != "" {
		b.WriteString(strings.TrimRight(vm.Body, "\n"))
		b.WriteByte('\n')
		if vm.Truncated {
			b.WriteString("\n_Excerpt truncated by session view._\n")
		}
	} else {
		b.WriteString("_No body excerpt was authorized in the session view._\n")
	}
	return b.String()
}

func mailStateLabel(msg adapter.Message) string {
	switch {
	case msg.State == adapter.MsgDeadLettered:
		return adapter.MsgDeadLettered
	case msg.Bounced && msg.State != "":
		return msg.State + " (bounced)"
	case msg.Bounced:
		return "bounced"
	}
	return orDash(msg.State)
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// writeHandBack writes body to <dir>/rex-amq-ui/<session>-hand-back-<seq>.md,
// read-only, replacing an earlier copy.
func writeHandBack(dir, session string, seq int, body string) (string, error) {
	if dir == "" {
		return "", errors.New("no cache directory to write the hand-back to")
	}
	name := fmt.Sprintf("%s-hand-back-%d.md", unsafeName.ReplaceAllString(session, "_"), seq)
	return writeReadOnlyCacheFile(dir, name, body)
}

// writeMail writes body to <dir>/rex-amq-ui/<session>-mail-<safeID>.md,
// read-only, replacing an earlier copy.
func writeMail(dir, session, id, body string) (string, error) {
	if dir == "" {
		return "", errors.New("no cache directory to write the mail to")
	}
	safeSession := unsafeName.ReplaceAllString(session, "_")
	safeID := unsafeName.ReplaceAllString(id, "_")
	if safeID == "" {
		safeID = "message"
	}
	name := fmt.Sprintf("%s-mail-%s.md", safeSession, safeID)
	return writeReadOnlyCacheFile(dir, name, body)
}

func writeReadOnlyCacheFile(dir, name, body string) (string, error) {
	dir = filepath.Join(dir, "rex-amq-ui")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, os.WriteFile(path, []byte(body), 0o444)
}

// show opens path for reading next to anchor.
func show(ctx context.Context, d Deps, anchor, label, path string) error {
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
	return d.Layer.OpenBeside(ctx, anchor, label, append(editor, path))
}
