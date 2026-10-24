# Canonical agent core

Read [runtime contracts](../../docs/runtime.md) for execution changes and
[architecture](../../docs/architecture.md) for ownership boundaries.

- Model, prompt, and task-state contracts are below tools and transcripts;
  runtime composes them; delegation uses runtime. `scripts/repocheck` enforces
  direct dependency direction, including tests and platform-specific files.
- Keep identity, policy decisions, canonical events, cancellation, and accounting
  explicit. Product text, OAuth providers, SQL/Redis, and Slack/CLI wire shapes
  belong in adapters behind canonical interfaces.
- Before adding recovery state, define its durable source, owner, interruption
  window, and idempotency rule. A timer or nonempty transcript is not proof that
  a durable transition completed.
- A delegation/wrapper layer must own a real contract. Tool wrappers preserve
  call-specific descriptors, lifecycle cleanup, policy effects, and accounting.
- Add regression tests at the failed state transition, including restart,
  cancellation, duplicate submission, or ambiguous external effects as relevant.
  Reuse existing transcript/model fixtures instead of constructing a second loop.
