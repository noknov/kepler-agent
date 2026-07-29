package benchcli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/benchmarks/benchkit"
	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/observability"
	appruntime "github.com/noknov/slack-copilot-agent/packages/runtime"
	"github.com/noknov/slack-copilot-agent/packages/safety"
)

func Run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		PrintUsage()
		return nil
	}
	var suite benchkit.Suite
	var err error
	switch args[0] {
	case "builtin":
		suite = benchkit.BuiltinSuite()
		args = args[1:]
	case "run":
		if len(args) < 2 {
			return fmt.Errorf("bench run requires a suite path")
		}
		suite, err = benchkit.LoadSuite(args[1])
		if err != nil {
			return err
		}
		args = args[2:]
	case "humaneval":
		return runHumanEval(ctx, args[1:])
	case "terminal-bench":
		return runTerminalBench(ctx, args[1:])
	case "swe-bench":
		return runSWEBench(ctx, args[1:])
	case "livecodebench":
		return runLiveCodeBench(ctx, args[1:])
	default:
		return fmt.Errorf("unknown benchmark command %q", args[0])
	}
	benchOpts, agentArgs, err := parseBenchOptions(args)
	if err != nil {
		return err
	}
	agent, err := benchAgent(benchOpts, agentArgs)
	if err != nil {
		return err
	}
	out, closeOut, err := benchOutput(benchOpts.OutputPath)
	if err != nil {
		return err
	}
	defer closeOut()
	summary, _, err := benchkit.RunSuite(ctx, suite, agent, out, benchOpts)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "summary: passed=%d/%d score=%.3f duration_ms=%d\n", summary.Passed, summary.Total, summary.Score, summary.DurationMillis)
	return nil
}

func PrintUsage() {
	fmt.Println(`slack-copilot-bench - Slack Copilot Agent benchmark runner

Usage:
  slack-copilot-bench builtin [--self] [--workspace-root DIR] [--keep-workspaces] [--output results.jsonl] [-- <agent command...>]
  slack-copilot-bench run suite.json [--self] [--workspace-root DIR] [--keep-workspaces] [--output results.jsonl] [-- <agent command...>]
  slack-copilot-bench humaneval smoke HumanEval.jsonl.gz [--self]
  slack-copilot-bench humaneval full HumanEval.jsonl.gz [--self]
  slack-copilot-bench terminal-bench smoke [--task-id hello-world]
  slack-copilot-bench terminal-bench full
  slack-copilot-bench swe-bench lite-eval --predictions predictions.jsonl
  slack-copilot-bench swe-bench lite-full --predictions predictions.jsonl
  slack-copilot-bench livecodebench smoke
  slack-copilot-bench livecodebench full

Agent command arguments can use {{prompt}}, {{id}}, and {{workspace}} templates.
Without --self or an agent command, the benchmark harness runs with an echo agent for smoke testing.`)
}

func runHumanEval(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Println(`Usage:
  slack-copilot-bench humaneval smoke HumanEval.jsonl.gz [--self]
  slack-copilot-bench humaneval full HumanEval.jsonl.gz [--self]
  slack-copilot-bench humaneval HumanEval.jsonl.gz [--limit N] [--self]`)
		return nil
	}
	limit := 0
	switch args[0] {
	case "smoke":
		if len(args) < 2 {
			return fmt.Errorf("humaneval smoke requires a HumanEval.jsonl or HumanEval.jsonl.gz path")
		}
		limit = 5
		args = args[1:]
	case "full":
		if len(args) < 2 {
			return fmt.Errorf("humaneval full requires a HumanEval.jsonl or HumanEval.jsonl.gz path")
		}
		args = args[1:]
	}
	if len(args) == 0 {
		return fmt.Errorf("humaneval requires a HumanEval.jsonl or HumanEval.jsonl.gz path")
	}
	path := args[0]
	opts, agentArgs, err := parseBenchOptions(args[1:])
	if err != nil {
		return err
	}
	if limit > 0 && opts.Limit == 0 {
		opts.Limit = limit
	}
	suite, err := benchkit.LoadHumanEvalSuite(path, benchkit.HumanEvalOptions{Limit: opts.Limit})
	if err != nil {
		return err
	}
	agent, err := benchAgent(opts, agentArgs)
	if err != nil {
		return err
	}
	out, closeOut, err := benchOutput(opts.OutputPath)
	if err != nil {
		return err
	}
	defer closeOut()
	summary, _, err := benchkit.RunSuite(ctx, suite, agent, out, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "summary: passed=%d/%d score=%.3f duration_ms=%d\n", summary.Passed, summary.Total, summary.Score, summary.DurationMillis)
	return nil
}

