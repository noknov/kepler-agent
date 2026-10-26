# Slack context, discoverability and owned execution lifetimes

Status: active. Source baseline eb4cc178; deploy baseline1a9fde0.

## Outcome and acceptance

Improve useful Slack interaction and reliability using documented Slack APIs and
existing canonical runtime boundaries. Prefer a reproducible defect or concrete
interaction over framework additions. User authorizes continued improvements,
PRs and best-practice refactoring. At five-hour allowance remaining <10%, publish
work and keep this loop waiting, checking every30 minutes until allowance recovers.
No manual continuation confirmation or reset-credit spending.

## Work

- [x] Preserve message-time Slack viewing references through DM ingress, durable
  queue, steering and canonical user input without new tracking/cache state.
- [x] Add native suggested prompts that reflect supported interactions and retain
  access checks, optional API failure behavior and agent_view wire semantics.
- [ ] Audit ownership/cancellation/admission with special attention to work after
  model execution; reproduce concrete reliability/resource defects before fixes.
- [ ] Review the complete diff, relevant race/integration tests, make check and
  exact-head CI; publish reviewable PRs and update owning guides.

## Decisions

Official Slack developing-agents guidance says message.im carries app_context
when subscribed to app_context_changed. Use that event snapshot, not a mutable
user-to-current-channel cache or repeated channel history fetch. Context is an
untrusted reference, not content or permission. Read actual content only through
existing user-token tools. Support the documented channel entity first; do not
guess schemas for canvas/thread/list entities or another workspace.

Keep transport metadata in the Slack surface. Append bounded supported context
as a canonical JSON content block beside the user's unchanged text, so existing
budgeting, source events, queue/recovery and steering own its lifetime. No runtime
SQL schema or second execution loop is required.

## Progress and evidence

2026-10-27: clean source main matches GitHub; doctor/repository checks passed.
Reviewed official OpenAI platform/App Server, Slack agent/context/sessions and
suggested-prompts references. The source still discards message.im app_context;
Messages-tab suggested prompts are a stub. No live Slack or model claim.

## Handoff

Message-time context implemented. The ingress regression failed on the previous
source: valid message.im app_context disappeared. Tests now cover original text
and reply identity, foreign/unsupported references, no stale ambient fallback,
serialized queue recovery and steering. Slack client/conversation/handler/agent
race suites passed. PR35 published; local make check passed. Await CI/review before merge.
 Temporary integration
stores from the prior run are stopped; restart only isolated stores if needed.
Native suggestion milestone: prior Messages-tab handler made no API call;
black-box regression failed before implementation. New tests verify agent_view
wire shape without thread_ts, explicit channel placeholders, authorization,
Chinese/default catalog content, clearing stale context, and optional API failure
metrics without launching work. Affected races passed; finish make check and
publication. Next investigate admission during durable finalization, where an
execution-only active map may release capacity too early.

Source and deploy changes must be published before allowance waits.
