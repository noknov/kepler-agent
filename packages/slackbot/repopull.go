package slackbot

import (
	"context"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/appsupport"
)

func pullWorkspaceRepos(ctx context.Context, roots []string, interval time.Duration) {
	appsupport.PullWorkspaceRepos(ctx, roots, interval)
}

func discoverWorkspaceRepos(roots []string) []string {
	return appsupport.DiscoverWorkspaceRepos(roots)
}
