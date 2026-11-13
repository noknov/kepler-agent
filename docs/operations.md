# Operations

This guide explains application signals and diagnosis. Image publishing,
environment rendering, migrations, and restart commands depend on the target
environment and are intentionally out of scope.

## Health and run inspection

| Endpoint | Purpose |
| --- | --- |
| `GET /livez` | Process liveness |
| `GET /readyz` | Dependency readiness; fails during drain |
| `POST /drain` | Direct-loopback shutdown signal |
| `GET /health/dashboard` | Tool/service dashboard |
| `GET /health/tools` | Tool health JSON |
| `GET /metrics` | Durable run and cost metrics |
| `GET /runs?limit=20` | Recent runs |
| `GET /runs/<run_id>` | Run detail |

Health endpoints belong to the respective service. Run/metric/dashboard
endpoints are served by observability; do not assume they are exposed through
the public gateway. Protected observability endpoints accept
`Authorization: Bearer <token>` or `X-Kepler-Agent-Admin-Token: <token>` matching
`OBSERVABILITY_TOKEN`. `OBSERVABILITY_ALLOW_UNAUTHENTICATED=true` is for direct
loopback development only.

Use readiness to establish dependency connectivity, then verify a representative
turn. Neither readiness nor a health dashboard proves that all user integrations
are authorized or every tool can complete.

## Diagnose by symptom

| Symptom | Inspect first | Next decision |
| --- | --- | --- |
| Service never becomes ready | Startup error, PostgreSQL/Redis reachability, schema version, required configuration | Correct configuration or migration before restart |
| Slack accepted an event but no answer | Inbox claim/retry/dead-letter state, run termination, delivery error | Determine whether execution or delivery failed before replay |
| Turns wait without progress | Active sessions, connection-pool occupancy, locks, provider timeouts, child-agent fan-out | Resolve the constrained resource; do not blindly raise every concurrency limit |
| Model calls fail | Provider/protocol/model selection and typed error | Verify credentials and request compatibility; inspect bounded retries |
| Cost appears zero | Configured rates and recorded usage | Compare provider billing; zero configured rates mean zero estimate |
| Old code appears in a read | Requested snapshot/ref versus explicit working-tree view | Check [workspace snapshot rules](configuration.md#workspace-snapshots) |

Collect session/turn/run IDs, source/image revision, timestamps, termination
reason, and relevant redacted logs. Do not export credentials or full private
prompts into an incident report.

## Retry and ownership

Slack ingress is persisted to a PostgreSQL inbox before worker processing.
Claims use owner and lease fields, renewal, bounded retries, and dead letters.
Session inputs are also persisted; Redis wakeups are not the durable queue.

Treat processing as at least once across systems. Before manually retrying an
uncertain external write, check the upstream outcome and transcript result.
Do not clear owner/lease fields or delete inbox rows as routine recovery.
Investigate malformed and exhausted events rather than replaying them forever.
See [safety and limitations](safety.md).

Slack intake acknowledges durable admission rather than waiting for a model run.
A bounded executor claims inputs only when capacity is available. Its one-minute
claim is renewed independently of the model deadline; Redis active-session hints
use the same short lifetime. Orphan steering is promoted only after its parent
claim expires. Event retries wake at their persisted deadline; periodic scans
recover lost wakeups. Exhausted session inputs remain dead-lettered and visible.

Slack reply receipts record create intent before sending and preserve known
message IDs across restarts. An ambiguous create is marked uncertain instead of
posting another answer. Terminal session status is reconciled while holding
canonical session ownership, so an old turn cannot clear its successor's status.
`kepler_agent_uncertain_replies` indicates ambiguous message creation.
`kepler_agent_unsettled_session_status` also includes active work; inspect the
canonical turn before treating it as an error.

## Shutdown

A drain makes readiness fail and fences new conversation execution. Ingress and
running turns share the `HTTP_SHUTDOWN_TIMEOUT` grace deadline. Waiting observes
execution through durable acknowledgement or release. When grace expires, the
worker cancels service work. `Service.Close` gives background work and bounded
Slack finalization a shared 40-second join budget before closing stores.
Container stop settings must allow cleanup in addition to the grace period.
An arbitrary adapter or PostgreSQL pool close cannot be forcibly interrupted in
process; long turns can still be interrupted by container termination.

The local deployment replaces containers by stopping the old instance before
starting the new one. Expect an interruption window. It does not provide
rolling availability or automatic rollback. Keep application shutdown and
container stop settings aligned in deploy configuration.

## Schema and release coordination

[Schema contract](../schema/postgres.sql) describes the current fresh-install
schema. Application processes validate required storage but do not execute DDL.
The release workflow owns incremental migrations and their checksum tracking.
Do not use the fresh-install schema as an undocumented incremental upgrade.

Before a schema-dependent release, inspect pending migrations and compatibility,
verify a recoverable backup, apply the incremental migration, then restart the
matching application revision. Database rollback is a separate operation from
reverting an image; do not assume old code can read a newer schema.

## Logs, traces, and costs

See [observability configuration](observability.md) for OTLP, Langfuse, and
cost rates. Trace export intentionally omits prompt/result content; raw service
logs and evaluator artifacts need their own access and retention controls.
