# Bind tool replay to immutable model calls

Status: completed. Baseline main35983ef5; implementation PR41 merged after CI148.

## Outcome and acceptance

Reject empty/duplicate tool IDs and retargeted durable IDs before tool policy,
side effects or executable assistant persistence. Reuse equivalent results
without repeating writes, child usage or plan/task-state transitions.

## Work

- [x] Reproduce malformed batches with the existing runtime/model/transcript
  fixtures and counting external-write tool: two writes occurred before the fix.
- [x] Validate whole batches before persistence; bind completed IDs to original
  tool names and exact JSON arguments using shared approval comparison logic.
- [x] Preserve JSONB key/number normalization and legacy absent ExecutionID.
- [x] Verify a fresh sibling cannot execute ahead of a conflicting replay;
  preserve exact large integers and avoid repeated accounting/state transitions.
- [x] Run affected races, make check and exact-head implementation CI; add real
  PostgreSQL round-trip and terminal-replay coverage in the final evidence PR.

## Decisions

No ID repair, new registry/cache/schema or floating-point argument comparison.
Existing RunTurn ownership restores authorization scope; transient prompt hash
is not model call identity. Fresh actions require fresh IDs. Protocol failures
are content-free and non-retryable at this execution boundary.

## Progress and evidence

Runtime/delegation/hosted/local race suites and broad checks passed. Real PG
regressions prove equivalent reordered/normalized JSONB reuses evidence, a
one-unit change above float64 precision is rejected, and writes/usage remain
unchanged after terminal replay. Combined isolated PG/Redis integration passed.

## Handoff

External APIs still need their own idempotency/reconciliation. No real-model,
production restart/load experiment or deployment was performed.
