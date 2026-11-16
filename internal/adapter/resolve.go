package adapter

import (
	"errors"
	"os"
	"os/exec"
)

// HomebrewBin is where `brew install rex-amq-adapter` puts the adapter.
const HomebrewBin = "/opt/homebrew/opt/rex-amq-adapter/bin/rex-amq-adapter"

// ErrNoAdapter is a resolution that found no adapter binary.
var ErrNoAdapter = errors.New("adapter: rex-amq-adapter not found: set REX_AMQ_ADAPTER_BIN, install it with Homebrew, or put it on PATH")

// ResolveBin finds the adapter binary: $REX_AMQ_ADAPTER_BIN as given (an
// explicit choice is never second-guessed), then the Homebrew install, then
// PATH.
func ResolveBin() (string, error) {
	return resolveBin(os.Getenv, isExecutable, exec.LookPath)
}

func resolveBin(getenv func(string) string, isExec func(string) bool, lookPath func(string) (string, error)) (string, error) {
	if p := getenv("REX_AMQ_ADAPTER_BIN"); p != "" {
		return p, nil
	}
	if isExec(HomebrewBin) {
		return HomebrewBin, nil
	}
	if p, err := lookPath("rex-amq-adapter"); err == nil {
		return p, nil
	}
	return "", ErrNoAdapter
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}
