# Development

Tool constructors declare effects explicitly. `FunctionDescriptor` does not
default missing effects to read access and does not load product prompts.
Hosted/local composition applies descriptor wording through
`packages/toolkit/toolprompts`; the canonical core cannot import product prompts.
Argument-dependent tools implement `tool.CallDescriptor` so policy and scheduling
resolve effects before execution. Descriptor wrappers forward that contract and
turn cleanup. Wording overlays cannot alter effects or concurrency.

Start in the source repository. Packaging and environment rollout are separate
operational concerns and are not documented here.

## Prerequisites

- Go: use the version in [go.mod](../go.mod).
- Frontend: Node.js 20 and pnpm 10, matching [CI](../.github/workflows/check.yml).
- Tool tests: install ripgrep and Git. Some tests require loopback networking.
- Local sandbox behavior: macOS Seatbelt or Linux bubblewrap.
- Integration checks: use disposable PostgreSQL/Redis with the required schema;
  check the relevant test's environment requirements before running it.

Install frontend dependencies from the source root:

```sh
pnpm --dir apps/cli install --frozen-lockfile
```

## Verification

| Change | Check | What it establishes |
| --- | --- | --- |
| Go implementation | `make fmt-check boundaries vet` and relevant `go test` packages | Formatting, layering, static analysis, tested behavior |
| Broad source change | `make check` | Root Makefile checks, including generated protocol drift and evaluator dry-run |
| CLI protocol/client | `pnpm --dir apps/cli typecheck` | Protocol-focused TypeScript scope only |
| CLI/Web interaction | `make test-ui` | Submission, disconnect, draft retention, queue ordering, and conversation request races; deterministic UI boundary mocks |
| CLI bundle | `pnpm --dir apps/cli build` | Bundling succeeds; not a complete interaction test |
| Full CLI/vendor types | `pnpm --dir apps/cli typecheck:vendor` | Broader TypeScript diagnostics; inspect separately from the narrower gate |
| Concurrency | `make test-race`, plus affected packages outside its list | Races exercised by those tests |
| Evaluator changes | `make eval-check` | Runner/report wiring and evaluator unit tests, not model quality |

For example, when modifying app-server or Web concurrency, include those
packages explicitly; the default race target does not cover every package:

```sh
go test -race ./packages/appserver ./packages/surfaces/web
```

A passing command is evidence for its tested scope only. Tests gated on
external dependencies may skip. Inspect skip/failure output, and verify real
sandbox and database behavior when those contracts change.

`make build` compiles service and CLI entry points to `/dev/null`. To obtain
runnable bundles, follow [local CLI packaging](local-cli.md).

## Change a contract

- **Runtime:** keep canonical events append-only and surfaces out of the loop.
- **Tool:** declare effects, schema, execution limits, and concurrency behavior;
  check both profile policy and the actual filesystem/network boundary.
- **App-server:** update the Go registry, run `make protocol-generate`, then
  `make protocol-check`; review JSON Schema and TypeScript changes together.
- **Database:** update [schema/postgres.sql](../schema/postgres.sql), prepare the
  corresponding incremental migration, and document upgrade ordering. Runtime code
  must not perform DDL.
- **Prompt:** change the appropriate layer in [prompts](prompts.md), then test
  representative behavior rather than only checking that a string exists.
- **Documentation:** update the owning guide and its links; do not duplicate
  deployment recipes or declare a proposal implemented without evidence.

## Evaluation versus tests

Use deterministic tests for permissions, lifecycle transitions, replay, and
protocol boundaries. Use fixed model-driven tasks for behavior and quality.
Public coding benchmarks run through Harbor's native grader. Hosted workflow
and presentation checks need separate coverage. See [evaluation](../evals/README.md).

### Connection revision upgrade

Before deploying versioned credential refresh, apply
`schema/migrations/20261024_connection_revision.sql` to the existing PostgreSQL
store (new stores use `schema/postgres.sql`). Stop old workers before enabling
new workers: old unconditional refresh writers cannot participate in revision
checks. Each authorization, refresh, or account enrichment receives a fresh
revision. A conditional update conflict fails the current credential request;
it does not replay the OAuth refresh or return the stale response. A subsequent
request loads the current authorization. File stores use the same contract,
with an OS lock around the entire transaction and atomic file replacement.

Deferred tool discovery expires at turn cleanup. Pending approvals instead bind
the immutable call (arguments and scope) to its persisted descriptor. Resumption
requires a registered, enabled tool with that same descriptor and a fresh policy
decision. Change `Descriptor.Version` when a target binding or implementation
changes the operation without changing its schema. Contract changes require a
new approval; a removed or disabled tool can always be declined. Legacy approvals
without a descriptor retain active-tool lookup and cannot restore deferred tools.

Session bootstrap records `SessionStarted` as intent and atomically commits
imported history with `SessionInitialized` through `transcript.BatchStore`.
Embeddings that supply nonempty external history must implement that atomic
contract. A loader error or failed transaction leaves initialization retryable;
a committed marker prevents refetching after a lost commit response. Existing
sessions with canonical input, a started turn, or a compaction checkpoint remain
authoritative. An old partial import without a turn boundary can resume only
when its durable prefix exactly matches the loaded history.
