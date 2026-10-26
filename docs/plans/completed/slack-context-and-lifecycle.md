# Slack context, discoverability and execution lifetimes

Status: completed. Source baseline eb4cc178; deploy baseline 1a9fde0.

## Outcome and acceptance

Use documented Slack APIs and the canonical runtime to improve interaction and
close reproducible admission/protocol failures. PR35–40 and PR42 merged after
exact-head CI and review. No second state store, dependency or schema was added.

## Work

- [x] Carry message-time viewing references through DM ingress, durable queue,
  steering and canonical input; retain text, reply identity and user permissions.
- [x] Publish localized native suggestions with agent_view semantics; preserve
  access checks, optional API failure behavior and no automatic task execution.
- [x] Keep Web capacity occupied through durable finalization and queued follow-up
  admission; verify held acknowledgements with actual PostgreSQL.
- [x] Bind A2A peer identity to actual credentials; fence rotation before dispatch.
- [x] Centralize conversation/directory cursor progress, reject cycles, follow
  empty pages and request only remaining message capacity.
- [x] Validate complete bounded Slack JSON receipts; preserve uncertain-write
  classification and omit private upstream error bodies.

## Decisions

Use each event's viewing snapshot; no ambient current-page cache or proactive
history fetch. References do not grant access. Reuse inFlight for admitted work
instead of creating another counter. Reuse Slack's documented cursor/response
contracts rather than page-size or JSON-prefix success heuristics. Old A2A
credential-unbound references require reconciliation, not receipt rewriting.

## Progress and evidence

Pre-fix regressions reproduced discarded viewing context, the missing native
suggestion API request, premature Web admission, A2A principal switching, cursor
cycles/overfetch and malformed success prefixes. Affected race suites and
make check passed. Combined runtime/delegation/A2A/Web/worker/memory races and
isolated PostgreSQL/Redis make integration-check passed after integration.

## Handoff

Configure Slack agent_view, subscriptions and assistant:write before live
acceptance. No live Slack/model quality experiment or production deployment.
Temporary isolated stores are stopped at the end of this work session. Source
and deploy changes are published before any quota wait (<10% remaining, check
at 30-minute intervals, no reset credits or manual resume).
