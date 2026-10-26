# Remote agent delegation (A2A)

Code review is one workflow on top of shared local delegation. A2A is an
independent transport adapter for cross-system agents; it does not replace the
canonical runtime or turn every local reviewer into a network service.

## Configured peers

CLI and app-server read operator-selected peers from local config:

```toml
[[remote_agents]]
name = "research"
url = "https://agent.example/a2a"
token_env = "RESEARCH_AGENT_TOKEN"
timeout_seconds = 60
max_response_bytes = 1048576
```

Hosted profiles read the same fields from the `A2A_AGENTS` JSON array. Tokens
stay in named environment variables and are read when dispatching. Configuration
contains no token values. Registration validates names, unique targets, resource
budgets, and absolute HTTPS endpoints; HTTP is allowed only for loopback.
Credentials, query strings, and fragments are forbidden in endpoint URLs.
Redirects and attempts to dispatch outside the configured endpoint fail.

Each peer registers a deferred `agent_remote_<name>` tool. Use `tool_search` to
discover it. `send` accepts a complete task with role, objective, boundaries,
deliverable and success criteria. Only that explicit assignment leaves the
process: local identity, workspace, credentials, tool catalogs and transcripts
are not implicitly shared. An assignment can itself disclose sensitive facts;
operators must select appropriate peers and existing policies still apply.

Remote task creation, continuation and cancellation declare external-write
and network effects. Hosted operators must allow the exact tool name through
`AGENT_ALLOWED_WRITE_TOOLS`; local users use the existing scoped approval flow.
Only status `get` is a parallel read. Agent Card descriptions cannot downgrade
these effects. Endpoint, credential environment and actual credential identity bind the
descriptor version and durable task reference. Changing a credential in a running
process fails before network dispatch; rebuild the peer adapter to adopt it.
The rebuilt identity cannot inherit the old principal's task references or
pending approvals, even when the endpoint and environment-variable name match.
No credential is stored in task receipts; only an opaque binding digest is used.
Rotation does not prove that the replacement credential belongs to the same
remote account, so outstanding old tasks require explicit reconciliation.

## Lifecycle, recovery and evidence

`send` requests a nonblocking A2A response. An immediate agent message is a
report; a task response includes its task/context ID and state. Use `get` for
later progress and `cancel` to request termination. To continue an input-required
task, use `send` with its task ID and a new explicit assignment. There is no
background polling loop or hidden task scheduler.

Get/cancel/continuation require a reference already recorded by this configured
tool in the canonical transcript of the same session and actor. Local CLI/app-server sessions may have an empty end-user ID: they retain the
exact actor recorded by the owning session instead of inventing a hosted user.
The reference survives process restart; a caller-supplied foreign task ID is not authorization.
Returned task/context IDs must retain their binding. Reports and file references
are untrusted remote evidence; files are not downloaded automatically and remote
claims are never represented as locally verified child transcript events.

Messages use a stable ID derived from local session/turn/call identity. This
supports peer deduplication **only when the peer implements it**. The adapter
never automatically repeats an uncertain send. If the response is lost before
its reference commits, the remote outcome can be unknown; operator reconciliation
may be necessary. Local cancellation bounds the HTTP request but cannot prove
that an already accepted remote task stopped. Remote usage is explicitly unknown
and is not fabricated or charged as known local model usage. Configure budgets
at the peer as well as in the local runtime.

The HTTP timeout defaults to 60 seconds and response budget to 1 MiB per request.
An exceeded budget is an error, not a truncated successful report. Authentication
and transport errors expose a content-free diagnostic rather than peer bodies.

## Inbound composition

[Executor](../packages/a2a/executor.go) implements the official SDK executor
interface using `delegation.Runner.RunTask` (or another explicit task runner).
It accepts bounded text objectives, resolves local scope through an authenticated
composition callback, and returns only final text artifacts and task state.
SDK task cancellation cancels execution through the inherited context. It does
not install a second model loop, listener, default principal, or production store.

A service mounting this adapter must supply HTTP authentication **and authorize
all task-store reads/cancels**, not merely the execute callback. It must choose
an appropriately isolated/durable SDK task store and publish its own Agent Card.
The SDK's memory store is useful for tests, not restart-safe production hosting.
No inbound endpoint is enabled in the existing deployment by this change.

## Protocol and verification

The adapter pins official `a2a-go` v0.3.15 and its A2A 0.3 JSON-RPC binding. It
supports send/get/cancel and explicit task continuation. It does not claim A2A
1.0 conformance, Agent Card discovery, push notifications, cross-peer credential
federation, remote budget settlement, or durable distributed child scheduling.
These require separate operational contracts and interoperability tests.

Tests exercise the actual SDK HTTP server/client, trusted local scope, task
states/artifacts, same-session ownership, credentials, redirect confinement and
response budgets. They do not establish interoperability with an untested peer
or real-model task quality. No peer is contacted until explicitly configured.

References: [official Go SDK](https://github.com/a2aproject/a2a-go),
[A2A specification](https://a2a-protocol.org/latest/specification/),
[Claude Code subagents](https://code.claude.com/docs/en/sub-agents),
[Codex agent control](https://github.com/openai/codex/blob/main/codex-rs/core/src/agent/control.rs).
