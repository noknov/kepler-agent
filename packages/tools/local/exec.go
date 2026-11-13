package localtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

type Exec struct{ Sandbox local.Sandbox }

type execArguments struct {
	Argv    []string `json:"argv"`
	Workdir string   `json:"workdir"`
	Network bool     `json:"network"`
	Timeout *int     `json:"timeout_seconds"`
}

func (Exec) Descriptor() tool.Descriptor {
	return tool.Descriptor{
		Name: "exec", Description: "Run one argv command directly in the workspace sandbox without a shell. Pipelines and shell operators are not interpreted. Network is denied unless requested and approved.",
		InputSchema: schema(`{"argv":{"type":"array","items":{"type":"string"},"minItems":1},"workdir":{"type":"string"},"network":{"type":"boolean"},"timeout_seconds":{"type":"integer","minimum":1,"maximum":1800}}`),
		Effects:     []tool.Effect{tool.EffectWorkspaceWrite, tool.EffectNetwork}, Exposure: tool.ExposureEager, Timeout: 30 * time.Minute,
	}
}

func (t Exec) DescriptorForCall(call tool.Call) (tool.Descriptor, error) {
	args, err := parseExecArguments(call.Arguments)
	if err != nil {
		return tool.Descriptor{}, err
	}
	descriptor := t.Descriptor()
	descriptor.Effects = []tool.Effect{tool.EffectWorkspaceWrite}
	if args.Network {
		descriptor.Effects = append(descriptor.Effects, tool.EffectNetwork)
	}
	return descriptor, nil
}

func (t Exec) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	args, err := parseExecArguments(call.Arguments)
	if err != nil {
		return tool.Result{}, err
	}
	timeout := 120
	if args.Timeout != nil {
		timeout = *args.Timeout
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	command, runErr := t.Sandbox.Run(runCtx, local.CommandRequest{Argv: args.Argv, Workdir: args.Workdir, Network: args.Network})
	result := commandToolResult(command, runErr)
	return result, runErr
}

func parseExecArguments(raw json.RawMessage) (execArguments, error) {
	var args execArguments
	if err := json.Unmarshal(raw, &args); err != nil {
		return args, err
	}
	argv, err := execArgv(args.Argv)
	if err != nil {
		return args, err
	}
	args.Argv = argv
	if args.Timeout != nil && (*args.Timeout < 1 || *args.Timeout > 1800) {
		return args, fmt.Errorf("timeout_seconds must be between 1 and 1800")
	}
	return args, nil
}

func commandToolResult(command local.CommandResult, err error) tool.Result {
	code := exitCode(command.ExitCode)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code = "command_timeout"
	case errors.Is(err, context.Canceled):
		code = "command_canceled"
	case err != nil:
		code = "command_failed"
	}
	value := command.Output
	if value == "" {
		if err != nil {
			value = err.Error()
		} else {
			value = fmt.Sprintf("Command exited with status %d.", command.ExitCode)
		}
	}
	var metadata map[string]any
	if err == nil {
		metadata = map[string]any{"exit_code": command.ExitCode}
	}
	return tool.Result{Content: tool.TextResult(value).Content, IsError: err != nil || command.ExitCode != 0, ErrorCode: code, Truncated: command.Truncated, Metadata: metadata}
}

func execArgv(argv []string) ([]string, error) {
	if len(argv) == 0 || argv[0] == "" {
		return nil, fmt.Errorf("argv is required")
	}
	for _, arg := range argv {
		if strings.ContainsRune(arg, 0) {
			return nil, fmt.Errorf("argv must not contain NUL bytes")
		}
	}
	return append([]string(nil), argv...), nil
}

func exitCode(code int) string {
	if code == 0 {
		return ""
	}
	return fmt.Sprintf("exit_%d", code)
}
