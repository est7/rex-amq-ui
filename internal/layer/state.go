package layer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const stateFile = "state.json"

type persistedState struct {
	Size string `json:"size"`
}

// StatePath is <cacheDir>/rex-amq-ui/state.json, or "" when cacheDir is empty.
func StatePath(cacheDir string) string {
	if cacheDir == "" {
		return ""
	}
	return filepath.Join(cacheDir, Label, stateFile)
}

// LoadSize reads the persisted panel size from <cacheDir>/rex-amq-ui/state.json.
// When cacheDir is empty, the file is missing or unreadable, or the JSON or
// size value is invalid, it returns Drawer, false.
func LoadSize(cacheDir string) (Size, bool) {
	path := StatePath(cacheDir)
	if path == "" {
		return Drawer, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Drawer, false
	}
	var ps persistedState
	if err := json.Unmarshal(b, &ps); err != nil {
		return Drawer, false
	}
	switch ps.Size {
	case "drawer":
		return Drawer, true
	case "board":
		return Board, true
	default:
		return Drawer, false
	}
}

// SaveSize atomically writes {"size":"drawer"} or {"size":"board"} to
// <cacheDir>/rex-amq-ui/state.json. When cacheDir is empty it does nothing.
func SaveSize(cacheDir string, s Size) error {
	path := StatePath(cacheDir)
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(persistedState{Size: strings.ToLower(s.String())})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, "state-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	keep = true
	return nil
}
