package benchcli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func runTerminalBench(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Println(`Usage:
  slack-copilot-bench terminal-bench smoke [--task-id hello-world] [--dataset terminal-bench-core==0.1.1] [--agent terminus] [--output-path runs]
  slack-copilot-bench terminal-bench full [--dataset terminal-bench-core==0.1.1] [--agent terminus] [--output-path runs]
  slack-copilot-bench terminal-bench run [official tb args...]
  slack-copilot-bench terminal-bench harbor [official harbor args...]
  slack-copilot-bench terminal-bench -- <tb-or-harbor args...>`)
		return nil
	}
	if args[0] == "--" {
		return runTerminalBenchRaw(ctx, args[1:])
	}
	switch args[0] {
	case "smoke":
		return runTerminalBenchSmoke(ctx, args[1:])
	case "full":
		return runTerminalBenchFull(ctx, args[1:])
	case "run":
		return runTerminalBenchTB(ctx, args[1:])
	case "harbor":
		return runTerminalBenchHarbor(ctx, args[1:])
	default:
		return runTerminalBenchRaw(ctx, args)
	}
}

func runTerminalBenchSmoke(ctx context.Context, args []string) error {
	taskID := "hello-world"
	dataset := "terminal-bench-core==0.1.1"
	agent := "terminus"
	outputPath := "runs"
	extra := []string{}
	for len(args) > 0 {
		switch args[0] {
		case "--task-id":
			if len(args) < 2 {
				return fmt.Errorf("--task-id requires a value")
			}
			taskID = args[1]
			args = args[2:]
		case "--dataset":
			if len(args) < 2 {
				return fmt.Errorf("--dataset requires a value")
			}
			dataset = args[1]
			args = args[2:]
		case "--agent":
			if len(args) < 2 {
				return fmt.Errorf("--agent requires a value")
			}
			agent = args[1]
			args = args[2:]
		case "--output-path":
			if len(args) < 2 {
				return fmt.Errorf("--output-path requires a value")
			}
			outputPath = args[1]
			args = args[2:]
		case "--":
			extra = append(extra, args[1:]...)
			args = nil
		default:
			extra = append(extra, args[0])
			args = args[1:]
		}
	}
	tbArgs := []string{
		"run",
		"--dataset", dataset,
		"--agent", agent,
		"--task-id", taskID,
		"--livestream",
		"--n-concurrent", "1",
		"--output-path", outputPath,
	}
	tbArgs = append(tbArgs, extra...)
	return runTerminalBenchTB(ctx, tbArgs)
}

func runTerminalBenchFull(ctx context.Context, args []string) error {
	dataset := "terminal-bench-core==0.1.1"
	agent := "terminus"
	outputPath := "runs"
	concurrency := "1"
	extra := []string{}
	for len(args) > 0 {
		switch args[0] {
		case "--dataset":
			if len(args) < 2 {
				return fmt.Errorf("--dataset requires a value")
			}
			dataset = args[1]
			args = args[2:]
		case "--agent":
			if len(args) < 2 {
				return fmt.Errorf("--agent requires a value")
			}
			agent = args[1]
			args = args[2:]
		case "--output-path":
			if len(args) < 2 {
				return fmt.Errorf("--output-path requires a value")
			}
			outputPath = args[1]
			args = args[2:]
		case "--n-concurrent":
			if len(args) < 2 {
				return fmt.Errorf("--n-concurrent requires a value")
			}
			concurrency = args[1]
			args = args[2:]
		case "--":
			extra = append(extra, args[1:]...)
			args = nil
		default:
			extra = append(extra, args[0])
			args = args[1:]
		}
	}
	tbArgs := []string{
		"run",
		"--dataset", dataset,
		"--agent", agent,
		"--livestream",
		"--n-concurrent", concurrency,
		"--output-path", outputPath,
	}
	tbArgs = append(tbArgs, extra...)
	return runTerminalBenchTB(ctx, tbArgs)
}

func runTerminalBenchTB(ctx context.Context, args []string) error {
	if _, err := exec.LookPath("tb"); err != nil {
		return fmt.Errorf("tb not found. Install Terminal-Bench with `uv tool install terminal-bench`")
	}
	fmt.Fprintln(os.Stderr, "note: Terminal-Bench can be quiet while downloading datasets or building Docker images.")
	return runExternal(ctx, "tb", args)
}

func runTerminalBenchHarbor(ctx context.Context, args []string) error {
	if _, err := exec.LookPath("harbor"); err != nil {
		return fmt.Errorf("harbor not found. Install Harbor with `uv tool install harbor`")
	}
	return runExternal(ctx, "harbor", args)
}

func runTerminalBenchRaw(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("bench terminal-bench requires a preset or official CLI args")
	}
	binary := "tb"
	if args[0] == "harbor" || args[0] == "tb" {
		binary = args[0]
		args = args[1:]
	}
	if _, err := exec.LookPath(binary); err != nil {
		return fmt.Errorf("%s not found. Install official harness first: terminal-bench via `uv tool install terminal-bench`, or Harbor via `uv tool install harbor`", binary)
	}
	return runExternal(ctx, binary, args)
}

func runLiveCodeBench(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Println(`Usage:
  slack-copilot-bench livecodebench smoke [--model MODEL] [--scenario codegeneration] [--first-n 1]
  slack-copilot-bench livecodebench full [--model MODEL] [--scenario codegeneration]
  slack-copilot-bench livecodebench run [official livecodebench args...]`)
		return nil
	}
	switch args[0] {
	case "smoke":
		return runLiveCodeBenchSmoke(ctx, args[1:])
	case "full":
		return runLiveCodeBenchFull(ctx, args[1:])
	case "run":
		return runLiveCodeBenchRaw(ctx, args[1:])
	default:
		return runLiveCodeBenchRaw(ctx, args)
	}
}

