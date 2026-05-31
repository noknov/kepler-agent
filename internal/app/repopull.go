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
func pullWorkspaceRepos(ctx context.Context, roots []string, githubToken string, interval time.Duration) {
	askpass, cleanup := gitAskPass(githubToken)
	defer cleanup()

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
				cmd.Env = gitFetchEnv(askpass, githubToken)
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

func gitFetchEnv(askpass, githubToken string) []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if askpass == "" || strings.TrimSpace(githubToken) == "" {
		return env
	}
	// Avoid global url.insteadOf rules that may embed stale tokens. This is
	// scoped to the fetch subprocess only; normal user git commands are untouched.
	env = append(env,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_ASKPASS="+askpass,
		"ONCALL_AGENT_GITHUB_TOKEN="+githubToken,
	)
	return env
}

func gitAskPass(githubToken string) (string, func()) {
	if strings.TrimSpace(githubToken) == "" {
		return "", func() {}
	}
	file, err := os.CreateTemp("", "oncall-agent-git-askpass-*")
	if err != nil {
		log.Printf("workspace pull: cannot create git askpass helper: %v", err)
		return "", func() {}
	}
	path := file.Name()
	script := `#!/bin/sh
case "$1" in
  *Username*) printf '%s\n' "x-access-token" ;;
  *) printf '%s\n' "$ONCALL_AGENT_GITHUB_TOKEN" ;;
esac
`
	if _, err := file.WriteString(script); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		log.Printf("workspace pull: cannot write git askpass helper: %v", err)
		return "", func() {}
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		log.Printf("workspace pull: cannot close git askpass helper: %v", err)
		return "", func() {}
	}
	if err := os.Chmod(path, 0o700); err != nil {
		_ = os.Remove(path)
		log.Printf("workspace pull: cannot chmod git askpass helper: %v", err)
		return "", func() {}
	}
	return path, func() { _ = os.Remove(path) }
}
