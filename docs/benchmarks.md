# Coding Agent Benchmarks

Slack is an ingress surface. Coding benchmarks should exercise the local agent
runtime directly so results measure code reading, editing, debugging, and task
completion rather than Slack delivery behavior.

Benchmark-specific code and assets live under `benchmarks/`:

- `benchmarks/cmd/slack-copilot-bench`: standalone benchmark command
- `benchmarks/benchcli`: benchmark presets and official-harness wrappers
- `benchmarks/suites`: project-owned benchmark suites
- `benchmarks/fixtures`: project-owned benchmark fixtures
- `benchmarks/benchkit`: reusable benchmark harness library

The main `slack-copilot bench ...` command remains as a compatibility wrapper,
but new benchmark work should prefer `go run ./benchmarks/cmd/slack-copilot-bench ...`.

## Capability Targets

Use a benchmark matrix instead of one headline score:

| Capability | Primary signal | Suggested benchmark |
|---|---|---|
| Code generation | Function-level correctness | LiveCodeBench, HumanEval only as a smoke test |
| Code reading | Repository navigation and evidence quality | Internal repo-understanding tasks |
| Debugging | Reproduce, localize, patch, and verify failures | SWE-bench style tasks, internal bug fixtures |
| End-to-end work | Multi-step terminal and repo workflows | Terminal-Bench, internal work-order tasks |
| Architecture quality | Tool use, context handling, autonomy, cost | Internal regression suite over this agent runtime |

## Recommended Public Benchmarks

- **Terminal-Bench**: best fit for terminal-native agent behavior: inspect files,
  run commands, fix environment or code issues, and complete end-to-end tasks in
  containers.
- **SWE-bench family**: useful for repository bug-fixing, but treat public scores
  carefully. SWE-bench Verified is widely used but increasingly contaminated;
  SWE-bench Pro is longer-horizon but has known task-quality concerns. Use them
  as external reference points, not the only decision metric.
- **LiveCodeBench**: good for fresh code-generation signal because of the rolling
  cutoff design. It is less representative of repo-scale engineering.
- **HumanEval/MBPP/APPS**: useful only as cheap smoke tests. They are too small
  and saturated for judging a serious coding agent.

## Public Benchmark Adapters

Current status:

| Benchmark | Adapter status | Scoring source |
|---|---|---|
| HumanEval-compatible | Implemented locally from official-format JSONL | Task `test` field executed with `python3 check.py` in an isolated workspace |
| Terminal-Bench | Wrapper for official `tb`/`harbor` CLI | Official Terminal-Bench/Harbor harness |
| SWE-bench | Wrapper for official `swebench.harness.run_evaluation` | Official SWE-bench Docker harness |
| LiveCodeBench | Wrapper for `livecodebench` CLI | LiveCodeBench packaged runner |
| MBPP | Not implemented yet | Can follow the HumanEval adapter shape |

HumanEval-compatible run:

```bash
go run ./benchmarks/cmd/slack-copilot-bench humaneval smoke /path/to/HumanEval.jsonl.gz --self --workspace-root /tmp/slack-copilot-humaneval --keep-workspaces
go run ./benchmarks/cmd/slack-copilot-bench humaneval full /path/to/HumanEval.jsonl.gz --self --workspace-root /tmp/slack-copilot-humaneval
```

Terminal-Bench official wrapper:

```bash
uv tool install terminal-bench
go run ./benchmarks/cmd/slack-copilot-bench terminal-bench smoke
go run ./benchmarks/cmd/slack-copilot-bench terminal-bench smoke --task-id hello-world --dataset terminal-bench-core==0.1.1 --agent terminus
go run ./benchmarks/cmd/slack-copilot-bench terminal-bench full
```

Terminal-Bench 2.0 via Harbor:

```bash
uv tool install harbor
go run ./benchmarks/cmd/slack-copilot-bench terminal-bench harbor run -d terminal-bench/terminal-bench-2 -a oracle -l 5
```

SWE-bench official evaluation wrapper:

```bash
pip install swebench
go run ./benchmarks/cmd/slack-copilot-bench swe-bench lite-eval --predictions predictions.jsonl --max-workers 1
go run ./benchmarks/cmd/slack-copilot-bench swe-bench verified-eval --predictions predictions.jsonl --max-workers 1
go run ./benchmarks/cmd/slack-copilot-bench swe-bench lite-full --predictions predictions.jsonl --max-workers 1
go run ./benchmarks/cmd/slack-copilot-bench swe-bench verified-full --predictions predictions.jsonl --max-workers 1
```

SWE-bench predictions must use the official JSONL format:

```json
{"instance_id":"sympy__sympy-20590","model_name_or_path":"slack-copilot","model_patch":"diff --git ..."}
```

The Terminal-Bench and SWE-bench commands intentionally call official harnesses
instead of reimplementing scoring. They require the official CLI/package and
Docker where those harnesses require Docker.

LiveCodeBench wrapper:

```bash
uv tool install nvidia-livecodebench
go run ./benchmarks/cmd/slack-copilot-bench livecodebench smoke --first-n 1 --model openai/gpt-4o-mini
go run ./benchmarks/cmd/slack-copilot-bench livecodebench full --model openai/gpt-4o-mini
go run ./benchmarks/cmd/slack-copilot-bench livecodebench run --model openai/gpt-4o-mini --scenario codegeneration --first_n 5 --evaluate
```

