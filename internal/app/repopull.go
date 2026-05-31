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

// pullWorkspaceRepos periodically git-fetches each sub-repo under the given
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
				cmd := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "--prune", "origin")
				cmd.Env = gitFetchEnv()
				out, pullErr := cmd.CombinedOutput()
				if pullErr != nil {
					log.Printf("workspace pull %s: %s", e.Name(), strings.TrimSpace(string(out)))
				} else {
					log.Printf("workspace fetch %s: ok", e.Name())
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

func gitFetchEnv() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
}
