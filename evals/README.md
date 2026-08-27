# Agent harness evaluation

This module compares agent **harnesses**, not native models. Every candidate is configured to use the same model endpoint and model identifier. The evaluator is intentionally independent from `packages/agent` and all provider packages: it launches each product as a black-box subprocess, inspects the resulting workspace, and records machine-readable evidence.

## What is implemented

- A deterministic local task runner with isolated workspace copies, wall-clock limits, command/test grading, JSONL case records, and an aggregate JSON report.
- Command adapters for the local `copilot-agent` CLI, Codex CLI, Claude Code, Pi, and OpenCode. Commands are data, so version-specific flags can be changed without changing the evaluator.
- Optional candidate version probes, recorded once per run and copied into every case record.
- Capability-aware eligibility: tasks declare the minimum capabilities they exercise; a candidate that does not declare a requirement is recorded as `skipped`, never as a failed run.
- Per-candidate, category, and tag coverage with weighted pass rate, median latency, p95 latency, and failure-class breakdowns in JSON and the static report.
- A shared gateway contract (`OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL`, and one model ID) and a LiteLLM deployment example exposing both OpenAI-compatible and Anthropic-compatible routes.
- A direct Harbor public-benchmark launcher for Terminal-Bench 2.1, SWE-bench Verified, and Harbor Index. Harbor owns task images, sandboxing, and grading; the local runner is never used to grade those datasets.
- A custom Harbor adapter for slack-copilot-agent. It builds a full, supplied Git commit inside each task environment, so the evaluated product revision is explicit and does not depend on the operator's local binary.

This does not claim benchmark results. The checked-in smoke suite validates evaluator mechanics only. Product comparisons must use Harbor's public datasets and their native grader.

## Roadmap

- Add a result normalizer that combines completed Harbor job directories into a single cross-agent report without modifying raw Harbor results.
- Add a published environment image for slack-copilot-agent, removing setup-time package installation while retaining the same pinned source-ref contract.
- Add hosted operations and Slack-surface evaluations separately from coding benchmarks, so product-specific surfaces do not distort the public CLI-harness score.

## Quick start

```sh
go build -o bin/copilot-agent ./cli/cmd/copilot-agent
python3 evals/run.py \
  --suite evals/suites/smoke.json \
  --candidates evals/candidates.example.json \
  --model your-controlled-model \
  --output evals/results/smoke
```

Set `EVAL_OPENAI_BASE_URL` and `EVAL_ANTHROPIC_BASE_URL` to the same gateway. The `copilot-agent` candidate invokes the local CLI with its own provider configuration and `--protocol responses`; it never routes through the Slack surface or a cloud-hosted Slack agent. Each candidate command receives `EVAL_MODEL`, `OPENAI_MODEL`, and `ANTHROPIC_MODEL`. Run `python3 evals/run.py --help` for filtering, repetitions, and dry-run options.

Task filters are composable:

- multiple `--task`, `--category`, `--source`, or `--tag` values are OR within the same field
- different filter fields are ANDed together

Example:

```sh
python3 evals/run.py \
  --suite evals/suites/smoke.json \
  --candidates evals/candidates.example.json \
  --model your-controlled-model \
  --category bugfix \
  --tag go \
  --output evals/results/go-bugfix
```

Generate a static report from any result directory:

```sh
python3 evals/report.py evals/results/go-bugfix
```

The smoke runner is not a benchmark adapter. Use it only to validate changes to
this evaluator; do not use its score in external harness comparisons.

## Public benchmarks through Harbor

Harbor is the only execution path for public datasets. Its task images,
verifier, lifecycle, and result schema remain intact. The launcher writes an
`invocation.json` next to Harbor's jobs before it starts, recording the exact
dataset, candidate, model, command, and (for `copilot-agent`) source commit.

First inspect the invocation. This is side-effect-free:

```sh
python3 evals/run_harbor.py \
  --benchmark terminal-bench-2.1 \
  --candidate copilot-agent \
  --source-ref "$(git rev-parse HEAD)" \
  --model controlled-model \
  --output evals/results/terminal-bench-2.1/copilot-agent \
  --dry-run
```

Then run the same command without `--dry-run`. Harbor needs Docker (or another
configured Harbor environment), model credentials, and network access to fetch
the public task images. Run Harbor's oracle for a newly selected dataset or
adapter before comparing agents.

For every candidate, use a separate jobs directory and keep the model, model
gateway, task selection, attempts, and concurrency constant:

```sh
python3 evals/run_harbor.py \
  --benchmark terminal-bench-2.1 \
  --candidate codex \
  --model controlled-model \
  --attempts 3 \
  --output evals/results/terminal-bench-2.1/codex
```

Available public suites:

- `terminal-bench-2.1`: terminal-use and environment interaction; primary CLI harness comparison.
- `swe-bench-verified`: 500 validated repository issues; run after Terminal-Bench because it is substantially more expensive.
- `harbor-index-1.0`: broader agent-index tasks; report separately rather than averaging it with code repair.

The built-in Harbor candidates are `codex`, `claude-code`, `opencode`, and
`pi`. `copilot-agent` uses this repository's custom adapter and requires a full
40-character `--source-ref`; this is intentionally mandatory. Candidate tools
can differ in provider authentication and model controls, so record those
agent-specific settings with the Harbor job rather than asserting model parity
that was not actually achieved. The referenced commit must already be reachable
from `source_repo` (the public origin by default); push it before starting a
run.

