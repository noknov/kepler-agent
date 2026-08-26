package localtools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/profiles/local"
)

type Exec struct {
	Sandbox local.Sandbox
}

func (Exec) Descriptor() tool.Descriptor {
	return tool.Descriptor{
		Name: "exec", Description: "Run one argv command directly in the workspace sandbox without a shell. Network is denied unless explicitly requested and approved.",
		InputSchema: schema(`{"argv":{"type":"array","items":{"type":"string"},"minItems":1},"workdir":{"type":"string"},"network":{"type":"boolean"},"timeout_seconds":{"type":"integer","minimum":1,"maximum":1800}}`, "argv"),
		Effects:     []tool.Effect{tool.EffectWorkspaceWrite, tool.EffectNetwork}, Exposure: tool.ExposureEager, Timeout: 30 * time.Minute,
	}
}

func (t Exec) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var arguments struct {
		Argv    []string `json:"argv"`
		Workdir string   `json:"workdir"`
		Network bool     `json:"network"`
		Timeout int      `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return tool.Result{}, err
	}
	if len(arguments.Argv) == 0 || arguments.Argv[0] == "" {
		return tool.Result{}, fmt.Errorf("argv is required")
	}
	if arguments.Timeout <= 0 {
		arguments.Timeout = 120
	}
	if arguments.Timeout > 1800 {
		arguments.Timeout = 1800
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(arguments.Timeout)*time.Second)
	defer cancel()
	result, err := t.Sandbox.Run(runCtx, local.CommandRequest{Argv: append([]string(nil), arguments.Argv...), Workdir: arguments.Workdir, Network: arguments.Network})
	if err != nil {
		return tool.Result{}, err
	}
	value := result.Output
	if value == "" {
		value = fmt.Sprintf("Command exited with status %d.", result.ExitCode)
	}
	return tool.Result{Content: tool.TextResult(value).Content, IsError: result.ExitCode != 0, ErrorCode: exitCode(result.ExitCode), Truncated: result.Truncated, Metadata: map[string]any{"exit_code": result.ExitCode}}, nil
}

func exitCode(code int) string {
	if code == 0 {
		return ""
	}
	return fmt.Sprintf("exit_%d", code)
}
