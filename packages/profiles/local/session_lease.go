package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Lock serializes an entire turn across independent CLI/app-server processes.
// This lock is separate from short transcript append locks: holding the turn
// lease must not deadlock its own durable writes. Kernel locks release on exit.
func (s *JSONLStore) Lock(ctx context.Context, key string) (func(), error) {
	sessionID, ok := strings.CutPrefix(key, "session:")
	if !ok || !safeID.MatchString(sessionID) {
		return nil, fmt.Errorf("invalid session lease key")
	}
	directory := filepath.Join(s.Root, sessionID)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	release, err := lockLocalFile(ctx, filepath.Join(directory, ".turn.lock"))
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(release) }, nil
}
