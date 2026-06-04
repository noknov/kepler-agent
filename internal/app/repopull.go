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
		for _, dir := range discoverWorkspaceRepos(roots) {
			cmd := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "--prune", "--no-write-fetch-head", "origin")
			cmd.Env = gitFetchEnv()
			out, pullErr := cmd.CombinedOutput()
			if pullErr != nil {
				log.Printf("workspace fetch %s: %s", filepath.Base(dir), strings.TrimSpace(string(out)))
			} else {
				log.Printf("workspace fetch %s: ok", filepath.Base(dir))
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

func discoverWorkspaceRepos(roots []string) []string {
	seen := map[string]bool{}
	var repos []string
	add := func(dir string) {
		clean := filepath.Clean(dir)
		if seen[clean] {
			return
		}
		if _, err := os.Stat(filepath.Join(clean, ".git")); err == nil {
			seen[clean] = true
			repos = append(repos, clean)
		}
	}
	for _, root := range roots {
		add(root)
		entries, err := os.ReadDir(root)
		if err != nil {
			log.Printf("workspace fetch: cannot read %s: %v", root, err)
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				add(filepath.Join(root, e.Name()))
			}
		}
	}
	return repos
}

func gitFetchEnv() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
}
