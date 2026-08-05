# Repository Working Agreement

## Scope

`packages/agentcore` and `packages/agentprotocol` contain transport-neutral
agent behavior and lifecycle contracts. Slack, app-server, HTTP, CLI, and
PostgreSQL packages are adapters. Do not introduce Slack, deployment, or wire
transport assumptions into the core packages.

## Safety

- Server-side capability policy is authoritative. Do not add end-user approval
  prompts for access to the server host.
- New write or external-write tools must declare `ToolMetadata` and remain
  disabled until their exact name is added to the operator allowlist.
- Never treat a surface annotation as write authorization.
- Commands must use argv execution without a shell and stay within configured
  workspace roots.

## Persistence

- Keep runtime code free of DDL. Update `schema/postgres.sql` when the current
  PostgreSQL contract changes and document any operator action explicitly.
- Durable queue ownership changes must check the current claim owner.
- Run items and feedback are append-only. Do not put growing step arrays back
  into the `agent_runs` aggregate payload.

## Verification

Run `make fmt-check`, `make vet`, and the smallest relevant `go test` package
set while iterating. Run `make check` before handing off broad changes. Tests
that bind loopback ports may require a normal local or CI environment.