func runExternal(ctx context.Context, name string, args []string) error {
	fmt.Fprintf(os.Stderr, "running: %s\n", formatCommand(name, args))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func formatCommand(name string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, name)
	for _, arg := range args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

func parseBenchOptions(args []string) (benchkit.RunOptions, []string, error) {
	var opts benchkit.RunOptions
	for len(args) > 0 {
		switch args[0] {
		case "--":
			return opts, args, nil
		case "--self":
			opts.Agent = "self"
			args = args[1:]
		case "--keep-workspaces":
			opts.KeepWorkspaces = true
			args = args[1:]
		case "--output":
			if len(args) < 2 {
				return opts, nil, fmt.Errorf("--output requires a file path")
			}
			opts.OutputPath = args[1]
			args = args[2:]
		case "--timeout-seconds":
			if len(args) < 2 {
				return opts, nil, fmt.Errorf("--timeout-seconds requires a value")
			}
			seconds, err := strconv.Atoi(args[1])
			if err != nil || seconds <= 0 {
				return opts, nil, fmt.Errorf("--timeout-seconds must be a positive integer")
			}
			opts.Timeout = time.Duration(seconds) * time.Second
			args = args[2:]
		case "--max-patch-bytes":
			if len(args) < 2 {
				return opts, nil, fmt.Errorf("--max-patch-bytes requires a value")
			}
			n, err := strconv.Atoi(args[1])
			if err != nil || n <= 0 {
				return opts, nil, fmt.Errorf("--max-patch-bytes must be a positive integer")
			}
			opts.MaxPatchBytes = n
			args = args[2:]
		case "--limit":
			if len(args) < 2 {
				return opts, nil, fmt.Errorf("--limit requires a value")
			}
			n, err := strconv.Atoi(args[1])
			if err != nil || n <= 0 {
				return opts, nil, fmt.Errorf("--limit must be a positive integer")
			}
			opts.Limit = n
			args = args[2:]
		case "--workspace-root":
			if len(args) < 2 {
				return opts, nil, fmt.Errorf("--workspace-root requires a directory")
			}
			opts.WorkspaceRoot = args[1]
			args = args[2:]
		default:
			return opts, args, nil
		}
	}
	return opts, args, nil
}

func benchOutput(path string) (io.Writer, func(), error) {
	if path == "" || path == "-" {
		return os.Stdout, func() {}, nil
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, func() {}, err
	}
	return file, func() { _ = file.Close() }, nil
}

func benchAgent(opts benchkit.RunOptions, args []string) (benchkit.Agent, error) {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if opts.Agent == "self" {
		cfg, err := config.LoadLocalAgent()
		if err != nil {
			return nil, err
		}
		return localAgent{cfg: cfg}, nil
	}
	if len(args) == 0 {
		return benchkit.EchoAgent{}, nil
	}
	return benchkit.CommandAgent{Command: args}, nil
}

type localAgent struct {
	cfg config.Config
}

func (a localAgent) RunCase(ctx context.Context, c benchkit.Case) (benchkit.AgentResult, error) {
	cfg := a.cfg
	if c.Workspace != "" {
		cfg.Security.WorkspaceRoots = []string{c.Workspace}
	}
	recorder := observability.NewRecorder()
	rt := appruntime.NewAgentRuntime(cfg, nil, nil, recorder, nil)
	workspacePolicy := safety.WorkspacePolicy{Roots: cfg.Security.WorkspaceRoots}
	commandPolicy := safety.NewCommandPolicy()
	tools := appruntime.NewCodingToolRegistry(cfg, rt.Runner.LLM, nil, "", workspacePolicy, commandPolicy)
	rt.Tools = tools
	rt.Runner.Tools = tools
	return benchkit.RunnerAgent{Runner: rt.Runner}.RunCase(ctx, c)
}
