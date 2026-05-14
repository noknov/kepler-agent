package app

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// pullWorkspaceRepos periodically git-pulls each sub-repo under the given
// workspace roots so the agent always reads up-to-date code.
func pullWorkspaceRepos(ctx context.Context, roots []string, interval time.Duration) {
	pullAll := func() {
		for _, root := range roots {
			entries, err := os.ReadDir(root)
			if err != nil {
				log.Printf("workspace pull: cannot read %s: %v", root, err)
				continue
			}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				dir := filepath.Join(root, e.Name())
				if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
					continue
				}
				var out []byte
				var pullErr error
				for _, branch := range []string{"main", "master"} {
					cmd := exec.CommandContext(ctx, "git", "-C", dir, "pull", "--ff-only", "origin", branch)
					out, pullErr = cmd.CombinedOutput()
					if pullErr == nil {
						break
					}
				}
				if pullErr != nil {
					log.Printf("workspace pull %s: %s", e.Name(), strings.TrimSpace(string(out)))
				} else {
					log.Printf("workspace pull %s: ok", e.Name())
				}
			}
		}
	}

	pullAll()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pullAll()
		}
	}
}
