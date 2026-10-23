# Harness design status

This document tracks design direction. It is not a feature checklist or a
claim that another product's implementation has been reproduced. Current
behavior belongs in [architecture](architecture.md) and [runtime](runtime.md).

## Decisions to retain

- Keep one transport-neutral loop and canonical append-only transcript.
- Compose product policy, storage, credentials, and delivery in profiles/surfaces.
- Derive client views and observability from events rather than adding competing
  execution-state stores.
- Extend tool, skill, provider, and MCP interfaces without handing ownership of
  persistence or authorization to arbitrary extensions.
- Keep hosted authorization operator-controlled and local execution sandboxed.
- Keep public benchmark grading in Harbor and product-specific tests separate.

## Implementation and remaining validation

| Track | Implementation entry | Remaining work |
| --- | --- | --- |
| Trace and trajectory | Runtime events, run projection, `thread/trajectory` | Verify trace joins, redaction and replay; record enough versions to interpret historical facts |
| App-server admission | Active-turn limit and retryable `-32001` | Verify client retries, reconnects and overload behavior; add transport bounds before network exposure |
| Protocol compatibility | Initialize handshake, Go registry, generated JSON Schema/TypeScript | Verify compatibility and reconnect behavior, not just generated-file equality |
| Evaluation | Local runner/report/gate, Harbor adapter, and AppWorld MCP runner | Validate gate inputs, freeze experiment identity, connect completed results to release decisions |
| Tool safety | Descriptors, effects, profile policy and sandbox | Cross-tool conformance, dynamic MCP classification, failure-path tests |
| Delegation | Bounded leaf tasks, child transcripts, circuit breaker, and parent usage rollup | Measure review accuracy, duplication, cost, cancellation, and aggregate resource use |

An implementation entry means code exists, not that the feature is complete
or proven under every failure condition. In particular, a gate script is not
a CI release gate until the release workflow requires a compatible completed
result. See [evaluation](../evals/README.md).

## How to adopt an external idea

Record the problem, the concrete mechanism worth adopting, Kepler's boundary
conditions, and a testable success criterion. Compare behavior and evidence;
do not infer stronger durability or safety from architecture labels alone.
The [Chinese architecture site](../architecture-site/README.md) explains the
current mechanisms; dated comparisons and audits should retain their own
source revisions rather than becoming timeless product claims.

## 2026-10-02: recovery, delivery, and long-session costs

The following primary references informed this iteration (retrieved on
2026-10-02; their default branches and hosted documentation can change):

