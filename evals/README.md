# Agent harness evaluation

This module compares agent **harnesses**, not native models. Every candidate is configured to use the same model endpoint and model identifier. The evaluator is intentionally independent from `packages/agent` and all provider packages: it launches each product as a black-box subprocess, inspects the resulting workspace, and records machine-readable evidence.

## What is implemented

- A deterministic local task runner with isolated workspace copies, wall-clock limits, command/test grading, JSONL case records, and an aggregate JSON report.
- Command adapters for slack-copilot, Codex CLI, Claude Code, Pi, and OpenCode. Commands are data, so version-specific flags can be changed without changing the evaluator.
- Optional candidate version probes, recorded once per run and copied into every case record.
- A shared gateway contract (`OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL`, and one model ID) and a LiteLLM deployment example exposing both OpenAI-compatible and Anthropic-compatible routes.
- Importers for Terminal-Bench/Harbor-style task directories and a neutral task manifest for repository-specific YouTrack/PR cases later. Public benchmark adapters must preserve container isolation metadata instead of relying on the local smoke runner as a security boundary.

This does not claim benchmark results. The checked-in smoke suite validates the evaluator itself; meaningful comparisons require pinning candidate versions, supplying credentials, and running an established suite such as Terminal-Bench through Harbor.

## Roadmap

- Harden the Terminal-Bench/Harbor-style importer as the first public benchmark path, with containerized execution owned by Harbor or an equivalent benchmark adapter.
- Add larger local-coding suites for multi-file edits, failing command recovery, repo navigation, and long-context tasks.
- Add SWE-bench Lite / SWE-bench Verified adapters after the local harness schema is stable.
- Add hosted ops and Slack surface suites separately from local coding benchmarks, so product-surface behavior does not distort CLI harness comparisons.

## Quick start

```sh
go build -o bin/slack-copilot ./cli/cmd/slack-copilot
python3 evals/run.py \
  --suite evals/suites/smoke.json \
  --candidates evals/candidates.example.json \
  --model your-controlled-model \
  --output evals/results/smoke
```

Set `EVAL_OPENAI_BASE_URL` and `EVAL_ANTHROPIC_BASE_URL` to the same gateway. The slack-copilot candidate uses `--protocol responses`, so it exercises the same canonical provider adapter and Responses wire client available to the hosted profile. Each candidate command receives `EVAL_MODEL`, `OPENAI_MODEL`, and `ANTHROPIC_MODEL`. Run `python3 evals/run.py --help` for filtering, repetitions, and dry-run options.

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

Import a Harbor/Terminal-Bench-style suite with an explicit container boundary:

```sh
python3 evals/import_harbor.py path/to/tasks \
  --output evals/suites/terminal-bench.json \
  --container-image ghcr.io/example/terminal-bench@sha256:...
```

## Suite schema

Suites must use `schema_version: 1`; older ad-hoc suite shapes are intentionally unsupported.

Each task declares:

- `id`, `category`, `source`, `fixture`, `prompt`, `test`, `timeout_seconds`
- `tags`, for filtering and aggregate analysis
- `metadata`, for benchmark-specific fields that should be preserved in records

The runner copies `fixture` into an isolated workspace and records `category`, `source`, `tags`, and `metadata` in every case record.

The suite schema intentionally has no `allow_failure` or expected-failure switch. Flaky or unstable tasks should be labeled through `tags` or `metadata` and explicitly excluded with filters when needed; if selected, their grader result counts normally.

The schema also does not require a top-level `difficulty`. Benchmark-specific difficulty labels can live under `metadata.difficulty` until multiple imported suites prove a shared scale is useful.

For public benchmark suites, `metadata.isolation` should declare the external isolation boundary. The Harbor importer requires a pinned `--container-image` and records:

```json
{
  "kind": "container",
  "runtime": "harbor",
  "image": "registry.example/bench@sha256:...",
  "enforced_by": "benchmark-adapter"
}
```

The local runner does not turn that metadata into a sandbox. Its job is orchestration, repeatability, records, and grading. Harbor/Terminal-Bench or another adapter owns container lifecycle, image pinning, mounts, network policy, and process isolation for public comparisons.

## Candidate schema

Each candidate declares a black-box launch `command`. `version_command` is optional and non-fatal: the runner executes it once at the start of a run, records stdout/exit status in `candidate_versions.json`, includes the same data in `summary.json`, and attaches the candidate's entry to each case record.

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

- `launch`, when the candidate command cannot start
- `agent_exit`, when the candidate exits non-zero
- `timeout`, when the candidate or grader exceeds the task limit
- `test_failure`, when the candidate finishes but the grader fails

`summary.json` aggregates these under `failure_classes`. Semantic labels such as tool misuse, context miss, or instruction-following failure should come from a later log classifier, not from this runner.

## Fair comparison protocol

1. Pin the exact tool versions and container image digest.
2. Route all candidates through the same gateway/model with equivalent reasoning and output limits.
3. Start each case from an identical workspace snapshot and clean process environment; public benchmark suites must run inside the adapter's pinned container boundary.
4. Run multiple repetitions with shuffled candidate order.
5. Report pass rate, timeout/error rate, duration, test output, and raw agent logs. Do not collapse failures into one score.
6. Do not hide selected failures with allowlists; exclude unstable tasks explicitly or count them.
7. Keep suite adapters and candidate adapters outside product runtime code.

`gateway/compose.yaml` is an operator example, not a production security boundary. Do not expose it publicly.
