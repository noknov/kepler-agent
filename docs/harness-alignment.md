# Codex-aligned harness plan

This document records the target architecture for Kepler Agent. It treats the
OpenAI Codex harness as the primary reference because both products need one
agent loop across several clients. DeepSeek Harness and Pi are secondary
references: they provide useful extension and telemetry patterns, but neither
matches Kepler's hosted, policy-authoritative operating model.

## Architectural position

Kepler already has the right durable boundary:

```text
surfaces (Slack, Web, CLI) -> product profile -> shared runtime
                                             -> canonical transcript
                                             -> rebuildable projections
```

The runtime owns turn lifecycle, context projection, tool scheduling,
termination, and the canonical event record. Profiles own provider resilience,
storage, policy, credentials, and surface-specific delivery. New features must
not put Slack, browser, deployment, or provider wire assumptions into
`packages/agent`.

## Comparison and decisions

| Concern | Codex | Kepler current state | Decision |
| --- | --- | --- | --- |
| Multi-client runtime | One core thread per conversation, exposed through a stable app-server event protocol | One shared Go runtime; local app server and hosted surfaces are adapters | Keep the shared runtime; version the app-server contract and treat it as a public product boundary. |
| Backpressure | Bounded protocol queues and retryable overload response | Delta batching and async projections exist; accepted local turns previously had no process-level bound | Bound active app-server turns and return `-32001`; clients retry with jitter. |
| Trajectory | Thread lifecycle is durable and clients render stable items | Append-only transcript, plus run/step projection | Keep transcript as source of truth. Add a projection-independent trajectory inspector before adding another state store. |
| Extensibility | Skills and MCP join a consistent policy model | Tool catalog, skills, MCP, profiles, capability policy | Extend those seams only. Do not make the loop, policy, durable queue, or UI into arbitrary plugins. |
| Evaluation | Product surfaces share a harness but require product-specific verification | Black-box CLI evaluator and Harbor adapter | Add explicit release gates; keep public benchmarks in Harbor and hosted-Slack tests in a separate suite. |
| Telemetry | Rich thread/item events drive both UI and operations | OTEL spans, canonical events, and durable runs existed but used disconnected trace IDs | Persist W3C trace context at turn start and project it into runs. Then link individual model/tool spans. |

DeepSeek's append-only, replayable trajectory is aligned with Kepler and should
be adopted as a query experience. Its all-plugin kernel is intentionally not a
target: plugin ownership of sessions, persistence, loops, scheduling, and
policy makes production invariants harder to audit. Pi's separated agent-core,
provider API, and telemetry contracts are useful, but its default process
permissions are unsuitable for a hosted agent.

## Five delivery tracks

### 1. Trace and trajectory

Completed: the runtime persists a typed W3C trace context on canonical events;
the hosted run projection reuses the root trace and records actual model/tool
span IDs on run steps. This survives projection replay and links `/runs` to
OTEL and Langfuse.

Completed: `thread/trajectory` derives a read-only, redacted operational view
from the same transcript. Do not persist streamed token deltas or secrets as
trajectory facts. Next, add tool-catalog version and redaction-policy revision
to every turn so historical trajectories remain precisely interpretable.

### 2. Evaluation and release gates

Completed: `evals/gate.py` can gate selected candidates on weighted pass rate,
timeout rate, p95 duration, and allowed regression from a compatible baseline.
It is intentionally evaluated after a pinned, black-box run rather than
importing runtime code.

Next: define a versioned production regression suite from incident transcripts.
Run it alongside Harbor in CI with pinned candidate version, model gateway,
task image, source revision, attempts, and concurrency. Report category and
tag matrices; never promote only an aggregate score.

### 3. App-server protocol resilience

Completed: local app-server limits active turns and returns retryable JSON-RPC
`-32001` when saturated. Delta batching remains non-durable presentation work.

Completed: the handshake declares its exact supported protocol range and the
TypeScript client rejects incompatible ranges. Next: publish generated JSON
schema artifacts from Go types before supporting third-party clients. A client
reconnect must use `thread/resume` from the last transcript sequence rather
than recover state from its terminal view.

### 4. Safety and runtime contracts

Keep server-side capability policy authoritative. Every external-write tool
must have metadata and an exact operator allowlist entry; app-server/UI labels
are never authorization. Keep argv-only execution, workspace roots, sandboxing,
and the existing uncertain-tool-call recovery rule.

Next: add conformance tests that execute every registered tool descriptor
against hosted and local profiles, asserting capability effect, approval mode,
timeout, and parallel-safety. Fail registration when metadata is incomplete.

### 5. Codex alignment review

Adopt Codex's stable event-oriented client contract and bounded transport,
while retaining Kepler's stronger hosted durability. Do not copy Codex internal
implementation details or protocol naming blindly. The interoperable unit is a
thread/turn/item lifecycle and replay cursor, not a provider-specific tool
payload.

## Existing design issues to address

1. **Run/trace joins were lossy.** Fixed for the root turn trace. Model and tool
   projection step IDs are still event IDs, not their actual OTel span IDs.
2. **App-server admission was unbounded.** Fixed for active turns. Inbound
   parsing is sequential today; if a network transport is added, it needs a
   bounded request queue before concurrent dispatch.
3. **Trajectory reconstruction is implicit.** The facts exist in the
   transcript, but operators need a deliberate read model and redaction rules
   rather than manually decoding events.
4. **Evaluation has measurement but no policy.** The new gate provides policy
   enforcement; baselines and incident-derived suites remain operator work.
5. **Protocol compatibility is declared but not machine-enforced.** `v2` is a
   string today. Add generated schema and compatibility tests before external
   clients are supported.

## References

- OpenAI, [Unlocking the Codex harness](https://openai.com/index/unlocking-the-codex-harness/)
- OpenAI, [Codex App Server protocol](https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md)
- DeepSeek, [DeepSeek Harness](https://www.deepseek.com/harness/en/)
- Pi, [Pi Agent Harness](https://github.com/earendil-works/pi/blob/main/README.md)
