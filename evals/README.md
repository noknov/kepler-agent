# Agent harness evaluation

This module compares agent **harnesses**, not native models. Every candidate is configured to use the same model endpoint and model identifier. The evaluator is intentionally independent from `packages/agentv2` and all provider packages: it launches each product as a black-box subprocess, inspects the resulting workspace, and records machine-readable evidence.

## What is implemented

- A deterministic local task runner with isolated workspace copies, wall-clock limits, command/test grading, JSONL case records, and an aggregate JSON report.
- Command adapters for slack-copilot v2, Codex CLI, Claude Code, Pi, and OpenCode. Commands are data, so version-specific flags can be changed without changing the evaluator.
- A shared gateway contract (`OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL`, and one model ID) and a LiteLLM deployment example exposing both OpenAI-compatible and Anthropic-compatible routes.
- Importers for Terminal-Bench/Harbor-style task directories and a neutral task manifest for repository-specific YouTrack/PR cases later.

This does not claim benchmark results. The checked-in smoke suite validates the evaluator itself; meaningful comparisons require pinning candidate versions, supplying credentials, and running an established suite such as Terminal-Bench through Harbor.

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

## Fair comparison protocol

1. Pin the exact tool versions and container image digest.
2. Route all candidates through the same gateway/model with equivalent reasoning and output limits.
3. Start each case from an identical workspace snapshot and clean process environment.
4. Run multiple repetitions with shuffled candidate order.
5. Report pass rate, timeout/error rate, duration, test output, and raw agent logs. Do not collapse failures into one score.
6. Keep suite adapters and candidate adapters outside product runtime code.

`gateway/compose.yaml` is an operator example, not a production security boundary. Do not expose it publicly.
