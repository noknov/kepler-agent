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

// pullWorkspaceRepos periodically runs "git fetch origin" for each sub-repo
// under the given workspace roots.  The fetch keeps remote tracking refs
// (origin/*) current in the local .git/objects store so that code-search,
// code-read_file, repo-search, and git-read_file_ref can all read the latest
// committed code via "git show <ref>:<path>" without ever touching the working
// tree.  We intentionally do NOT run "git merge" or "git checkout" here:
// modifying the working tree is unsafe when concurrent users may be targeting
// different branches through their own tool calls.
func pullWorkspaceRepos(ctx context.Context, roots []string, interval time.Duration) {
	pullAll := func() {
		for _, dir := range discoverWorkspaceRepos(roots) {
			name := filepath.Base(dir)
			cmd := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "--prune", "--force", "--no-write-fetch-head", "origin")
			cmd.Env = gitFetchEnv()
			out, err := cmd.CombinedOutput()
			if err != nil {
				log.Printf("workspace fetch %s: %s", name, strings.TrimSpace(string(out)))
			} else {
				log.Printf("workspace fetch %s: ok", name)
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