- [Codex App Server](https://learn.chatgpt.com/docs/app-server): explicit
  thread/turn/item lifecycles and authoritative completed items. Kepler's CLI
  now prefers durable final content, reserves a turn before its start request
  resolves, and rejects pending requests immediately on transport closure.
- [DeepSeek Harness core](https://github.com/deepseek-ai/deepseek-harness/blob/master/docs/subsystems/core.md):
  event-derived history and explicit ownership of agent lifetimes. Kepler keeps
  its existing Go dependency injection; private leaf sessions inherit the
  parent's cancellation context without acquiring a second session lease.
- [pi agent core](https://github.com/badlogic/pi-mono/blob/main/packages/agent/README.md):
  separation between model context and presentation, with distinct steering and
  follow-up delivery. Kepler keeps its existing routing choice, allows typing
  while busy, and serializes follow-ups across the start-acknowledgement window.

These are mechanisms adapted to Kepler's contracts, not a migration to another
harness or a claim of feature parity.

### Implemented invariants

| Boundary | Behavior | Regression evidence |
| --- | --- | --- |
| Model resilience | An open primary circuit can still use a healthy fallback; committed output remains non-retryable | `agent/model/recovery_regression_test.go` and existing committed-stream tests |
| Turn admission/replay | Start and initial input recover independently using stable IDs; retry after an ambiguous input commit does not duplicate the user message | `agent/runtime/recovery_regression_test.go` |
| Turn termination | Cancellation is terminal for queue acknowledgement; cancellation and completion update app-server phase under the same lock | Web/app-server recovery regressions and race suite |
| Context and cleanup | Every profile reserves configured output tokens; impossible budgets fail before model dispatch; terminal persistence has a default 10-second deadline | Runtime recovery regressions |
| Child ownership | A fresh private child does not consume its parent's bounded distributed-lease pool | Eight saturated parents complete eight child model calls in `agent/delegation/lease_test.go` |
| Local transcript | Offset/ID indexes avoid re-parsing historical messages per append; file locks preserve cross-process sequencing, and each write still syncs | Local store tests cover external append, replacement, truncation, duplicate IDs, cancellation, corruption, and concurrent writers |
| PostgreSQL JSON | One encoder replaces actual NUL escapes while preserving literal escapes and full integer precision | `infra/postgresjson` unit tests and fuzz seeds |
| Slack delivery | One scheduled/in-flight flush coalesces deltas; model callbacks do no network I/O; completion drains before final delivery | Blocked-network test asserts all 100 additional deltas arrive once, with at most one append |
| CLI interaction | Failed submissions preserve the draft; queued start failures pause without dropping later input; stale completions do not release another turn | Seven transport/component/hook regressions in `apps/cli/tests` |
| Web selection | Snapshots are bound to request and selection identity, and cannot replace newer live events | Three selection/recovery regressions in `surfaces/web/tests` |

Paths in the evidence column are relative to `packages/` unless otherwise
specified. No schema migration or new production dependency is required.

### Local append and search microbenchmarks

The local JSONL store uses offset/ID indexes to avoid re-parsing historical
messages per append while keeping file locks and `fsync` enabled. The cache
retains at most 64 session indexes, containing IDs and file offsets rather than
message bodies. Shared prompt layers (`system.md` + `general.md`) are kept
compact, prompt catalog publication is atomic under a single lock, and query
tokenization and regex compilation stay out of inner loops.

Run the Go storage and tool microbenchmarks from the source root with:

```sh
make bench
```

Raw benchmark result records, public dataset executions, packaging, and
deployment artifacts belong in the sibling `kepler-agent-deploy` repository;
this source repository retains only the benchmark code and harness contracts.

## 2026-10-07: context masking, lexical tool ranking, and surface parity

This iteration closed the remaining architectural gaps across the shared
runtime, provider catalog, tool discovery, MCP routing, and local/hosted
surfaces:

| Area | Implemented contract | Verification entry |
| --- | --- | --- |
| Two-stage observation masking | `BoundedProjector` folds historical `ToolResult` text and JSON blocks outside the most recent two complete exchanges into UTF-8 head/tail previews with original-evidence references before dropping whole turn groups or invoking LLM compaction | `agent/runtime/context_test.go` |
| Reasoning retention and prompt caching | Assistant prose accompanying tool calls is retained as text with a commentary phase, distinct from native reasoning and final-answer text; `prompt.Build` injects `---DYNAMIC_CONTEXT_BELOW---` between static and dynamic layers, enabling Anthropic incremental cache breakpoints | `agent/prompt/prompt_test.go`, `providers/providers_test.go` |
| Sub-agent circuit breaking and usage rollup | `agent-explore` enables `CircuitBreaker` (`MaxIdenticalFailedAttempts: 3`) for child turns and rolls child `model.Usage` up into the parent turn's `TurnResult.Usage`; `CircuitWarning` appends pre-trip self-correction hints to repeated tool errors | `agent/delegation/explore_test.go`, `agent/runtime/runner_test.go` |
| Provider catalog and shared circuit state | `providers/catalog.go` declares capabilities for ChatGPT plan slugs (`gpt-5.4`, `gpt-5.6-luna`, `gpt-6-luna`, `gpt-6.1-sol`, etc.) and API providers; `hosted.NewProfile` shares one `model.CircuitBreakers` registry across primary, router, compactor, and explorer clients | `providers/providers_test.go`, `profiles/hosted/profile_test.go` |
| Identifier-aware BM25 tool search | `agent/tool/search.go` replaces static bilingual synonym tables with field-weighted BM25 ranking and camelCase/snake_case sub-token indexing; `diagnostics` pseudo-tool is replaced by the `incident-diagnostics` skill | `agent/tool/search_test.go` |
| MCP router session caching and tool contracts | `tools/mcp` caches initialized MCP router sessions and tool definitions (5-minute TTL, invalidated on transport error), reducing per-call round trips from three to one; `luckin-create_order` enforces turn-scoped preview fingerprint verification; `git-search_ref` and `repo-search` default to literal `-F` matching | `tools/luckin/tools_test.go`, `tools/git` and `tools/mcp` tests |
| Local harness unification and fuzzy edit | `tools/local/bootstrap.go` unifies skill, MCP, and prompt assembly across `app-server` and headless CLI, registers the durable `memory` tool, persists `agent-explore` events to the session store, and adds whitespace-tolerant fuzzy matching to `edit` | `tools/local/tools_test.go`, `tools/edit/tools_test.go` |
| Web and Slack lifecycle alignment | Web enforces process-wide `MaxActiveTurns` (32), maps `TurnFailed` through `failure.PublicMessage`, collapses duplicate `plan` events, and renders live tool/plan progress in activity cards; Slack bypasses the secondary LLM router when `workflows.Registry.HasPromptCandidate` finds no workflow trigger | `surfaces/web` and `surfaces/slack/agent/service_test.go` |

## 2026-10-22: evidence-backed architecture review

The [October audit](agent-audit-2026-10.md) records the reviewed source revision,
reproducible context/ownership/credential/privacy fixes, official comparison
sources, and remaining design gaps. The [architecture deep dive](architecture-deep-dive.md)
explains the current ownership, recovery, context, artifact, memory, and disclosure
contracts. Native provider compaction, typed task state, semantic recall, and
tenant-fair scheduling remain proposals rather than completed capabilities.

## Slack quality criteria

Reviewed against the main runtime on 2026-10-23. Slack is the primary product
surface; a coding benchmark alone cannot measure its quality. These criteria
compare observable behavior, not feature counts or inferred implementation
quality. This review does not establish parity with, or superiority over, another
agent.

| Dimension | Existing mechanism | Required evidence / remaining gap |
| --- | --- | --- |
| Interaction | Explicit channel mentions, direct messages, typed plan events, native session lifecycle and Stop | Slow/erroring Slack must not stall the model; final state must not be lost; measure Stop-to-idle latency under load |
| Execution reliability | Durable inbox/session inputs, turn IDs, owner-checked claims, uncertain-effect handling | Fault-inject worker restarts, lost replies and concurrent controls; never turn an unknown side effect into an automatic repeat |
| Context and tools | Canonical transcript, checkpoint coverage, task-state revisions, deferred tool activation and evidence reads | Test long Slack threads with corrections, attachments and large tool results; measure constraint retention and evidence recall as well as tokens |
| Performance | Shared model governor, attempt receipts, prompt/context versioning | Compare queue wait, time to first delivered answer and total latency at fixed concurrency/model; a faster single CLI task is insufficient |
| Observability | `slack.reply` spans link user timestamps, delivery results and runtime work | Report p50/p95 by model/surface and separate provider failure, tool failure, delivery uncertainty and failed grading; do not count unknown delivery as success |
| Quality evaluation | Runtime/product regressions plus official Terminal Bench and AppWorld graders | Require the Slack regression gate; separately run versioned, representative live-model tasks with fixed budgets and independent expected outcomes |

`make eval-slack` provides the deterministic product-contract layer, including
real PostgreSQL/Redis control paths. It does not contact Slack or prove task
quality. Before claiming an experience improvement, run a fixed Slack scenario
set against the deployed version: multi-turn investigation with a correction,
image/file interpretation, a permission failure followed by reconnection,
concurrent users, cancellation during tool work, and resumption after a worker
restart. Record scenario/model/prompt-policy versions, outcome evidence,
attempts/tokens and delivery latency. Expected outcomes must be independent of
the agent answer; do not accept an LLM's own declaration of success as a grade.

Start with these failure paths before adding new orchestration or prompt rules.
Keep operational facts and user content separate: the UI projects lifecycle and
plan events, while canonical user/tool evidence drives the model. Keep a
one-to-one relationship between a confirmed side effect and its execution
receipt. These invariants are testable without guessing intent from text.

Primary references consulted for this review:

- [Slack agent sessions](https://docs.slack.dev/ai/agent-sessions/): lifecycle
  states and native stopping are platform contracts; loading text is not a
  substitute for a task plan.
- [Slack rate limits](https://docs.slack.dev/apis/web-api/rate-limits/): the
  Retry-After window applies per method/workspace, rather than per conversation.
- [pi agent core](https://github.com/earendil-works/pi/blob/main/packages/agent/README.md):
  typed streaming events, separate steering/follow-up queues, and explicit
  turn-finalization hooks are useful boundaries. They do not establish durable
  cross-worker delivery by themselves.

For Codex's thread/turn/item contract, retain the App Server reference above;
Kepler's Slack adapter should preserve the same explicit lifecycle boundaries
without importing CLI-specific interaction into Slack.
