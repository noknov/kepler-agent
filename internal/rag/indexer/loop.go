package indexer

import (
	"context"
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
	RAGIndexSuccess(repo, branch, commit string, filesChanged, chunksAdded, chunksReused int, d time.Duration)
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

			fetchOrigin(ctx, repo)

			start := time.Now()
			result, err := l.Indexer.IndexRepo(ctx, repo, branch)
			if err != nil {
				if l.Observer != nil {
					l.Observer.RAGIndexError(repo, branch, time.Since(start), err)
				}
				log.Printf("rag/indexer: failed to index %s@%s: %v", repo, branch, err)
				continue
			}
			if l.Observer != nil {
				l.Observer.RAGIndexSuccess(repo, branch, result.CommitSHA, result.FilesChanged, result.ChunksAdded, result.ChunksReused, result.Duration)
			}
			if result.FilesChanged > 0 {
				log.Printf("rag/indexer: indexed %s@%s: %d files, %d chunks, %d reused in %s",
					repo, branch, result.FilesChanged, result.ChunksAdded, result.ChunksReused, result.Duration)
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
	for _, branch := range []string{"main", "master"} {
		if _, err := resolveBranchRef(ctx, repo, branch); err == nil {
			return branch
		}
	}
	return ""
}

func fetchOrigin(ctx context.Context, repo string) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if _, err := gitRun(ctx, repo, "fetch", "--prune", "--no-write-fetch-head", "origin"); err != nil {
		log.Printf("rag/indexer: fetch %s: %v", repo, err)
	}
}
