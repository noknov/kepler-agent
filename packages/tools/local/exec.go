package localtools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

type Exec struct {
	Sandbox local.Sandbox
}

func (Exec) Descriptor() tool.Descriptor {
	return tool.Descriptor{
		Name: "exec", Description: "Run a command in the workspace sandbox. Prefer a shell command string; argv is also accepted. Network is denied unless requested and approved.",
		InputSchema: schema(`{"command":{"type":"string"},"argv":{"type":"array","items":{"type":"string"},"minItems":1},"workdir":{"type":"string"},"network":{"type":"boolean"},"timeout_seconds":{"type":"integer","minimum":1,"maximum":1800}}`),
		Effects:     []tool.Effect{tool.EffectWorkspaceWrite, tool.EffectNetwork}, Exposure: tool.ExposureEager, Timeout: 30 * time.Minute,
	}
}

func (t Exec) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var arguments struct {
		Command string   `json:"command"`
		Argv    []string `json:"argv"`
		Workdir string   `json:"workdir"`
		Network bool     `json:"network"`
		Timeout int      `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return tool.Result{}, err
	}
	argv, err := execArgv(arguments.Command, arguments.Argv)
	if err != nil {
		return tool.Result{}, err
	}
	if arguments.Timeout <= 0 {
		arguments.Timeout = 120
	}
	if arguments.Timeout > 1800 {
		arguments.Timeout = 1800
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(arguments.Timeout)*time.Second)
	defer cancel()
	result, err := t.Sandbox.Run(runCtx, local.CommandRequest{Argv: argv, Workdir: arguments.Workdir, Network: arguments.Network})
	if err != nil {
		return tool.Result{}, err
	}
	value := result.Output
	if value == "" {
		value = fmt.Sprintf("Command exited with status %d.", result.ExitCode)
	}
	return tool.Result{Content: tool.TextResult(value).Content, IsError: result.ExitCode != 0, ErrorCode: exitCode(result.ExitCode), Truncated: result.Truncated, Metadata: map[string]any{"exit_code": result.ExitCode}}, nil
}

func execArgv(command string, argv []string) ([]string, error) {
	command = strings.TrimSpace(command)
	if command != "" && len(argv) > 0 {
		return nil, fmt.Errorf("provide command or argv, not both")
	}
	if command != "" {
		return []string{"/bin/bash", "-lc", command}, nil
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, fmt.Errorf("command or argv is required")
	}
	return append([]string(nil), argv...), nil
}

func exitCode(code int) string {
	if code == 0 {
		return ""
	}
	return fmt.Sprintf("exit_%d", code)
}
