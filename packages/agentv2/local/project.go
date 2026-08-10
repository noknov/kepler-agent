package local

import (
	"errors"
	"os"
	"path/filepath"
)

// ProjectInstructions loads repository-owned guidance from the workspace root.
// Private or user-specific overlays belong in Config.PromptFiles instead.
func ProjectInstructions(root string) ([]string, error) {
	var values []string
	for _, name := range []string{"AGENTS.md"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		values = append(values, string(data))
	}
	return values, nil
}
