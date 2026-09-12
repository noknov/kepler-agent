//go:build unix

package local

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestRunCommandCancelsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _ = runCommand(exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 10 & wait"), t.TempDir(), nil, nil, t.TempDir())
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("process tree took %s to stop", elapsed)
	}
}
