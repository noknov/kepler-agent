package benchkit

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type Agent interface {
	RunCase(ctx context.Context, c Case) (AgentResult, error)
}

type EchoAgent struct{}

func (EchoAgent) RunCase(_ context.Context, c Case) (AgentResult, error) {
	return AgentResult{Output: c.Prompt}, nil
}

type CommandAgent struct {
	Command []string
}

func (a CommandAgent) RunCase(ctx context.Context, c Case) (AgentResult, error) {
	if len(a.Command) == 0 {
		return AgentResult{}, fmt.Errorf("agent command is required")
	}
	args := make([]string, 0, len(a.Command)-1)
	for _, arg := range a.Command[1:] {
		args = append(args, expandTemplate(arg, c))
	}
	cmd := exec.CommandContext(ctx, a.Command[0], args...)
	if c.Workspace != "" {
		cmd.Dir = c.Workspace
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return AgentResult{Output: string(out)}, fmt.Errorf("agent command failed: %w", err)
	}
	return AgentResult{Output: string(out)}, nil
}

func expandTemplate(s string, c Case) string {
	s = strings.ReplaceAll(s, "{{prompt}}", c.Prompt)
	s = strings.ReplaceAll(s, "{{id}}", c.ID)
	s = strings.ReplaceAll(s, "{{workspace}}", c.Workspace)
	return s
}
