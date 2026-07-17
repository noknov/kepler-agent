package registry

import "strings"

// PRContext is the session-local source of truth for a pull-request review.
// Tool output is deliberately not used as the transport: large diffs may be
// compacted or spilled before the next model turn.
type PRContext struct {
	Repository   string
	RepoPath     string // workspace-relative local repository directory
	Number       int
	HeadRef      string
	HeadSHA      string
	BaseRef      string
	ChangedFiles []string
	Diff         string // kept in the run cache for github-pr_file_diff only
}

const prContextCacheKey = "pull-request-context"

func SetPRContext(rt Runtime, ctx PRContext) {
	if rt.Cache == nil {
		return
	}
	ctx.Repository = strings.TrimSpace(ctx.Repository)
	ctx.RepoPath = strings.TrimSpace(ctx.RepoPath)
	ctx.HeadRef = strings.TrimSpace(ctx.HeadRef)
	ctx.HeadSHA = strings.TrimSpace(ctx.HeadSHA)
	ctx.BaseRef = strings.TrimSpace(ctx.BaseRef)
	rt.Cache.Set(prContextCacheKey, ctx)
}

func PRContextFromRuntime(rt Runtime) (PRContext, bool) {
	if rt.Cache == nil {
		return PRContext{}, false
	}
	v, ok := rt.Cache.Get(prContextCacheKey)
	if !ok {
		return PRContext{}, false
	}
	ctx, ok := v.(PRContext)
	return ctx, ok && ctx.Repository != "" && ctx.HeadSHA != "" && ctx.RepoPath != ""
}

func (p PRContext) ContainsPath(path string) bool {
	path = strings.Trim(strings.TrimSpace(path), "/")
	for _, changed := range p.ChangedFiles {
		if path == strings.Trim(changed, "/") {
			return true
		}
	}
	return false
}
