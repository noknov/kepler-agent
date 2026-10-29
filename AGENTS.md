# Repository map and working agreement

Kepler is maintained by coding agents. Keep decisions and verification in the
repository so a new session can continue without private chat history.

## Start here

- Product boundaries and request flow: [architecture](docs/architecture.md).
- Change method, simplicity, review, and completion: [maintenance](docs/maintaining.md).
- Commands and verification scope: [development](docs/development.md).
- Multi-session work: [execution records](docs/plans/README.md); inspect linked
  issues/PRs and Git status/log before starting overlapping work.
- Known gaps and their acceptance criteria: [maintenance backlog](docs/maintenance-backlog.md).
- Find a subsystem guide through the [documentation index](docs/README.md).

Start with `make doctor` and `make repo-check`. For a small change, state the
behavior and acceptance test in the task/PR. For a cross-boundary, persistent,
or multi-session change, maintain an execution record in its GitHub issue/PR.
Keep stable contracts in the owning guide. Reuse the owning
module; justify a new package, interface, dependency, or durable state first.

## Non-negotiable contracts

- `packages/agent` owns transport-neutral behavior. Profiles compose adapters;
  surfaces own ingress/delivery. Read scoped AGENTS.md files in changed areas.
- Server capability policy is authoritative. No end-user approval grants host
  access. Write tools declare `tool.Descriptor` effects and require the exact
  operator allowlist entry; surface wording never grants authorization.
- Commands use argv execution within configured workspace roots.
- Runtime code performs no DDL. Update `schema/postgres.sql` and the incremental
  migration in `kepler-agent-deploy`, with an explicit rollout order.
- Durable ownership transitions check the current owner. Run items/feedback are
  append-only; growing step arrays do not belong in `agent_runs` payloads.
- Fix failed checks at their cause. Do not weaken checks, refresh a baseline,
  add exemptions, or delete acceptance tests to make a change pass. A deliberate
  contract change must update the rationale and negative regression together.

## Finish with evidence

Run `make fmt-check vet` and relevant tests while iterating. Broad changes need
`make check`; concurrency changes need relevant race tests. Run the affected
integration/UI checks described in the owning guide and report skipped scopes.
Stage new files, then run `make change-report BASE=<verified-base-ref>` to expose
new packages, dependencies, and growth for review. Read the diff and failures;
passing checks alone do not establish product quality or authorize deployment.
