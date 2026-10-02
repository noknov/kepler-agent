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
| Evaluation | Local runner/report/gate and Harbor adapter | Validate gate inputs, freeze experiment identity, connect completed results to release decisions |
| Tool safety | Descriptors, effects, profile policy and sandbox | Cross-tool conformance, dynamic MCP classification, failure-path tests |
| Delegation | Bounded leaf tasks, child transcripts and parent links | Measure review accuracy, duplication, cost, cancellation, and aggregate resource use |

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

## 2026-10-08: recovery, delivery, and long-session costs

The following primary references informed this iteration (retrieved on
2026-10-08; their default branches and hosted documentation can change):

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

### Measured local append cost

Baseline: `d47061c5453894cda823b53d984943469e937839`. Both versions ran the same
`BenchmarkJSONLAppend` on an Apple M3 Pro, macOS arm64, with 1 KiB message bodies,
20 measured appends per sample and three samples. History was loaded before
timing, matching the runtime's load-then-append flow. `fsync` remained enabled.
The baseline store was selected with a Go source overlay, excluding the new
index implementation and its implementation-specific tests.

| Existing events | Before, median ms/append | After, median ms/append | Before, median bytes allocated/append | After, median bytes allocated/append |
| ---: | ---: | ---: | ---: | ---: |
| 100 | 6.216 | 4.841 | 465,336 | 3,391 |
| 1,000 | 10.202 | 4.257 | 3,899,715 | 3,592 |
| 10,000 | 59.073 | 4.374 | 41,593,066 | 3,592 |

Raw [before](benchmarks/2026-10-08-jsonl-before.txt) and
[after](benchmarks/2026-10-08-jsonl-after.txt) samples are retained. Re-run the
current implementation with:

```sh
GOCACHE="$PWD/.cache/go-build" go test ./packages/profiles/local \
  -run '^$' -bench BenchmarkJSONLAppend -benchtime=20x -count=3
```

At 10,000 events this is approximately 13.5 times faster. Cold index building
and full history loads remain linear; atomic batch replacement still copies
existing bytes. The cache retains at most 64 session indexes, containing IDs and
file offsets rather than message bodies. Memory within one index grows with its
event count. This microbenchmark measures local storage, not model quality,
end-to-end provider latency, PostgreSQL throughput, or power-loss recovery.

### Verification and next measurements

This iteration was checked with `make check`, `make test-race`, `make test-ui`,
and the CLI protocol typecheck/bundle. UI tests execute the real owned submission
and transport code with deterministic rendering/DOM boundaries; they are not a
visual terminal or browser end-to-end test. Live PostgreSQL/Redis, multi-pod
routing, and real provider evaluations were not run locally. CI retains the
existing PostgreSQL/Redis/Linux sandbox checks and now runs the UI regressions
and local-store/delegation race tests.

Further work should be driven by production evidence: aggregate model/tool
admission across surfaces, Web multi-pod event routing/cancellation, incremental
run-summary projections, atomic session-history initialization, and narrowing
the CLI's imported UI surface. The broader vendor TypeScript scope still reports 1,949 pre-existing diagnostics
(all in `src/cc`, with none in owned source during this check); the protocol-only gate is not a substitute for removing
that debt. These are separate contracts and should not be hidden behind a
wholesale plugin-framework rewrite.
