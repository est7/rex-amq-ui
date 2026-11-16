package apc

import (
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
)

// TestReaderUnderCancelReader reads through ultraviolet's cancel reader,
// as Bubble Tea does: it waits for the descriptor to be readable before
// each Read, so the Reader must never keep filtered bytes it could have
// returned, or they wait for an unrelated key.
func TestReaderUnderCancelReader(t *testing.T) {
	rf, wf := pipe(t)
	r := NewReader(rf)
	more := "[A" + strings.Repeat("b", 4094)
	waits := 0
	r.Wait = func(time.Duration) bool {
		waits++
		if waits == 1 {
			wf.Write([]byte(more)) // the rest of a sequence, and more, arrives at once
		}
		return waits == 1
	}
	cr, err := uv.NewCancelReader(r)
	if err != nil {
		t.Fatal(err)
	}
	defer cr.Close()
	want := strings.Repeat("a", 4095) + "\x1b" + more
	wf.Write([]byte(strings.Repeat("a", 4095) + "\x1b"))

	got := make(chan string)
	go func() {
		var all []byte
		buf := make([]byte, 4096) // Bubble Tea's read size
		for len(all) < len(want) {
			n, err := cr.Read(buf)
			all = append(all, buf[:n]...)
			if err != nil {
				break
			}
		}
		got <- string(all)
	}()
	select {
	case s := <-got:
		if s != want {
			t.Fatalf("read %d bytes, want %d", len(s), len(want))
		}
	case <-time.After(3 * time.Second):
		cr.Cancel()
		t.Fatal("filtered input stranded behind the descriptor poll")
	}
}

func TestReaderRefusesAShortBuffer(t *testing.T) {
	rf, wf := pipe(t)
	wf.Write([]byte("abc"))
	if n, err := NewReader(rf).Read(make([]byte, MinRead-1)); n != 0 || err == nil {
		t.Fatalf("read %d, %v", n, err)
	}
}
