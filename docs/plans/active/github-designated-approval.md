# GitHub approval by a designated operator

Status: active.

## Outcome and acceptance

Authorized Slack users may request PR approval using the operator's existing
GitHub credential. Only the configured approver may confirm or decline. The
confirmed PR head and credential binding cannot change before execution. Other
GitHub review actions and merging are outside this change.

Acceptance: runtime authority/replay tests, GitHub controlled-HTTP tests, Slack
card/interaction tests including original-thread binding, affected race suites,
make check, and deployed readiness/metrics. No live approval is sent during tests.

## Work

- [x] Preserve designated approval authority in the canonical policy decision.
- [x] Validate authority before queue admission; retain requester execution scope.
- [x] Mention the approver in the original thread; restrict button visibility.
- [x] Add one exact external-write tool, pinned to PR head and credential.
- [ ] Configure the verified operator identity, validate, publish, and deploy.

## Decisions

Extend existing runtime approvals and Slack controls rather than adding a second
approval store or interpreting model prose. Policy owns the approving principal;
the tool owns GitHub API validation and uncertain-write behavior. The authenticated
clicker is distinct from the requesting user whose turn resumes. Persist the
original session in controls and resolve all authority from the transcript.

## Progress and evidence

Baseline 8d3322e2 has passing GitHub Check CI. Deployment schema 000–018 is current.
Local check reproduced malformed JSON in ignored private/runtime.json after the
new strict catalog loader; the extra trailing comma was removed without weakening
loading. Existing SOPS GitHub credential resolves to the operator's personal
account. Slack operator identity was resolved through the connected account.

Final behavior keeps every control in the original thread, using Slack's current
`visible_to_user_ids` field plus independent backend authority checks. Cross-user
private conversations are denied by policy before requesting confirmation;
the operator's own DM remains available. No DM forwarding was implemented.

Local make check passed. make integration-check passed against dedicated
temporary PostgreSQL/Redis, including the durable Slack race gate. Additional
race suites passed for runtime, GitHub, hosted catalog and configuration. Tests
cover approval/decline, wrong principals, authority and credential rotation,
changed heads, unknown write receipts, scope preservation and control origin.

## Handoff

Implementation and deterministic verification are complete. Publish the source
and deployment configuration, then verify readiness and metrics. Do not exercise
a real APPROVE API call as a smoke test. Deployment uses the existing 000–018
schema; the existing operator token stays in SOPS.
