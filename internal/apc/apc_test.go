package apc

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const st = "\x1b\\"

// stream mixes keys, other sequences, and this channel's sequences, valid
// and not; want is what passes through and cmds what it commands.
var (
	stream = "j" + Prefix + "pause" + st + "k\x1b[A" +
		"\x1b_Gi=1;OK" + st + // a Kitty graphics reply passes whole
		Prefix + "tab:mail" + st +
		Prefix + "bogus" + st + // unknown verb: consumed, ignored
		Prefix + "tab:nope" + st + // unknown tab: consumed, ignored
		"\x1b\x1b[B" + // Esc key, then an arrow
		Prefix + "zoom" + st + "q" + Prefix + "resume" + st + "x\x1b"
	want = "jk\x1b[A\x1b_Gi=1;OK" + st + "\x1b\x1b[Bqx\x1b"
	cmds = []Command{{Verb: Pause}, {Verb: Tab, Tab: "mail"}, {Verb: Zoom}, {Verb: Resume}}
)

func feedAll(p *Parser, chunks ...string) (string, []Command) {
	var out []byte
	var got []Command
	for _, c := range chunks {
		o, cs := p.Feed([]byte(c))
		out = append(out, o...)
		got = append(got, cs...)
	}
	return string(append(out, p.Flush()...)), got
}

func TestAPC(t *testing.T) {
	t.Run("every split point", func(t *testing.T) {
		for i := 0; i <= len(stream); i++ {
			for j := i; j <= len(stream); j++ {
				var p Parser
				out, got := feedAll(&p, stream[:i], stream[i:j], stream[j:])
				if out != want || !reflect.DeepEqual(got, cmds) {
					t.Fatalf("split %d/%d: out %q cmds %v", i, j, out, got)
				}
			}
		}
	})
	t.Run("byte at a time", func(t *testing.T) {
		var p Parser
		var chunks []string
		for i := range len(stream) {
			chunks = append(chunks, stream[i:i+1])
		}
		if out, got := feedAll(&p, chunks...); out != want || !reflect.DeepEqual(got, cmds) {
			t.Fatalf("out %q cmds %v", out, got)
		}
	})
	t.Run("no byte of a sequence leaks", func(t *testing.T) {
		for _, c := range []Command{{Verb: Pause}, {Verb: Resume}, {Verb: Zoom}, {Verb: Tab, Tab: "graph"}} {
			var p Parser
			out, got := p.Feed([]byte(Encode(c)))
			if len(out) != 0 || !reflect.DeepEqual(got, []Command{c}) || p.HoldsEsc() {
				t.Fatalf("%v: out %q cmds %v", c, out, got)
			}
		}
	})
	t.Run("every tab name", func(t *testing.T) {
		for _, name := range Tabs {
			var p Parser
			if _, got := p.Feed([]byte(Encode(Command{Verb: Tab, Tab: name}))); len(got) != 1 || got[0].Tab != name {
				t.Fatalf("tab:%s: %v", name, got)
			}
		}
	})
	t.Run("a cut-off sequence stops eating keys", func(t *testing.T) {
		var p Parser
		out, got := feedAll(&p, Prefix+"pa", "use"+strings.Repeat("e", 40)+"jk", Prefix+"resume"+st)
		if len(got) != 1 || got[0].Verb != Resume {
			t.Fatalf("cmds %v", got)
		}
		if !strings.HasSuffix(out, "jk") || strings.Contains(out, "amqui") {
			t.Fatalf("out %q", out)
		}
		// A key that cannot be part of a verb ends it at once.
		var q Parser
		if out, got := feedAll(&q, Prefix+"pa", "J"); out != "J" || got != nil {
			t.Fatalf("out %q cmds %v", out, got)
		}
	})
	t.Run("a sequence broken by another passes the other", func(t *testing.T) {
		var p Parser
		if out, got := feedAll(&p, Prefix+"pau\x1b[A"); out != "\x1b[A" || got != nil {
			t.Fatalf("out %q cmds %v", out, got)
		}
	})
	t.Run("lone esc is held, then flushed as the Esc key", func(t *testing.T) {
		var p Parser
		out, _ := p.Feed([]byte("\x1b"))
		if len(out) != 0 || !p.HoldsEsc() {
			t.Fatalf("out %q held %v", out, p.HoldsEsc())
		}
		if f := p.Flush(); string(f) != "\x1b" {
			t.Fatalf("flush %q", f)
		}
	})
	t.Run("reader", testReader)
}

func pipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return r, w
}

func testReader(t *testing.T) {
	t.Run("a sequence split across writes", func(t *testing.T) {
		rf, wf := pipe(t)
		r := NewReader(rf)
		var got []Command
		r.On = func(c Command) { got = append(got, c) }
		go func() {
			wf.Write([]byte("a" + Prefix + "pa"))
			time.Sleep(100 * time.Millisecond) // longer than any ESC timeout
			wf.Write([]byte("use" + st + "b"))
			wf.Close()
		}()
		var all bytes.Buffer
		buf := make([]byte, MinRead)
		for {
			n, err := r.Read(buf)
			all.Write(buf[:n])
			if err != nil {
				break
			}
		}
		if all.String() != "ab" || !reflect.DeepEqual(got, []Command{{Verb: Pause}}) {
			t.Fatalf("out %q cmds %v", all.String(), got)
		}
	})
	t.Run("the Esc key alone gets through", func(t *testing.T) {
		rf, wf := pipe(t)
		r := NewReader(rf) // polls the pipe
		wf.Write([]byte("\x1b"))
		buf := make([]byte, MinRead)
		start := time.Now()
		n, err := r.Read(buf)
		if err != nil || string(buf[:n]) != "\x1b" {
			t.Fatalf("read %q %v", buf[:n], err)
		}
		if d := time.Since(start); d < EscWait/2 {
			t.Fatalf("the ESC was not held to wait for more (%v)", d)
		}
	})
	t.Run("an ESC whose sequence follows at once is not the Esc key", func(t *testing.T) {
		rf, wf := pipe(t)
		r := NewReader(rf)
		var got []Command
		r.On = func(c Command) { got = append(got, c) }
		waited := 0
		r.Wait = func(time.Duration) bool {
			waited++
			wf.Write([]byte("_amqui;zoom" + st))
			return true
		}
		wf.Write([]byte("z\x1b"))
		buf := make([]byte, MinRead)
		n, _ := r.Read(buf)
		if string(buf[:n]) != "z" || waited != 1 || !reflect.DeepEqual(got, []Command{{Verb: Zoom}}) {
			t.Fatalf("read %q waited %d cmds %v", buf[:n], waited, got)
		}
	})
}
