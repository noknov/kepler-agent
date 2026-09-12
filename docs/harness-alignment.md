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
