# Evaluation

Use this directory to test evaluator mechanics and run attributable Agent
experiments. It does not currently provide a verified end-to-end release gate.

| Purpose | Entry | Evidence produced |
| --- | --- | --- |
| Check runner/report wiring without model calls | `make eval-check` | Dry-run artifacts and evaluator unit tests |
| Run local synthetic cases | `run.py` | Command/test outcomes in copied workspaces |
| Run public coding datasets | `run_harbor.py` with a compatible Agent adapter | Harbor-native environment and grader results |
| Gate a completed compatible summary | `gate.py` | Threshold checks; input validity must also be established |
| Test hosted delivery, recovery, and authorization | Dedicated product tests | Separate from CLI coding scores |

## Authentication and preflight

Kepler uses gateway login/bootstrap rather than direct-provider CLI flags.
Run `bin/kepler-agent login --api-url <your-public-gateway>` once. The runner
reads that existing private login, validates `/cli/bootstrap`, and selects its
actual model automatically. An explicit `--model` must match bootstrap. Expired
login and model mismatches fail before cases start. Tokens stay in child process
environments and are never included in result manifests.

`EVAL_KEPLER_API_URL` and `KEPLER_TOKEN` can override the saved login. For a local
Docker gateway, set the host and container addresses explicitly:

```sh
export EVAL_KEPLER_API_URL=http://127.0.0.1:8080
export EVAL_KEPLER_CONTAINER_API_URL=http://host.docker.internal:8080
```

The container override is used only by the Harbor adapter; it does not change
which gateway/model the host preflight checks. Both addresses must reach the
same deployment. Direct-provider candidates still need their own credentials.

The local runner copies a workspace and HOME but does not supply an OS isolation
boundary for arbitrary Agent/grader code. The grader gets a separate copy with
declared protected files restored and a credential-free environment, but both
processes still execute on the host. Do not run untrusted cases with privileged
host access. Public datasets must remain in Harbor's native execution path.

The gate verifies run completion, non-dry-run artifacts, finite metrics,
compatible baseline identity, candidate presence, and minimum coverage. It
still cannot prove dataset validity, model parity, or absence of contamination.
Capability skips change the eligible denominator, so keep the default full
coverage requirement unless a reviewed profile requires otherwise.

## Reading order

