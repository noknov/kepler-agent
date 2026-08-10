// Package hosted provides the server-owned runtime profile. Slack, HTTP, and
// future surfaces adapt into this profile; they are not separate agent types.
package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/local"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/localtools"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/prompt"
	agentruntime "github.com/noknov/slack-copilot-agent/packages/agentv2/runtime"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/tool"
)

// Policy is authoritative and non-interactive. Hosted users never approve
// host capabilities through a chat surface.
type Policy struct{ Allowed map[string]bool }

func (p Policy) Decide(_ context.Context, request tool.PolicyRequest) (tool.Decision, error) {
	if p.Allowed != nil && !p.Allowed[request.Call.Name] {
		return tool.Decision{Type: tool.DecisionDeny, Reason: "tool is not in the operator allowlist"}, nil
	}
	for _, effect := range request.Descriptor.Effects {
		if effect != tool.EffectRead {
			return tool.Decision{Type: tool.DecisionDeny, Reason: "hosted profile is read-only"}, nil
		}
	}
	return tool.Decision{Type: tool.DecisionAllow}, nil
}

type ArgvRequest struct {
	Argv    []string
	Workdir string
}
type ArgvResult struct {
	Output   string
	ExitCode int
}
type ArgvExecutor interface {
	Execute(context.Context, ArgvRequest) (ArgvResult, error)
}

type Exec struct {
	Workspace local.Workspace
	Executor  ArgvExecutor
	Commands  map[string]bool
}

func (Exec) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "exec", Description: "Run one operator-allowlisted argv command without a shell.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"argv":{"type":"array","items":{"type":"string"},"minItems":1},"workdir":{"type":"string"}},"required":["argv"]}`), Effects: []tool.Effect{tool.EffectRead}, Exposure: tool.ExposureDeferred}
}
func (t Exec) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var arguments struct {
		Argv    []string `json:"argv"`
		Workdir string   `json:"workdir"`
	}
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return tool.Result{}, err
	}
	if len(arguments.Argv) == 0 || !t.Commands[arguments.Argv[0]] {
		return tool.Result{}, fmt.Errorf("command is not allowlisted")
	}
	if t.Executor == nil {
		return tool.Result{}, fmt.Errorf("hosted argv executor is not configured")
	}
	workdir := t.Workspace.Root
	if arguments.Workdir != "" {
		var err error
		workdir, err = t.Workspace.Resolve(arguments.Workdir, false)
		if err != nil {
			return tool.Result{}, err
		}
	}
	result, err := t.Executor.Execute(ctx, ArgvRequest{Argv: append([]string(nil), arguments.Argv...), Workdir: workdir})
	if err != nil {
		return tool.Result{}, err
	}
	errorCode := ""
	if result.ExitCode != 0 {
		errorCode = fmt.Sprintf("exit_%d", result.ExitCode)
	}
	return tool.Result{Content: tool.TextResult(result.Output).Content, IsError: result.ExitCode != 0, ErrorCode: errorCode, Metadata: map[string]any{"exit_code": result.ExitCode}}, nil
}

func NewCatalog(workspace local.Workspace, executor ArgvExecutor, commands []string) (*tool.Catalog, error) {
	allowed := make(map[string]bool, len(commands))
	for _, command := range commands {
		allowed[command] = true
	}
	catalog, err := tool.NewCatalog(localtools.ReadFile{Workspace: workspace}, localtools.ListFiles{Workspace: workspace}, localtools.Search{Workspace: workspace}, Exec{Workspace: workspace, Executor: executor, Commands: allowed})
	if err != nil {
		return nil, err
	}
	if err := catalog.Register(localtools.ToolSearch{Catalog: catalog}); err != nil {
		return nil, err
	}
	return catalog, nil
}

type Agent struct {
	Runtime *agentruntime.Runtime
	Prompt  []prompt.Fragment
}
type Request struct {
	SessionID, TurnID, UserID, Workspace, Text string
	Steering                                   agentruntime.InputSource
	Prompt                                     []prompt.Fragment
}

func (a Agent) Run(ctx context.Context, request Request) (agentruntime.TurnResult, error) {
	if a.Runtime == nil {
		return agentruntime.TurnResult{}, fmt.Errorf("hosted runtime is not configured")
	}
	if strings.TrimSpace(request.Text) == "" {
		return agentruntime.TurnResult{}, fmt.Errorf("input is empty")
	}
	fragments := append([]prompt.Fragment(nil), a.Prompt...)
	fragments = append(fragments, request.Prompt...)
	return a.Runtime.RunTurn(ctx, agentruntime.TurnRequest{SessionID: request.SessionID, TurnID: request.TurnID, Input: model.TextMessage(model.RoleUser, request.Text), Prompt: fragments, Scope: tool.Scope{SessionID: request.SessionID, TurnID: request.TurnID, UserID: request.UserID, Workspace: request.Workspace}, Steering: request.Steering})
}
