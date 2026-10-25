# Agent capability and reliability hardening

Status: active.

## Outcome and acceptance

Improve useful task completion, bounded execution, recovery, interoperability,
and measurable performance across the shared runtime and its deployed products.
Do not equate protocol/framework names with capability or claim Codex parity
without fixed-model task results. Preserve existing actor, policy, cancellation,
and durable side-effect contracts. Source baseline: 80ba7a6; deploy: aa78940.

Acceptance is per independently reviewable PR: relevant positive and negative
regressions, race checks for concurrency, `make check`, and exact source evidence.
Live-model quality, latency, external interoperability, and HA require separate
measured runs; deterministic tests cannot substitute for them.

## Work

- [x] Model boundary: explicit native/validated JSON output contracts, actual
  provider wire mappings, schema validation, incomplete/refused-output handling,
  no unvalidated output publication, and an existing production consumer.
- [x] MCP: verify negotiated versions and RPC response identity, bounded discovery
  and incremental SSE reading; preserve uncertain-write behavior and credentials.
- [x] Evaluation: reject incompatible/incomplete evidence, validate baselines and
  raw records, preserve failures, and record attributable task-level outcomes.
- [x] Delegation/A2A: retain generic local task ownership; design a remote adapter
  around an actual protocol binding, authentication, cancellation, bounded results,
  and evidence. Code review remains one composing workflow, not the protocol owner.
- [ ] Retrieval/memory: improve semantic candidate coverage only with an explicit
  embedding/index lifecycle, namespace/deletion fences, and relevance comparisons.
- [ ] Context/planning/recovery: audit task-state retention, model/tool attempt
  accounting, checkpoint recovery, and cancellation together; fix demonstrated gaps.
- [ ] Operations: establish deploy diagnostics, service metrics/SLO inputs, bounded
  resource behavior, reproducible failure/recovery checks, and migration parity.
- [ ] Run attributable real-model evaluation when credentials/services are available;
  publish every completed PR and keep this handoff usable from another device.

## Decisions

Use existing owners: model contracts in agent/model; wire formats in llm;
validation/adaptation in providers; delegation in agent/delegation; product code
review in workflows/codereview. Do not introduce Spring AI/LangGraph/Temporal
alongside the existing runtime merely to accumulate framework coverage.

Use a maintained JSON Schema implementation in providers rather than inventing a
partial validator. It owns only validation, allows internal references, and must
not resolve arbitrary network/file references. Core remains dependency-neutral.
A2A is cross-system communication, distinct from internal parallel reviewers.
Remote communication must not convey new host capability or operator authority.

## Progress and evidence

2026-10-26: both main branches clean and match GitHub. All historical local branch
file trees exist in merged remote history. Baseline doctor/repo-check passed with
Python 3.11. Existing runtime/delegation/tool/memory/MCP/provider/appserver package
suites passed during the preceding assessment. No live-model or HA claim made.

Structured-output milestone: native OpenAI Chat/Responses and Anthropic mappings,
explicit validated mode, self-contained full-schema validation, and memory ranker
integration implemented. Failed hosted completions now preserve usage across the
remote model protocol without exposing rejected content. `make check` and
`go test -race ./packages/cloud ./packages/providers ./packages/llm ./packages/memory`
passed. This is contract/recovery evidence, not model-quality measurement.

MCP milestone: incremental SSE result/error handling, exact RPC identity,
negotiated version validation/headers and bounded non-cyclic discovery added.
Tests use a real HTTP server that keeps SSE open after its result, plus protocol
errors, version mismatches and exhausted discovery. Structured output is PR #23.

Quota instruction: inspect the account's 300-minute window at milestones. At
remaining <=5%, save/publish the current work and keep this agent loop waiting;
check approximately every 30 minutes and resume automatically only when the
window reports 100% remaining. No manual confirmation and no silent-state notices.
Do not spend reset credits. This instruction is from the user, 2026-10-26.