For a deployed provider profile, pass only configuration *names* as Harbor
kwargs and inject the corresponding values through `--agent-env` or the process
environment. For example, a LongCat deployment profile uses
`provider=longcat`, `protocol=anthropic`, `api_key_env=LONGCAT_API_KEY`, and
`base_url_env=LONGCAT_BASE_URL`. Do not put keys or endpoint secrets in the
command line or the invocation manifest.

## Suite schema

Suites must use `schema_version: 1`; older ad-hoc suite shapes are intentionally unsupported.

Each task declares:

- `id`, `category`, `source`, `fixture`, `prompt`, `test`, `timeout_seconds`
- `tags`, for filtering and aggregate analysis
- `metadata`, for benchmark-specific fields that should be preserved in records
- optional `required_capabilities`, the declared minimum harness abilities such as `workspace_read`, `workspace_write`, `shell`, `mcp`, or `skills`
- optional positive `weight`, used only for the weighted pass rate (default `1`)

The runner copies `fixture` into an isolated workspace and records `category`, `source`, `tags`, and `metadata` in every case record.

The suite schema intentionally has no `allow_failure` or expected-failure switch. Flaky or unstable tasks should be labeled through `tags` or `metadata` and explicitly excluded with filters when needed; if selected, their grader result counts normally.

The schema also does not require a top-level `difficulty`. Benchmark-specific difficulty labels can live under `metadata.difficulty` until multiple imported suites prove a shared scale is useful.

Public benchmark suites must not be represented in this local schema. The
retired `import_harbor.py` command fails closed rather than copying benchmark
files into a local workspace and accidentally bypassing Harbor's verifier.

## Candidate schema

Each candidate declares a black-box launch `command`. `version_command` is optional and non-fatal: the runner executes it once at the start of a run, records stdout/exit status in `candidate_versions.json`, includes the same data in `summary.json`, and attaches the candidate's entry to each case record.

Candidates can also declare a `capabilities` string list. This is a reproducible evaluation-profile declaration, not a marketing claim about every agent version or deployment. A task whose `required_capabilities` are not declared by a candidate is emitted as `skipped` with the missing capabilities, and excluded from its eligible denominator. Keep these profiles under review when changing a command adapter or agent version.

Use `{repo}`, `{workspace}`, `{prompt}`, `{model}`, `{candidate_model}`, `{case_root}`, `{home}`, `{env_openai_base}`, and `{env_anthropic_base}` placeholders in commands where needed.

For non-dry runs, every case directory also keeps:

- `original/`, the pristine copied fixture
- `workspace/`, the candidate's final workspace
- `workspace.diff`, a unified recursive diff from `original/` to `workspace/`

## Result directory contract

Every run writes a stable artifact layout:

- `run.json`, the run manifest: suite/candidate inputs, filters, model, seed, repetitions, selected tasks/candidates, shuffled plan, start/end timestamps, repo commit, dirty status, and summary pointers
- `summary.json`, aggregate pass/fail/error/timeout counts and candidate versions
- `records.jsonl`, one machine-readable record per case
- `candidate_versions.json`, version probe output for every selected candidate
- `report.html`, optional static dashboard generated by `evals/report.py`
- `<task>__<candidate>__<repetition>/`, per-case artifacts

Non-dry case artifacts include `agent.log`, `test.log` when tests launch, `original/`, `workspace/`, and `workspace.diff`. Dashboards and CI jobs should treat `run.json` as the machine-readable entrypoint. Humans can open `report.html` for the same run context, candidate versions, aggregate counts, case records, and artifact links.

Case records use a small stable status enum: `passed`, `failed`, `timeout`, `agent_error`, `launch_error`, and `dry_run`. Failed records also include a mechanical `failure_class`:

`skipped` is an additional non-failure status for capability-ineligible cases. Candidate summaries report both raw total and eligible total; pass rates and weighted pass rates use only eligible cases. Latency summaries use all launched cases and include median and p95 wall-clock duration.

- `launch`, when the candidate command cannot start
- `agent_exit`, when the candidate exits non-zero
- `timeout`, when the candidate or grader exceeds the task limit
- `test_failure`, when the candidate finishes but the grader fails

`summary.json` aggregates these under `failure_classes`. Semantic labels such as tool misuse, context miss, or instruction-following failure should come from a later log classifier, not from this runner.

## Fair comparison protocol

1. Pin the exact tool versions and container image digest.
2. Route all candidates through the same gateway/model with equivalent reasoning and output limits.
3. Start each case from an identical task image and clean process environment; public benchmark suites must run inside Harbor's native container boundary.
4. Use Harbor's native repeat/attempt controls. Do not combine results from different models, task filters, dataset versions, or source commits.
5. Report pass rate, timeout/error rate, duration, test output, and raw agent logs. Do not collapse failures into one score.
6. Do not hide selected failures with allowlists; exclude unstable tasks explicitly or count them.
7. Keep suite adapters and candidate adapters outside product runtime code.
8. Compare each category and tag matrix before using an aggregate score. A global pass rate can hide a harness that is strong on trivial edits but weak on repository navigation, recovery, or operations.

`gateway/compose.yaml` is an operator example, not a production security boundary. Do not expose it publicly.