Raw official CLI arguments are still supported for escape hatches:

```bash
go run ./benchmarks/cmd/slack-copilot-bench terminal-bench run --dataset terminal-bench-core==0.1.1 --agent terminus --task-id hello-world --livestream
go run ./benchmarks/cmd/slack-copilot-bench terminal-bench -- tb run --dataset terminal-bench-core==0.1.1 --agent terminus --task-id hello-world --livestream
```

## Local vs Remote

Run both, for different reasons:

- **Local** for fast iteration: small task slices, deterministic fixtures,
  record/replay model calls, pprof, and architecture regression checks.
- **Remote** for trustworthy headline runs: Docker isolation, clean machines,
  pinned images, parallelism, repeatable artifacts, and long-running SWE/terminal
  suites without contaminating a developer checkout.

Default workflow:

1. Run 10-30 internal/local tasks on every agent change.
2. Run a larger remote suite nightly or before release.
3. Keep public benchmark runs pinned by agent commit, model, prompt catalog,
   tool set, container image, and scorer version.

## Harness Shape

The benchmark runner should call `agent.Runner` through a local coding runtime,
not `slackhandler.Handler`.

```text
benchmark case
  -> isolated evaluation workspace
  -> local coding runtime
  -> agent.Runner
  -> shared read tools + local-only edit/command tools
  -> scorer: tests, diff checks, rubric, metrics
  -> JSONL result artifact
```

Minimum case schema:

```json
{
  "id": "go-debug-001",
  "kind": "debug",
  "repo": "fixtures/go-debug-001",
  "prompt": "Fix the failing parser test.",
  "setup": [
    { "argv": ["npm", "install"], "timeout_seconds": 120 }
  ],
  "graders": [
    { "type": "command", "command": ["go", "test", "./..."], "timeout_seconds": 120 },
    { "type": "patch_contains", "value": "parser" }
  ],
  "timeout_seconds": 900
}
```

Minimum result schema:

```json
{
  "id": "go-debug-001",
  "status": "passed",
  "duration_ms": 12345,
  "workspace": "/tmp/slack-copilot-evals/go-debug-001",
  "patch": "diff -ruN ...",
  "steps": 12,
  "llm_calls": 5,
  "tool_calls": 18,
  "tokens": 42000,
  "estimated_cost_usd": 0.12,
  "patch_bytes": 1840,
  "error": ""
}
```

## Runtime Requirements

The production Slack registry remains mostly read-only. Coding benchmarks need a
separate local runtime with:

- read tools: code search, code read, git read, code graph, diagnostics
- edit tools: exact replace and file write within a sandboxed workspace
- command tools: test/build commands in a disposable repo checkout
- artifact capture: final diff, logs, run transcript, token/cost metrics
- isolation: one fresh worktree/container per case

`runtime.NewCodingToolRegistry` is the intended starting point for the benchmark
runner. It enables local edit tools without exposing them through Slack.

The CLI benchmark runner creates an independent workspace per case. A case's
`workspace` field is treated as a template source and copied into the evaluation
root before the agent runs. Agent commands and graders receive the copied path,
so benchmark attempts do not mutate the developer checkout.

By default the evaluation root is a temporary directory and is removed after the
run. Use `--workspace-root DIR --keep-workspaces` to keep failed-case workspaces
for inspection.

The benchmark path does not instantiate `platform.Stores`, PostgreSQL, Redis, or
Slack clients. It only needs model credentials when `--self` is used.

CLI examples:

```bash
go run ./benchmarks/cmd/slack-copilot-bench builtin
go run ./benchmarks/cmd/slack-copilot-bench builtin --keep-workspaces
go run ./benchmarks/cmd/slack-copilot-bench run benchmarks/suites/local_coding_smoke.json --workspace-root /tmp/slack-copilot-evals --keep-workspaces -- ./my-agent "{{prompt}}" "{{workspace}}"
go run ./benchmarks/cmd/slack-copilot-bench run suite.json --workspace-root /tmp/slack-copilot-evals --keep-workspaces -- ./my-agent "{{prompt}}" "{{workspace}}"
go run ./benchmarks/cmd/slack-copilot-bench run suite.json --self --workspace-root /tmp/slack-copilot-evals --keep-workspaces
```

Supported grader types:

- `contains`, `not_contains`, `equals`, `regex` inspect the final answer.
- `file_contains`, `file_not_contains`, `file_exists` inspect files in the
  isolated evaluation workspace.
- `command` runs an argv command in the isolated workspace.
- `patch_contains`, `patch_not_contains` inspect the captured diff.
- `tool_called`, `max_tool_calls` inspect agent tool-use metrics.

There is a runnable local fixture at
`benchmarks/suites/local_coding_smoke.json`. It copies
`benchmarks/fixtures/go-palindrome-bug` into an isolated workspace and grades the
agent by running `go test ./...` plus checking the generated patch.

## Internal Benchmark Set

Add internal tasks because public benchmarks do not measure your architecture
well enough:

- small reading tasks where the answer must cite files/functions correctly
- bug fixtures with failing tests and hidden regression checks
- refactor tasks that require touching 3-8 files without changing behavior
- debugging tasks with noisy logs and one real root cause
- long-context tasks that require summarization/compaction
- tool-use traps: repeated failed calls, wrong file references, stale branch reads

These tasks should be versioned in the repo or a private benchmark repo, and
their expected outputs should be judged by tests whenever possible.
