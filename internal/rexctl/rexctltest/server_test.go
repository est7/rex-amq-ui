package rexctltest_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/est7/rex-amq-ui/internal/rexctl"
	"github.com/est7/rex-amq-ui/internal/rexctl/rexctltest"
)

// recorder is a test that only records what the fake reports.
type recorder struct {
	testing.TB
	mu   sync.Mutex
	errs []string
}

func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, format)
	_ = args
}

// The fake holds requests to Rex's published input schemas: a misspelled
// field, a missing required one, a wrong type, or an unknown block method
// is refused and fails the test that sent it.
func TestRexctltestRefusesRequestsOffSchema(t *testing.T) {
	cases := []struct {
		method  string
		payload any
		want    string
	}{
		{"session.focus_block", map[string]any{"session_id": "s", "blockid": "b"}, "missing required"},
		{"session.focus_block", map[string]any{"session_id": "s", "block_id": "b", "extra": 1}, "unknown field"},
		{"session.resize_layer", map[string]any{"session_id": "s", "layer_id": "l", "bounds": []float64{0, 0, 1}}, "anyOf"},
		{"session.set_layer_state", map[string]any{"session_id": "s", "layer_id": "l", "state": 3}, "not of type"},
		{"block.method", map[string]any{"session_id": "s", "block_id": "b", "method": "write", "args": map[string]any{"text": "x"}}, "missing required"},
		{"block.method", map[string]any{"session_id": "s", "block_id": "b", "method": "explode"}, "not one the fake knows"},
	}
	for _, c := range cases {
		rec := &recorder{TB: t}
		s := rexctltest.NewServer(rec)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cl, err := rexctl.Dial(ctx, s.URL)
		if err != nil {
			t.Fatal(err)
		}
		err = cl.Call(ctx, c.method, c.payload, nil)
		cl.Close()
		cancel()
		if !rexctl.IsCode(err, rexctl.CodeBadRequest) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v: %v, want bad_request mentioning %q", c.method, c.payload, err, c.want)
		}
	}
}

// Every request the client's methods send in the rexctl tests also passes
// here with a correct payload: the fake is not refusing everything.
func TestRexctltestAcceptsRequestsOnSchema(t *testing.T) {
	s := rexctltest.NewServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cl, err := rexctl.Dial(ctx, s.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if err := cl.Call(ctx, "session.focus_block", map[string]any{"session_id": "s", "block_id": "b"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := cl.Call(ctx, "session.resize_layer", map[string]any{"session_id": "s", "layer_id": "l", "bounds": map[string]any{"x": 0, "y": 0, "w": 1, "h": 1}}, nil); err != nil {
		t.Fatal(err)
	}
}