A2A milestone: official SDK 0.3 binding, configured peer send/get/cancel and
continuation, canonical same-session/actor reference ownership, target confinement,
unknown-outcome/usage disclosure, and inbound authenticated-scope executor added.
Local/hosted/app-server registration remains opt-in. Real SDK HTTP tests and
race suites passed. Native A2A 1.0/federation/HA hosting are not claimed.
No Docker CLI, existing Kepler login, operator ChatGPT session, or system psql
is available on this machine; real-model/public benchmark runs need those inputs.
MCP review reproduced and fixed empty SSE priming and null/non-object results;
the regression failed before the fix and passed after it.

Evaluation milestone: share pure aggregation; verify complete planned coverage,
finite raw weights/timings, grader exits, full recomputed outcomes/latency and
both baseline/current artifacts. Fixture content/modes/internal links now bind
comparison identity. New adversarial evidence tests plus the evaluator suite
passed (39 tests at this milestone). A real no-op grader control executed all
three synthetic fixtures: all failed, complete evidence validated, and the 100%
quality gate correctly rejected it. Artifacts remain in deploy/.cache/evidence-
wiring-control-20261026 and are explicitly not model-quality results.
`make check` passed. No live-agent quality claim.

Operational metrics milestone: standard worker process counters and aggregatable
histograms, authenticated queue-only durable gauges with failed-scrape semantics,
and constant-time latency rings with percentile sorting outside the lock.
M1 Pro/Go 1.25.0, three runs: full-window record path 436.0–448.4 ns/op before;
32.51–32.60 ns/op after including new histogram recording, zero allocations.
This is a recorder microbenchmark, not end-to-end model latency. Official client
v1.24.0 preserves the existing Go 1.25 toolchain. Local race suites and actual
PostgreSQL observer/hosted/connection/memory integration suites passed.

Memory/context milestone: reproduced a disclosure-allocation defect: a high-ranked
record larger than the disclosure byte budget consumed a slot before being
excluded, losing lower-ranked fitting evidence. Apply bytes before slots, retain
rank order and complete evidence, then run the existing version/generation fence.
The regression failed before the fix. No extra model calls, thresholds, mutable
ranking cache, automatic deletion, or synthetic embedding capability added.

## Handoff

PRs #23–#28 and #30 are merged after CI/review. Native remote structured output
now requires a matching native capability receipt; old optional-field-ignoring
servers fail closed while retaining usage. PR #29 began as a published embedding
resource prerequisite and now includes the real opt-in memory consumer, cache
ownership, separate terminal embedding accounting, and paired deploy migration018.
Do not merge it until final CI/review and migration parity are verified.

Semantic milestone: real PostgreSQL controlled-vector tests cover zero lexical
overlap, scope, source mutation/deletion/reset/expiry/model-space races, concurrent
claims, no held SQL connection during model calls, fallback and claim cleanup.
The native-array dot-product prototype measured 374.5–390.9 ms/query for 1000x1536;
pgvector exact retrieval measured 18.9–20.0 ms in the same local benchmark. This
justifies the extension, not an approximate index or another search service.
Schema parity fresh/upgrade/replay passed using official pgvector v0.8.7 built
against the isolated PG17.6 server. No actual model relevance claim; feature off.

Remaining: finish PR29/deploy publication/checks; inspect context/planning/durable
recovery for demonstrated defects, then publish a final capability assessment.
Live-model comparison is unavailable without a configured login/provider and
public task containers. Do not fabricate parity/quality/HA measurements.

User requests a live loop waiting at <=5% remaining with ~30-minute checks and
automatic continuation after reset, without confirmation. Reset has been observed
and work resumed; do not spend reset credits. Save all useful source/deploy changes
to GitHub before waits. Temporary stores are ours only: PostgreSQL on loopback
55437 under /tmp/kepler-review-postgres and Redis on56379 under
/tmp/kepler-capability-redis. Stop them before final handoff. pgvector build lives
in /tmp/kepler-capability-pgvector. Do not touch deployed application databases.