func runLiveCodeBenchSmoke(ctx context.Context, args []string) error {
	model := "openai/gpt-4o-mini"
	scenario := "codegeneration"
	firstN := "1"
	extra := []string{}
	for len(args) > 0 {
		switch args[0] {
		case "--model":
			if len(args) < 2 {
				return fmt.Errorf("--model requires a value")
			}
			model = args[1]
			args = args[2:]
		case "--scenario":
			if len(args) < 2 {
				return fmt.Errorf("--scenario requires a value")
			}
			scenario = args[1]
			args = args[2:]
		case "--first-n":
			if len(args) < 2 {
				return fmt.Errorf("--first-n requires a value")
			}
			firstN = args[1]
			args = args[2:]
		default:
			extra = append(extra, args[0])
			args = args[1:]
		}
	}
	lcbArgs := []string{"--model", model, "--scenario", scenario, "--first_n", firstN, "--evaluate"}
	lcbArgs = append(lcbArgs, extra...)
	return runLiveCodeBenchRaw(ctx, lcbArgs)
}

func runLiveCodeBenchFull(ctx context.Context, args []string) error {
	model := "openai/gpt-4o-mini"
	scenario := "codegeneration"
	extra := []string{}
	for len(args) > 0 {
		switch args[0] {
		case "--model":
			if len(args) < 2 {
				return fmt.Errorf("--model requires a value")
			}
			model = args[1]
			args = args[2:]
		case "--scenario":
			if len(args) < 2 {
				return fmt.Errorf("--scenario requires a value")
			}
			scenario = args[1]
			args = args[2:]
		case "--":
			extra = append(extra, args[1:]...)
			args = nil
		default:
			extra = append(extra, args[0])
			args = args[1:]
		}
	}
	lcbArgs := []string{"--model", model, "--scenario", scenario, "--evaluate"}
	lcbArgs = append(lcbArgs, extra...)
	return runLiveCodeBenchRaw(ctx, lcbArgs)
}

func runLiveCodeBenchRaw(ctx context.Context, args []string) error {
	if _, err := exec.LookPath("livecodebench"); err != nil {
		return fmt.Errorf("livecodebench not found. Install with `uv tool install nvidia-livecodebench`")
	}
	return runExternal(ctx, "livecodebench", args)
}

func runSWEBench(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Println(`Usage:
  slack-copilot-bench swe-bench lite-eval --predictions predictions.jsonl [--max-workers 1]
  slack-copilot-bench swe-bench lite-full --predictions predictions.jsonl [--max-workers 1]
  slack-copilot-bench swe-bench verified-full --predictions predictions.jsonl [--max-workers 1]
  slack-copilot-bench swe-bench eval --predictions predictions.jsonl [--dataset-name SWE-bench/SWE-bench_Lite] [--run-id run]`)
		return nil
	}
	switch args[0] {
	case "lite-eval":
		return runSWEBenchEval(ctx, append([]string{"--dataset-name", "SWE-bench/SWE-bench_Lite"}, args[1:]...))
	case "lite-full":
		return runSWEBenchEval(ctx, append([]string{"--dataset-name", "SWE-bench/SWE-bench_Lite"}, args[1:]...))
	case "verified-eval":
		return runSWEBenchEval(ctx, append([]string{"--dataset-name", "SWE-bench/SWE-bench_Verified"}, args[1:]...))
	case "verified-full":
		return runSWEBenchEval(ctx, append([]string{"--dataset-name", "SWE-bench/SWE-bench_Verified"}, args[1:]...))
	case "eval":
		return runSWEBenchEval(ctx, args[1:])
	default:
		return fmt.Errorf("unknown swe-bench preset %q", args[0])
	}
}

func runSWEBenchEval(ctx context.Context, args []string) error {
	dataset := "SWE-bench/SWE-bench_Lite"
	runID := "slack-copilot"
	predictions := ""
	maxWorkers := "1"
	for len(args) > 0 {
		switch args[0] {
		case "--predictions", "--predictions-path":
			if len(args) < 2 {
				return fmt.Errorf("%s requires a path", args[0])
			}
			predictions = args[1]
			args = args[2:]
		case "--dataset-name":
			if len(args) < 2 {
				return fmt.Errorf("--dataset-name requires a value")
			}
			dataset = args[1]
			args = args[2:]
		case "--run-id":
			if len(args) < 2 {
				return fmt.Errorf("--run-id requires a value")
			}
			runID = args[1]
			args = args[2:]
		case "--max-workers":
			if len(args) < 2 {
				return fmt.Errorf("--max-workers requires a value")
			}
			maxWorkers = args[1]
			args = args[2:]
		default:
			return fmt.Errorf("unknown swe-bench option %q", args[0])
		}
	}
	if predictions == "" {
		return fmt.Errorf("--predictions is required. SWE-bench predictions are JSONL with instance_id, model_name_or_path, and model_patch")
	}
	python := sweBenchPython()
	pyArgs := []string{
		"-m", "swebench.harness.run_evaluation",
		"--dataset_name", dataset,
		"--predictions_path", predictions,
		"--max_workers", maxWorkers,
		"--run_id", runID,
	}
	if err := runExternal(ctx, python, pyArgs); err != nil {
		return fmt.Errorf("%w\nInstall official SWE-bench harness first, for example `pip install swebench`, and ensure Docker is running", err)
	}
	return nil
}

func sweBenchPython() string {
	if path := strings.TrimSpace(os.Getenv("SWE_BENCH_PYTHON")); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err == nil {
		candidate := home + "/.local/share/slack-copilot-agent/swebench-venv/bin/python"
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
	}
	return "python3"
}
