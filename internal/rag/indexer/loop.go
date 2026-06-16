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
			if err := fetchOriginBranch(ctx, repo, branch); err != nil {
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

func fetchOriginBranch(ctx context.Context, repo, branch string) error {
	refspec, err := originBranchRefspec(branch)
	if err != nil {
		return err
	}
	err = retryTransientGitFetch(ctx, func(ctx context.Context) error {
		_, err := gitRun(ctx, repo, "fetch", "--prune", "--no-write-fetch-head", "origin", refspec)
		return err
	})
	if err != nil {
		return fmt.Errorf("fetch origin/%s: %w", branch, err)
	}
	return nil
}

func retryTransientGitFetch(ctx context.Context, run func(context.Context) error) error {
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
		err := run(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransientGitFetchError(err) {
			break
		}
	}
	return lastErr
}

func originBranchRefspec(branch string) (string, error) {
	if strings.TrimSpace(branch) == "" || strings.ContainsAny(branch, " \t\r\n:") {
		return "", fmt.Errorf("invalid branch %q", branch)
	}
	return "+refs/heads/" + branch + ":refs/remotes/origin/" + branch, nil
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
