# Development

Start with [maintenance](maintaining.md) and [architecture](architecture.md).
Slack is the maintained product; `benchmark/cmd/kepler-bench` is the single-shot
evaluation adapter. There are no frontend bundles or app-server code generators.

## Prerequisites

Use Go from [go.mod](../go.mod), Python 3.11+, Git and ripgrep. Node.js 20+ runs
the observability page tests; there is no pnpm application build. Local sandbox
integration uses macOS Seatbelt or Linux bubblewrap. Database tests require
explicitly configured isolated stores.

## Verification

| Scope | Command | Evidence |
| --- | --- | --- |
| Environment and repository contracts | `make doctor repo-check` | Tools, dependency direction, runtime DDL checks and documentation links |
| Formatting and static analysis | `make fmt-check vet` | Go formatting and vet |
| Complete source checks | `make check` | Go tests/builds and evaluator wiring |
| Concurrent execution/control | `make test-race` | Affected races exercised by regression tests |
| Durable hosted behavior | `make integration-check` | Real isolated PostgreSQL/Redis, connection/hosted race tests and Slack regressions |
| Observability UI | `make test-ui` | Browser interaction logic with deterministic fixtures |
| Structural review | `make change-report BASE=<base-ref>` | Staged additions, deletions, packages and dependencies |

`make eval-slack` refuses missing `POSTGRES_TEST_DSN` / `REDIS_TEST_URL` instead
of silently skipping durable control tests. Set `SANDBOX_INTEGRATION=1` for actual
OS sandbox tests. Always report skipped platform, store and live-service scopes.
Passing mocks or health checks does not establish Slack/model end-to-end quality.

`make build` compiles gateway, worker, observability and the benchmark adapter.
Deployment and packaging belong in kepler-agent-deploy. See the
[benchmark contract](benchmark.md) and [evaluation guide](../evals/README.md).

## Changes to durable state

Runtime code performs no DDL. Keep fresh-install schema in
[schema/postgres.sql](../schema/postgres.sql), add an incremental migration in
the deploy repository and verify both fresh installation and upgrade.
Do not change checksums of applied migrations. Retired browser tables and old
conversation data remain preserved for explicit archival/rollback decisions.

Tools declare exact effects. Operator policy remains authoritative and writes
require requester confirmation. Descriptor/preparation wrappers preserve scope,
call-specific effects, immutable approval state and cleanup. Deferred discovery
is turn-scoped; resumed approvals recheck the registered descriptor and current
policy rather than restoring arbitrary tools from model text.
