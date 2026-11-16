package ui

import "github.com/charmbracelet/x/ansi"

// plain is a frame without its styling.
func plain(s string) string { return ansi.Strip(s) }
