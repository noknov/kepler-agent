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

Working branch: feat/semantic-memory-retrieval. PRs #23–#28 are merged after
CI and review; no deployment was performed. The next commit is the embedding
transport/resource prerequisite, not an enabled semantic index. It adds a typed
Embedder, strict bounded OpenAI-compatible HTTP adapter, and one shared attempt
lifetime for generation/embedding admission, reservation, settlement and receipts.
`make check` plus model/provider/runtime/hosted/delegation race suites passed.

Next implement opt-in, versioned semantic recall in the existing memory owner:
- Reuse the bounded per-user PostgreSQL namespace rather than introduce another
  search service. Measure exact vector retrieval before selecting a new index.
- Bind cached vectors and claims to record version, reset generation, model
  space/dimensions and authenticated identity. Update-only result CAS must never
  resurrect deleted/reset records; expiry excludes vectors with their source.
- Keep network I/O outside SQL transactions; short owner/lease claims prevent
  duplicate concurrent indexing. Empty/unavailable caches retain lexical recall.
- A model-space change must be explicit and fence old writers; no guessed aliases,
  keyword semantic rules, automatic fact deletion or unmeasured default enablement.
- Add the matching deploy migration and schema-parity tests before enabling it.
- Validate zero lexical-overlap candidate retrieval with controlled vectors and
  real PostgreSQL, distinguishing this from real-model relevance evaluation.

Follow up native structured remote requests with an explicit capability receipt
so an older server ignoring the optional field cannot masquerade as native mode.
Real-model comparison remains unavailable without a configured login/provider and
public task containers. Keep every useful source milestone on GitHub before any
quota wait. User requests a live loop waiting at <=5% remaining, ~30-minute checks,
and automatic continuation only at 100%; do not spend reset credits or ask them
to confirm continuation.

Temporary integration stores are ours only: PostgreSQL on loopback 55437 under
/tmp/kepler-review-postgres and Redis 7.4.7 on loopback 56379 under
/tmp/kepler-capability-redis. Stop them before final handoff; never use deployment
application databases for tests. Logs are /tmp/kepler-capability-integration.log
and /tmp/kepler-capability-pg-tests.log. Full integration gate passed.
