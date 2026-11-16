// Command rex-amq-ui is the human's floating Crew and Cockpit panel for a Rex
// session. It reads `rex-amq-adapter panel --json` and drives its own
// floating layer over Rex's control API.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// version is set at build time with -ldflags "-X main.version=<v>".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rex-amq-ui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print the version and exit")
	// --session and --adapter-bin are the panel's inputs; the panel itself
	// arrives with the TUI views, so they are parsed but not used yet.
	_ = fs.String("session", "", "watch Rex session `S` instead of the caller's")
	_ = fs.String("adapter-bin", "", "rex-amq-adapter binary `path` (default: $REX_AMQ_ADAPTER_BIN, Homebrew, then PATH)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, "rex-amq-ui", version)
		return 0
	}
	fmt.Fprintln(stderr, "rex-amq-ui: the panel is not built yet; only --version works")
	return 2
}
