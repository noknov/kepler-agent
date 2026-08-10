# slack-copilot-agent

An open-source agent platform for code-assisted diagnosis and operational work.
It currently ships as a hosted Slack agent and is evolving toward a shared
harness used by both the hosted product and a full local coding-agent CLI.

> **Status:** v1 remains the stable production path. v2 is implemented as a
> parallel, testable vertical slice and is ready for sustained local trials;
> it does not yet serve production Slack traffic.

## Products

| Product | Runtime | Workspace | Status |
|---|---|---|---|
| Hosted Agent | Server-side; Slack is the current ingress | Server-owned, read-only by default | v1 stable; v2 adapter implemented |
| Local CLI | Runs the complete harness on the user machine | Local workspace-write sandbox | v2 available for trials |

Both v2 profiles share the agent loop, model and tool contracts, prompt
composition, context projection, transcript, retries, termination, and event
semantics. They intentionally differ in execution, storage, policy, and
presentation.

## What is included

- Slack mentions, DMs, threads, files, App Home, and reaction feedback.
- Durable event processing, sessions, run traces, reminders, and protocol
  replay backed by PostgreSQL; Redis provides coordination and wakeups.
- Structured tools for code, GitHub, Kubernetes/GCP, runbooks, web, browser,
  Slack, and operational diagnostics.
- OpenAI-compatible and Anthropic-compatible model protocols, streaming,
  multimodal input, retries, and context compaction.
- Server-side capability policy, workspace boundaries, credential redaction,
  event leases, dead letters, health checks, and graceful draining.
- v2 local JSONL sessions, TTY/headless operation, steering or queued input,
  OS sandboxing, scoped approvals, file skills, and MCP tools.
- Independent black-box evaluation across this agent, Codex, Claude Code, Pi,
  and OpenCode through one controlled model gateway.

## Architecture

```text
Slack / API / App Server ─┐
                          ├─ hosted profile ─┐
Local TTY / headless CLI ─┘                 │
                                            ├─ shared v2 harness
Model providers ────────────────────────────┤  loop · context · transcript
Built-ins / MCP / skills ───────────────────┘  tools · policy · events
```

Slack is an ingress and presentation surface, not a separate agent. The local
CLI executes locally; the hosted profile executes against server workspaces
under server-owned policy. See the bilingual
[architecture guide](https://noknov.github.io/slack-copilot-agent/) for the
current v1 topology and the v2 engineering plan.

## Try the v2 local CLI

```bash
go build -o bin/slack-copilot-v2 ./v2/cmd/slack-copilot
cp v2/config.example.toml ~/.config/slack-copilot-agent/config.toml
export OPENAI_API_KEY=...
bin/slack-copilot-v2 --cwd /path/to/project
```

The same binary adapts to automation when given a prompt or piped input:

```bash
bin/slack-copilot-v2 --cwd . "diagnose the failing tests"
printf "review this repository\n" | bin/slack-copilot-v2 --cwd . --output jsonl
bin/slack-copilot-v2 --resume
```

The default shell profile writes only inside the workspace and denies network
access. Network and external effects require approval; grants may apply once,
for the process session, or to the exact command and project. See
[v2 usage and security](v2/README.md).

## Run the hosted Slack agent

Prerequisites: Go 1.25, PostgreSQL, Redis, a Slack app, and a supported model
provider.

```bash
cp cmd/slack-copilot-agent/.env.example cmd/slack-copilot-agent/.env
# Configure Slack, model, PostgreSQL, Redis, and ALLOWED_SLACK_USERS.
psql "$POSTGRES_DSN" -f schema/postgres.sql
go run ./cmd/slack-copilot-agent
```

The all-in-one binary is convenient locally. Production responsibilities can
also be split without changing the storage contract:

```bash
go run ./gateway/cmd/gateway
go run ./worker/cmd/worker
go run ./observability/cmd/observability
go run ./appserver/cmd/app-server
```

The gateway verifies and persists Slack events, workers claim and execute
them, observability serves run and cost views, and app-server exposes the
transport-neutral JSON-RPC surface. Runtime code performs no database DDL.

### Slack app

Required OAuth scopes:

```text
app_mentions:read
channels:history  groups:history  im:history
chat:write        assistant:write
files:read
```

Subscribe to:

```text
app_mention  message.channels  message.groups  message.im
app_home_opened  app_context_changed  file_shared  reaction_added
```

Use `agent_view` for Slack’s Agent experience. Restrict access with
`ALLOWED_SLACK_USERS` and, when needed, `ALLOWED_SLACK_CHANNELS`. Full provider,
streaming, storage, and Slack options are documented in
[configuration](docs/configuration.md).

## Evaluate harnesses

`evals/` is deliberately independent from v1 and v2 runtime packages. It
copies each fixture into an isolated workspace and HOME, invokes every agent as
a subprocess, runs the task grader, and retains logs, exit states, duration,
and aggregate results.

```bash
make build
python3 evals/run.py \
  --suite evals/suites/smoke.json \
  --candidates evals/candidates.example.json \
  --model controlled-model \
  --output /tmp/slack-copilot-eval
```

The checked-in smoke task validates the evaluator; it is not a published
quality result. Pin tool versions and route every candidate through the same
model gateway before drawing comparisons. See [evaluation protocol](evals/README.md).

## Repository map

```text
packages/agentv2/          Shared v2 harness and local/hosted profiles
v2/                        Full local CLI, example config, and v2 guide
evals/                     Independent harness evaluation and gateway example
gateway/                   Slack HTTP ingress
worker/                    Durable inbox consumer and hosted execution
observability/             Runs, costs, metrics, and tool health
appserver/                 Transport-neutral JSON-RPC service
packages/                  v1 agent, Slack, storage, tools, and infrastructure
schema/postgres.sql        Authoritative database schema
architecture-site/         Bilingual architecture guide
```

Deployment topology is intentionally operator-owned. The binaries can run
directly or be packaged with containers, Kubernetes, systemd, or another
orchestrator.

## Operations

| Endpoint | Purpose |
|---|---|
| `GET /livez` | Process liveness |
| `GET /readyz` | Dependency readiness; fails while draining |
| `POST /drain` | Loopback-only graceful drain |
| `GET /metrics` | Durable run and cost metrics |
| `GET /runs?limit=20` | Recent run history |
| `GET /health/dashboard` | Tool and service health |

Slack events are persisted before processing. Workers renew ownership leases;
expired work is recoverable, and exhausted events move to a dead-letter state.
See [operations](docs/operations.md) for deployment and failure semantics.

## Documentation

- [v2 guide](v2/README.md)
- [architecture](https://noknov.github.io/slack-copilot-agent/)
- [configuration](docs/configuration.md)
- [tools](docs/tools.md)
- [prompts and private overlays](docs/prompts.md)
- [agent protocol](docs/agent-protocol.md)
- [operations](docs/operations.md)

## Development

```bash
make check
```

This runs formatting checks, vet, the complete v1/v2 test suite, all binary
builds, and an evaluation dry-run. Some existing HTTP tests bind a local
loopback port and therefore need that permission in restricted environments.
