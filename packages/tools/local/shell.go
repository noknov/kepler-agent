package localtools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/profiles/local"
)

type Shell struct {
	Sandbox local.Sandbox
}

func (Shell) Descriptor() tool.Descriptor {
	return tool.Descriptor{
		Name: "shell", Description: "Run a shell command in the workspace sandbox. Network is denied unless explicitly requested and approved.",
		InputSchema: schema(`{"command":{"type":"string"},"workdir":{"type":"string"},"network":{"type":"boolean"},"timeout_seconds":{"type":"integer","minimum":1,"maximum":1800}}`, "command"),
		Effects:     []tool.Effect{tool.EffectWorkspaceWrite}, Exposure: tool.ExposureEager, Timeout: 30 * time.Minute,
	}
}

func (t Shell) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var arguments struct {
		Command string `json:"command"`
		Workdir string `json:"workdir"`
		Network bool   `json:"network"`
		Timeout int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return tool.Result{}, err
	}
	if arguments.Command == "" {
		return tool.Result{}, fmt.Errorf("command is required")
	}
	if arguments.Timeout <= 0 {
		arguments.Timeout = 120
	}
	if arguments.Timeout > 1800 {
		arguments.Timeout = 1800
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(arguments.Timeout)*time.Second)
	defer cancel()
	result, err := t.Sandbox.Run(runCtx, local.CommandRequest{Command: arguments.Command, Workdir: arguments.Workdir, Network: arguments.Network})
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
