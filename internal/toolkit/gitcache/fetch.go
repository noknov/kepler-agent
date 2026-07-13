package gitcache

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	DefaultFetchTTL = 5 * time.Minute
	failureTTL      = 30 * time.Second
)

type fetchEntry struct {
	lastFetch time.Time
	err       error
}

var (
	mu      sync.Mutex
	entries = map[string]fetchEntry{}
)

// FetchOrigin refreshes origin refs at most once per TTL for each repo path.
// It is process-wide so separate Slack requests do not all pay a network fetch
// before answering a simple code question.
func FetchOrigin(ctx context.Context, repoDir string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = DefaultFetchTTL
	}
	now := time.Now()
	mu.Lock()
	if entry, ok := entries[repoDir]; ok {
		age := now.Sub(entry.lastFetch)
		if entry.err == nil && age < ttl {
			mu.Unlock()
			return nil
		}
		if entry.err != nil && age < failureTTL {
			err := entry.err
			mu.Unlock()
			return err
		}
	}
	entries[repoDir] = fetchEntry{lastFetch: now}
	mu.Unlock()

	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "fetch", "--prune", "--force", "--no-write-fetch-head", "origin")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		err = fetchError(strings.TrimSpace(string(out)))
	} else {
		// Keep refs/remotes/origin/HEAD aligned with the remote default branch.
		// The fetch updates origin/* refs but does not necessarily refresh this
		// symbolic ref, so default-branch tools could otherwise keep reading an
		// old main/master after the remote moves to mt-main.
		headCmd := exec.CommandContext(ctx, "git", "-C", repoDir, "remote", "set-head", "origin", "-a")
		headCmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		_ = headCmd.Run()
	}

	mu.Lock()
	entries[repoDir] = fetchEntry{lastFetch: now, err: err}
	mu.Unlock()
	return err
}

func ResetForTest() {
	mu.Lock()
	defer mu.Unlock()
	entries = map[string]fetchEntry{}
}

type fetchError string

func (e fetchError) Error() string {
	if e == "" {
		return "git fetch failed"
	}
	return "git fetch failed: " + string(e)
}
