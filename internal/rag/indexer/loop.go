package indexer

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

// Loop periodically indexes all repos under the given workspace roots.
type Loop struct {
	Indexer  *Indexer
	Roots    []string
	Interval time.Duration
	Observer Observer
}

type Observer interface {
	RAGIndexSuccess(repo, branch, commit string, filesChanged, chunksAdded, chunksReused, chunksSplitLarge, chunksSkippedLarge int, d time.Duration)
	RAGIndexError(repo, branch string, d time.Duration, err error)
}

func (l *Loop) Run(ctx context.Context) {
	if l.Interval <= 0 {
		l.Interval = 5 * time.Minute
	}

	log.Printf("rag/indexer: starting index loop, interval=%s, roots=%v", l.Interval, l.Roots)

	l.indexAll(ctx)

	ticker := time.NewTicker(l.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("rag/indexer: loop stopped")
			return
		case <-ticker.C:
			l.indexAll(ctx)
		}
	}
}

func (l *Loop) indexAll(ctx context.Context) {
	for _, root := range l.Roots {
		repos := discoverRepos(root)
		for _, repo := range repos {
			branch := detectDefaultBranch(ctx, repo)
			if branch == "" {
				continue
			}

			start := time.Now()
			if err := fetchOrigin(ctx, repo); err != nil {
				if l.Observer != nil {
					l.Observer.RAGIndexError(repo, branch, time.Since(start), err)
				}
				log.Printf("rag/indexer: fetch %s: %v", repo, err)
				continue
			}

			result, err := l.Indexer.IndexRepo(ctx, repo, branch)
			if err != nil {
				if l.Observer != nil {
					l.Observer.RAGIndexError(repo, branch, time.Since(start), err)
				}
				log.Printf("rag/indexer: failed to index %s@%s: %v", repo, branch, err)
				continue
			}
			if l.Observer != nil {
				l.Observer.RAGIndexSuccess(repo, branch, result.CommitSHA, result.FilesChanged, result.ChunksAdded, result.ChunksReused, result.ChunksSplitLarge, result.ChunksSkippedLarge, result.Duration)
			}
			if result.FilesChanged > 0 {
				log.Printf("rag/indexer: indexed %s@%s: %d files, %d chunks, %d reused, %d split_large, %d skipped_large in %s",
					repo, branch, result.FilesChanged, result.ChunksAdded, result.ChunksReused, result.ChunksSplitLarge, result.ChunksSkippedLarge, result.Duration)
			}
		}
	}
}

func discoverRepos(root string) []string {
	cmd := exec.Command("find", root, "-maxdepth", "2", "-name", ".git", "-type", "d")
	out, err := cmd.Output()
	if err != nil {
		log.Printf("rag/indexer: discover repos under %s: %v", root, err)
		return nil
	}
	var repos []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		repo := strings.TrimSuffix(line, "/.git")
		repos = append(repos, repo)
	}
	return repos
}

func detectDefaultBranch(ctx context.Context, repo string) string {
	for _, branch := range []string{"mt-main", "main", "master"} {
		if _, err := resolveBranchRef(ctx, repo, branch); err == nil {
			return branch
		}
	}
	return ""
}

func fetchOrigin(ctx context.Context, repo string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		attemptCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		_, err := gitRun(attemptCtx, repo, "fetch", "--prune", "--no-write-fetch-head", "origin")
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransientGitFetchError(err) {
			break
		}
	}
	return fmt.Errorf("fetch origin: %w", lastErr)
}

func isTransientGitFetchError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"early eof",
		"connection reset",
		"connection timed out",
		"timeout",
		"tls handshake timeout",
		"unexpected disconnect",
		"the remote end hung up unexpectedly",
		"could not read from remote repository",
		"ssh_exchange_identification",
		"connection refused",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
