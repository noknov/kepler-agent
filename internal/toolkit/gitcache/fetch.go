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
	inFlight  bool
	done      chan struct{}
}

var (
	mu      sync.Mutex
	entries = map[string]fetchEntry{}
)

// FetchOrigin refreshes origin refs at most once per TTL for each repo path.
// It is process-wide so separate Slack requests do not all pay a network fetch
// before answering a simple code question.
func FetchOrigin(ctx context.Context, repoDir string, ttl time.Duration) error {
	return fetchOrigin(ctx, repoDir, ttl, false)
}

// FetchOriginFresh refreshes origin refs without using the success TTL. It is
// useful when the user explicitly asks about a named branch's latest state.
func FetchOriginFresh(ctx context.Context, repoDir string) error {
	return fetchOrigin(ctx, repoDir, DefaultFetchTTL, true)
}

func fetchOrigin(ctx context.Context, repoDir string, ttl time.Duration, force bool) error {
	if ttl <= 0 {
		ttl = DefaultFetchTTL
	}
	requestStarted := time.Now()
	for {
		now := time.Now()
		mu.Lock()
		entry, ok := entries[repoDir]
		if ok && entry.inFlight {
			done := entry.done
			mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-done:
				continue
			}
		}
		if ok && !force {
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
		if ok && force && !entry.lastFetch.Before(requestStarted) {
			err := entry.err
			mu.Unlock()
			return err
		}
		done := make(chan struct{})
		entries[repoDir] = fetchEntry{lastFetch: entry.lastFetch, err: entry.err, inFlight: true, done: done}
		mu.Unlock()
		err := runFetch(ctx, repoDir)
		mu.Lock()
		entries[repoDir] = fetchEntry{lastFetch: time.Now(), err: err}
		close(done)
		mu.Unlock()
		return err
	}
}

func runFetch(ctx context.Context, repoDir string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "fetch", "--prune", "--force", "--no-write-fetch-head", "origin")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fetchError(strings.TrimSpace(string(out)))
	}
	// Keep refs/remotes/origin/HEAD aligned with the remote default branch.
	// The fetch updates origin/* refs but does not necessarily refresh this
	// symbolic ref, so default-branch tools could otherwise keep reading an
	// old main/master after the remote moves to mt-main.
	headCmd := exec.CommandContext(ctx, "git", "-C", repoDir, "remote", "set-head", "origin", "-a")
	headCmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	_ = headCmd.Run()
	return nil
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
