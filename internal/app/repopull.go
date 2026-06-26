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

// pullWorkspaceRepos periodically git-fetches and fast-forwards each sub-repo
// under the given workspace roots so the agent always reads up-to-date code.
func pullWorkspaceRepos(ctx context.Context, roots []string, interval time.Duration) {
	pullAll := func() {
		for _, dir := range discoverWorkspaceRepos(roots) {
			name := filepath.Base(dir)
			fetchCmd := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "--prune", "--no-write-fetch-head", "origin")
			fetchCmd.Env = gitFetchEnv()
			if out, err := fetchCmd.CombinedOutput(); err != nil {
				log.Printf("workspace fetch %s: %s", name, strings.TrimSpace(string(out)))
				continue
			}
			// Fast-forward the local branch to match its upstream tracking branch.
			// This keeps the working tree current so code-search/code-read_file
			// return up-to-date results without the agent needing to use ref tools.
			mergeCmd := exec.CommandContext(ctx, "git", "-C", dir, "merge", "--ff-only", "@{u}")
			mergeCmd.Env = gitFetchEnv()
			out, mergeErr := mergeCmd.CombinedOutput()
			msg := strings.TrimSpace(string(out))
			if mergeErr != nil {
				log.Printf("workspace fetch %s: fetched ok, ff-merge failed: %s", name, msg)
			} else if msg == "Already up to date." || msg == "Already up-to-date." {
				log.Printf("workspace fetch %s: ok (up to date)", name)
			} else {
				log.Printf("workspace fetch %s: ok, fast-forwarded (%s)", name, firstLine(msg))
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

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}
