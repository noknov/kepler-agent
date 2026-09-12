# Safety and limitations

This page separates intended boundaries from guarantees that require testing
across all tools and failure paths. Report vulnerabilities through
[SECURITY.md](../SECURITY.md).

## Hosted execution

The operator controls workspace roots, credentials, network placement, and the
exact write-tool allowlist. End-user approval cannot grant access to the host.
Surface visibility does not authorize a mutation. Tool effects must accurately
describe the operation, including dynamically discovered MCP tools; a remote
tool's label alone is not proof that it is read-only.

Integration identity also matters: per-user OAuth and shared operator
credentials have different resource scopes. An application allowlist is not a
substitute for checking the upstream resource permissions intended for users.

## Local execution

The local profile combines workspace path checks, sensitive-path restrictions,
scoped approvals, and macOS Seatbelt or Linux bubblewrap. Commands use argv.
Pattern matching supplies risk signals; it is not a shell parser or isolation
boundary. Disabling the sandbox is an explicit development escape hatch and
changes the execution boundary.

Validate reads, search, Git revision access, symlinks, command descendants,
and large output together. Passing a read-file test alone does not establish
that another tool cannot access the same path.

## Persistence and external effects

Canonical transcripts and owner-checked durable work improve replay, but do
not make an external API call and a database append atomic. A crash after a
side effect and before its result is recorded can leave an uncertain outcome.
Do not automatically repeat such actions without checking their result.

Likewise, browser acceptance, Slack inbox persistence, local app-server
admission, and UI streaming are distinct boundaries. Test each surface under
restart and disconnection; do not transfer one surface's recovery guarantee
to another merely because both use the shared runtime.

Session locks, database pools, parent/child concurrency, turn deadlines, and
shutdown deadlines must be sized together. Readiness reports dependency state;
it is not proof of an end-to-end successful turn or a verified backup.

## Validation limits

- Unit tests and evaluator dry-runs do not establish model quality.
- CLI coding scores do not establish hosted permission or delivery correctness.
- A completed model answer does not prove its citations or review findings.
- A successful bundle does not establish all frontend types or interactions.
- Public benchmark results require a fixed, attributable execution environment.

Use [development checks](development.md) and the [evaluation guide](../evals/README.md)
to select evidence for the behavior being changed. Keep dated audits and
incident reports separate from this current-behavior documentation; revalidate
findings against the relevant commit before treating them as current status.
