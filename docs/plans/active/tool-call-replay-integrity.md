# Bind tool replay to immutable model calls

Status: active. Baseline main35983ef5 (PR39 merged).

## Outcome and acceptance

Reject empty/duplicate tool IDs and retargeted durable IDs before any tool policy,
side effect, or executable assistant transcript write. Valid replay preserves
JSONB representation changes without losing integer precision, recharging child
usage, or reapplying state transitions. Keep canonical runtime as the sole owner.

## Work

- [x] Reproduce malformed batches and mismatched replay using the existing runtime,
  scripted model, memory transcript and counting external-write tool.
- [x] Validate complete batches before assistant persistence; bind durable reuse
  to tool name and JSON arguments using existing exact JSON equality semantics.
- [x] Preserve tool replies on replay without repeating usage/state transitions.
- [ ] Complete affected races, make check, diff review and exact-head CI; publish
  and merge the reviewed change, then run combined isolated integration checks.

## Decisions

No provider-specific ID repair or new registry/state/cache. Session/turn scope is
restored by existing RunTurn ownership logic. Transient prompt hash and legacy
missing ExecutionID do not change a model call's identity. Reuse the approval
contract JSON comparison (exact numbers, ordered arrays, unordered object keys)
instead of floating-point or raw-byte comparison. Existing completed-result scan
owns replay validation; move it out of dispatch so invalid model messages never
enter executable canonical history. Fail with content-free protocol diagnostics.

## Progress and evidence

2026-10-27: before fix, empty and duplicate IDs each dispatched two external writes;
changed names/arguments/large integers reused another call's result. Negative
regressions failed. Affected runtime/delegation/hosted/local race suites passed
initial identity fix. Final accounting regression, affected races and make check passed. Tests cover a
fresh sibling write before a retargeted durable ID, exact integers beyond
float64 precision, JSONB equivalence and no repeated usage/plan transition.

## Handoff

Finish checks/publication before allowance waits (<10% remaining; poll every
30 minutes, no reset credits or manual resume). No schema/dependency changes.
No real model, live Slack or production deployment performed.
