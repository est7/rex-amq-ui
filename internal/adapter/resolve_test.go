package adapter

import (
	"errors"
	"os/exec"
	"testing"
)

func TestResolveBinOrder(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "REX_AMQ_ADAPTER_BIN" {
				return v
			}
			return ""
		}
	}
	execs := func(paths ...string) func(string) bool {
		return func(p string) bool {
			for _, q := range paths {
				if p == q {
					return true
				}
			}
			return false
		}
	}
	onPath := func(string) (string, error) { return "/usr/local/bin/rex-amq-adapter", nil }
	notOnPath := func(string) (string, error) { return "", exec.ErrNotFound }

	cases := []struct {
		name     string
		getenv   func(string) string
		isExec   func(string) bool
		lookPath func(string) (string, error)
		want     string
	}{
		{"the variable wins, even over an installed binary and unchecked", env("/tmp/missing/rex-amq-adapter"), execs(HomebrewBin), onPath, "/tmp/missing/rex-amq-adapter"},
		{"Homebrew before PATH", env(""), execs(HomebrewBin), onPath, HomebrewBin},
		{"PATH last", env(""), execs(), onPath, "/usr/local/bin/rex-amq-adapter"},
	}
	for _, c := range cases {
		if got, err := resolveBin(c.getenv, c.isExec, c.lookPath); err != nil || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.name, got, err, c.want)
		}
	}
	if got, err := resolveBin(env(""), execs(), notOnPath); !errors.Is(err, ErrNoAdapter) {
		t.Errorf("nothing found: %q %v", got, err)
	}
}
