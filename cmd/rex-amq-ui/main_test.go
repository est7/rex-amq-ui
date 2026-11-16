package main

import (
	"bytes"
	"testing"
)

func TestVersionPrintsAndExitsZero(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--version"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if got := out.String(); got != "rex-amq-ui dev\n" {
		t.Fatalf("stdout %q", got)
	}
}

func TestUnknownFlagIsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--nope"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
}
