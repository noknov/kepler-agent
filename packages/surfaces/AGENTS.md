# Product adapters

Follow [architecture](../../docs/architecture.md), then the owning
[Slack](../../docs/slack.md) or [Web](../../docs/web.md) guide.

- Surfaces authenticate/admit input and project canonical events. Do not infer
  authorization, completion, or plans from free-form model text.
- Ingress acceptance, executing actor, runtime completion, network delivery,
  durable receipt, and queue acknowledgement are separate boundaries. Check
  actor and claim ownership where input is actually consumed.
- Slow presentation must have bounded pending state and must not block model
  execution. Final delivery and receipt persistence need explicit budgets.
- Keep ambiguous writes uncertain. Retry only when rejection or downstream
  idempotency makes replay safe. Redis wakeups are hints, not durable acceptance.
- Extend the existing deterministic service/control tests; run `make eval-slack`
  with isolated PostgreSQL/Redis for Slack durable control changes. Verify Web
  interactions with their existing Node test suite and the actual UI when changed.
