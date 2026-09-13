# Kepler Agent

Kepler is an agent harness shared by hosted team assistants and a local coding
CLI. Tools run where the selected product runs; the clients do not all execute
on the same remote workspace.

| Product | Tools and workspace | Entry point |
| --- | --- | --- |
| Hosted Slack | Operator-managed server workspace | Slack mentions and conversations |
| Hosted Web | Operator-managed server workspace | Browser, authenticated through Slack OIDC |
| Local CLI | User-selected local workspace | Interactive terminal or headless command |

Profiles compose a shared model/tool loop with product-specific policy,
storage, credentials, and presentation. Hosted transcripts use PostgreSQL;
local transcripts use JSONL. The local app-server exposes the local profile
over stdio JSON-RPC for clients.

## Start here

- **Connect Slack:** follow the [Slack integration guide](docs/slack.md).
- **Understand or change the code:** start with [architecture](docs/architecture.md)
  and [development](docs/development.md).
- **Evaluate behavior:** read the [evaluation guide](evals/README.md).
- **Browse all documentation:** use the [documentation index](docs/README.md).

## Develop

Use the Go version declared in [go.mod](go.mod). The CLI frontend uses the
Node/pnpm versions in [CI](.github/workflows/check.yml).

```sh
make check
```

This checks formatting, package boundaries, generated protocol artifacts,
Go vet/tests/builds, and evaluator wiring. It does not run a real model
benchmark or the full frontend checks. See [verification scope](docs/development.md#verification)
for frontend, race, and integration checks.

## Repository boundaries

| Path | Owns |
| --- | --- |
| `packages/agent/` | Model/tool contracts, transcript, context, runtime, delegation |
| `packages/profiles/` | Hosted and local composition and policy |
| `packages/surfaces/` | Slack/Web ingress and presentation |
| `packages/providers/`, `packages/tools/` | Provider adapters and tool implementations |
| `packages/connections/` | User integration credentials and OAuth lifecycle |
| `packages/appserver/`, `appserver/` | Local JSON-RPC protocol and executable |
| `cli/`, `apps/cli/` | Go launcher and Ink terminal frontend |
| `gateway/`, `worker/`, `observability/` | Hosted service entry points |
| `schema/`, `evals/`, `architecture-site/` | Schema contract, evaluation, Chinese architecture guide |

Hosted write authorization belongs to the operator; user approval cannot grant
access to the host. Local execution uses workspace policy, scoped approvals,
and an OS sandbox. These are design boundaries, not a claim of complete
isolation or exactly-once external effects. See [safety and limitations](docs/safety.md)
and [private vulnerability reporting](SECURITY.md).