1. [Local wiring check](#quick-start) for changes to the evaluator.
2. [Public benchmarks](#public-benchmarks-through-harbor) for actual dataset runs.
3. [Result contract](#result-directory-contract) for stored evidence.
4. [Comparison protocol](#fair-comparison-protocol) before comparing scores.

## What is implemented

- A local task runner with separate workspace copies, wall-clock limits, command/test grading, JSONL case records, and an aggregate JSON report.
- Command adapters for the local `kepler-agent` CLI, Codex CLI, Claude Code, Pi, and OpenCode. Commands are data, so version-specific flags can be changed without changing the evaluator.
- Optional candidate version probes, recorded once per run and copied into every case record.
- Capability-aware eligibility: tasks declare the minimum capabilities they exercise; a candidate that does not declare a requirement is recorded as `skipped`, never as a failed run.
- Per-candidate, category, and tag coverage with weighted pass rate, median latency, p95 latency, and failure-class breakdowns in JSON and the static report.
- A shared gateway contract (`OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL`, and one model ID) and a LiteLLM deployment example exposing both OpenAI-compatible and Anthropic-compatible routes.
- A direct Harbor public-benchmark launcher for Terminal-Bench 2.1, SWE-bench Verified, and Harbor Index. Harbor owns task images, sandboxing, and grading; the local runner is never used to grade those datasets.
- A custom Harbor adapter for kepler-agent. It builds a full, supplied Git commit inside each task environment, so source-built trials have an explicit revision. The optional binary upload path records a binary hash; source correspondence must also be verified.

This does not claim benchmark results. The checked-in smoke suite validates evaluator mechanics only. Product comparisons must use Harbor's public datasets and their native grader.

## Roadmap

- Add a result normalizer that combines completed Harbor job directories into a single cross-agent report without modifying raw Harbor results.
- Add a published environment image for kepler-agent, removing setup-time package installation while retaining the same pinned source-ref contract.
- Add hosted operations and Slack-surface evaluations separately from coding benchmarks, so product-specific surfaces do not distort the public CLI-harness score.

## Quick start

Run three real local coding cases, using the authenticated gateway model:

```sh
make eval
# Optional stable output directory (must not already exist):
make eval EVAL_OUTPUT=evals/results/my-run
```

Use `make bench` separately for Go storage/tool microbenchmarks; it makes no
model calls.

This builds the current CLI, runs the candidates, restores protected tests in a
separate grader workspace, and generates `report.html`. Case scores describe
these small synthetic tasks, not an external benchmark ranking.

`make eval-check` is the separate no-model wiring/unit-test check. It must not
be mistaken for a real evaluation.

Set `EVAL_OPENAI_BASE_URL` and `EVAL_ANTHROPIC_BASE_URL` for direct-provider
candidates. For kepler-agent, set `EVAL_KEPLER_API_URL` and `KEPLER_TOKEN`;
the secret is passed only through the child environment and is not written to
the manifest. Each candidate also receives `EVAL_MODEL`, `OPENAI_MODEL`, and
`ANTHROPIC_MODEL`. Run `python3 evals/run.py --help` for filters and repetitions.

Task filters are composable:

- multiple `--task`, `--category`, `--source`, or `--tag` values are OR within the same field
- different filter fields are ANDed together

Filtering example (wiring only):

```sh
python3 evals/run.py \
  --suite evals/suites/smoke.json \
  --candidates evals/candidates.example.json \
  --model dry-run \
  --category bugfix \
  --tag go \
  --output evals/results/go-bugfix \
  --dry-run
```

Generate a static report from any result directory:

```sh
python3 evals/report.py evals/results/go-bugfix
```

Gate a release against both an absolute floor and a compatible baseline. Keep
the model, task selection, weights, repetitions, and gateway compatible. For
a product-version regression, record the two candidate versions as the intended
variable. Verify report validity and coverage before running the threshold check:

```sh
python3 evals/gate.py evals/results/current/summary.json \
  --candidate kepler-agent --min-pass-rate 0.80 --max-timeout-rate 0.05 \
  --max-p95-seconds 180 --baseline evals/baselines/kepler-agent.json \
  --max-pass-rate-regression 0.02
```

The smoke runner is not a benchmark adapter. Use it only to validate changes to
this evaluator; do not use its score in external harness comparisons.

## Public benchmarks through Harbor

Harbor is the only execution path for public datasets. Its task images,
verifier, lifecycle, and result schema remain intact. The launcher writes an
`invocation.json` next to Harbor's jobs before it starts, recording the exact
dataset, candidate, model, command, and (for `kepler-agent`) source commit.

Start with a single official CPU task and its native oracle control:

```sh
make eval-harbor-oracle
make eval-harbor
```

The target builds a Linux CLI and uploads it, avoiding an unpublished-source
clone or task-by-task Go installation. The invocation records the source commit
and binary SHA256. `EVAL_GOARCH=amd64` selects another Docker architecture.
The binary adapter uses the host's trusted CA bundle when available and installs
missing ripgrep, a CLI tool dependency. It probes both executables before model
execution. Minimal task images must not be assumed to include either dependency.

The default task is Terminal-Bench 2.1 `openssl-selfsigned-cert`. To select another task:

```sh
make eval-harbor EVAL_HARBOR_BENCHMARK=terminal-bench-2.1 \
  EVAL_HARBOR_ARGS='--include-task terminal-bench/cancel-async-tasks'
```

Use fully qualified Harbor task names. Omit task filters only when ready for the
full dataset's time/resource requirements. Review the invocation first with
`run_harbor.py --dry-run --model <model>`. Source-building remains available
without `--binary-path`, but requires a reachable published commit and a suitable
Go toolchain inside the task image.

The adapter also puts reported token usage and structural execution metrics
into Harbor's native `AgentContext`; missing usage is left unknown, never priced
as a fabricated USD cost. Observed model changes fail rather than silently
mislabelling the experiment.

Inspect native results with `harbor view <output-directory>`. A zero Harbor CLI
exit can still contain errored trials; the wrapper now verifies completion and
native error counts before reporting success. A completed execution can have
reward zero: model failure is a valid benchmark result, not an infrastructure
success claim. Cold verifier dependency installation may require an explicit
`--verifier-timeout-multiplier`; it is retained in the invocation. Do not compare
runs with different timeout settings as equivalent.

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
`pi`. `kepler-agent` uses this repository's custom adapter and requires a full
40-character `--source-ref`; this is intentionally mandatory. Candidate tools
can differ in provider authentication and model controls, so record those
agent-specific settings with the Harbor job rather than asserting model parity
that was not actually achieved. For the source-build path, the referenced commit must be reachable from
`source_repo` (the public origin by default). Local binary upload records its
hash and does not require pushing the commit.

For Kepler, provider credentials remain on the gateway/worker (including SOPS
managed Qoder credentials). The task container receives a CLI session token,
not the operator PAT. Retired `provider`, `protocol`, `api_key_env`, and
`base_url_env` adapter kwargs are rejected because the current CLI has no such
flags.

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
- Kepler case records include model request counts, tool failures, observed model, first-token latency, and reported usage parsed from structured CLI events
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

## Verified runs (2026-10-13)

- Real local Qoder `Qwen3.8-Flash` smoke: **3/3 passed**, zero tool failures in
  the final run. Times were 18.0s, 16.7s and 19.9s; these are single samples,
  not a controlled model/harness ranking.
- Terminal-Bench 2.1 `openssl-selfsigned-cert`: native oracle **reward 1**;
  Kepler completed without infrastructure errors but passed **5/6 tests**, so
  its official binary **reward was 0**. The Python verification script's
  `cryptography` dependency was missing. This result must not be called a pass.
- `cancel-async-tasks` is not a usable quality baseline on this setup: its
  oracle failed the short SIGINT timing assertions, and the Kepler trial hit
  the agent timeout. Its post-timeout reward is not used as quality evidence.
- Hello World controls exposed verifier dependency-download timeouts. They
  were retained as failed runs; increasing a timeout is not a model-quality fix.

Raw local artifacts are under `evals/results/2026-10-13-*` (Git ignored). Use
`report.html` for local cases and `harbor view <run-directory>` for native
Harbor scores/logs. The final adapter also interrupts a timed-out container CLI
before grading; local candidate/grader processes get bounded process-group
cleanup. Neither path should keep mutating the workspace after its deadline.

## Preparing and diagnosing long runs

`make eval-harbor` now prepares a SHA-256-verified upstream static ripgrep
binary and uploads it alongside the CLI and CA bundle. Task setup performs
version probes without apt/apk downloads. The static binary is x86_64-musl;
on Apple Silicon it uses Docker's existing amd64 emulation. Task-specific
languages and libraries are still the agent's responsibility.

The optional `--prepare-images` resolves task specs and warms selected Docker
images before timed trials. It is disabled by default so a complete dataset
cannot fill a Docker disk shared with application services. Pulls have bounded
retries and fail before any model trials when the registry is unavailable. Image digests are
recorded in `invocation.json`. This cannot prevent a later Docker daemon failure
or a verifier's own dependency download outage. It does not change official
agent/verifier timeouts, rewards, or tests.

Adapter exceptions contain the terminal failure and an agent-log reference,
not an entire transcript. Failed/cancelled turns retain their reported usage.
Qoder malformed tool responses carry content-free protocol diagnostics and use
bounded retry before output is committed. Qoder streaming HTTP timeouts bound
inactivity; caller cancellation continues to bound total execution time.

Long single turns can compact completed tool exchanges. Original current user
input and complete tool-call/result batches remain together; a single oversized
indivisible message still fails explicitly rather than being silently truncated.

All `make eval-harbor` and oracle jobs now use the shared native directory
`evals/results/harbor-jobs`. Run `harbor view evals/results/harbor-jobs --jobs`
to browse multiple runs. Invocation/log artifacts remain in each named output
folder; only the current native job contributes to that invocation's outcome.
A preflight failure creates no native job because model trials have not begun.

Image warming has no artificial task wall-clock limit: Docker owns registry
network timeouts, failed pulls have bounded retries, and the operator can cancel
preparation. Large image downloads must not be repeatedly cut off after 300s.

## Shared Docker capacity

The default target no longer pre-pulls every task image. Native Harbor prepares
one task at a time, so a job appears immediately in the shared viewer. Yesterday's
preflight failures have launcher logs but no native job; they are not completed
model trials.

When Docker is shared with application services, explicitly reserve disk space:

```sh
make eval-harbor EVAL_HARBOR_ARGS= \
  EVAL_HARBOR_RESOURCE_ARGS="--disk-probe-container slack-copilot-postgres --min-free-disk-gib 20"
```

The probe runs read-only POSIX `df` inside an existing container on the target
Docker daemon. It checks before launch and every five seconds during the job.
Insufficient capacity or a failed probe stops the whole Harbor process group and
records `resource_exhausted`, without changing task tests or rewards. The 20GiB
value above is an explicit operational reserve, not a task-quality heuristic.
