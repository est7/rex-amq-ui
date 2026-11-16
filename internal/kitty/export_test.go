package kitty

import "os"

// SetFiles replaces the transport's file system calls.
func SetFiles(t *Transport, write func(string, []byte, os.FileMode) error, remove func(string) error) {
	t.writeFile, t.remove = write, remove
}
