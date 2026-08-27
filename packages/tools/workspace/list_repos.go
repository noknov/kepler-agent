package workspace

import (
	"context"
	"fmt"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

type ListReposTool struct {
	Roots []string
}

func (t ListReposTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"workspace-list_repos",
		"List git repositories discovered under configured workspace roots with a coarse technology stack guess.",
		tool.ObjectSchema(nil, nil),
	)
}

func (t ListReposTool) Execute(context.Context, tool.Call) (tool.Result, error) {
	repos := DiscoverRepositories(t.Roots)
	if len(repos) == 0 {
		return tool.TextResult("No git repositories were discovered under the configured workspace roots."), nil
	}
	lines := make([]string, 0, len(repos)+1)
	lines = append(lines, "Available code repositories. Use the repo name as the path prefix for repository tools:")
	for _, repo := range repos {
		lines = append(lines, fmt.Sprintf("- %s (%s)", repo.Name, repo.Stack))
	}
	return tool.TextResult(strings.Join(lines, "\n")), nil
}
