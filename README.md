# Kepler Agent

Shared agent harness for two products:

| Product | Where it runs | What it is for |
|---|---|---|
| **Hosted Agent** | Server workspace, with Slack as ingress and presentation | Team diagnosis and operational work |
| **Local CLI** | User machine, inside a selected workspace | Interactive or headless coding-agent work |
| **Local Desktop** | User machine, inside a selected workspace | Native visual coding-agent work with approval cards |

The project is intentionally not a single remote agent exposed through two UIs.
Both products share one provider-neutral execution loop and transcript contract;
their policy, storage, tools, and presentation stay product-specific.

> **Version note:** `main` is the active v2 architecture. The final v1 source
> is frozen at [`v1-final`](https://github.com/noknov/kepler-agent/tree/v1-final)
> (`49380a51`) and receives no fixes or releases. Do not combine v1 runtime,
> schema, or deployment configuration with v2.

## Architecture at a glance

```text
Slack ── gateway / worker ── hosted profile ─┐
                                               │
Local CLI / Desktop / app-server ── local profile ──┼── shared harness
                                               │   model loop · context · tools
Providers · skills · MCP ─────────────────────┘   canonical transcript · events
```

- **One loop:** context projection, model calls, tool execution, compaction,
  steering, and termination live in `packages/agent`.
- **One event model:** hosted sessions persist canonical events in PostgreSQL;
  local sessions persist the same model as JSONL.
- **Two safety models:** local execution uses a workspace sandbox and scoped
  approvals. Hosted policy is authoritative and non-interactive; mutations need
  an exact operator allowlist entry.
- **Durable hosted work:** PostgreSQL owns transcripts, session inputs, inboxes,
  run projections, and user connections. Redis provides wakeups and
  coordination, not a durable queue.

Read the [bilingual architecture guide](https://noknov.github.io/kepler-agent/)
for a detailed v1/v2 comparison and code-reading paths.

## What is in this repository

| Area | Included behavior |
|---|---|
| Agent runtime | Provider-neutral messages and tools, bounded context projection, compaction, termination, canonical transcripts, and typed streaming events |
| Model integration | OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages adapters; hosted primary/fallback resilience and circuit breaking |
| Hosted Slack | Verified durable ingress, leased workers, native answer streaming, thread status, runs/costs, and health endpoints |
| Local product | TTY/headless CLI and a native Tauri desktop app, JSONL resume, steering/queue input routing, workspace tools, OS sandbox, approvals, skills, and configured MCP |
| Integrations | Per-user OAuth connections for configured Slack, GitHub, ClickStack, Google Cloud, and Notion integrations; connection-required turns can resume after OAuth |
| Delegation | `agent-explore`: bounded, read-only child turns with separate transcripts and parent audit links |
| Evaluation | Independent subprocess-based harnesses for this agent and other supported candidates through one model gateway |

This is not a deployment distribution. PostgreSQL, Redis, secrets, worker
images, and orchestration are operator-owned.

## Start with the local CLI

Requirements: Go 1.25 and credentials for one supported provider.

```bash
go build -o bin/kepler-agent ./cli/cmd/kepler-agent
bin/kepler-agent config init
export OPENAI_API_KEY=...
bin/kepler-agent --cwd /path/to/project
```

The same binary supports non-interactive work:

```bash
bin/kepler-agent --cwd . "diagnose the failing tests"
printf "review this repository\n" | bin/kepler-agent --cwd . --output jsonl
bin/kepler-agent --resume
```

`max_steps = 0` is the local default: turns run until completion, cancellation,
or a model/context limit. Public benchmarks set an explicit budget separately.

The CLI's provider configuration and credentials are local; they are not shared
with a hosted deployment. Use `--profile <name>` to select a named model
profile. See [local CLI usage and security](docs/local-cli.md) and
[the example configuration](cli/config.example.toml).

## Run the local desktop app

The desktop app is a native Tauri window, not a web UI. It starts the same
local app-server used by other local clients over stdio; no Slack, Redis,
PostgreSQL, ngrok, or hosted credentials are involved.

```bash
make desktop-dev
```

Choose a workspace in the left sidebar. Sessions and approval decisions remain
in the local Kepler state directory. `make desktop-build` packages the native
app and its app-server sidecar for distribution.

## Run the hosted Slack agent

Requirements: Go 1.25, PostgreSQL, Redis, a Slack app, and a supported model
provider.

```bash
cp gateway/.env.example gateway/.env
cp worker/.env.example worker/.env
cp observability/.env.example observability/.env
# Configure Slack, provider, PostgreSQL, Redis, and ALLOWED_SLACK_USERS.
psql "$POSTGRES_DSN" -f schema/postgres.sql

go run ./gateway/cmd/gateway
go run ./worker/cmd/worker
go run ./observability/cmd/observability
```

The gateway verifies and stores Slack events. Workers claim them with renewable
leases, execute the hosted profile, and deliver replies. Observability reads
run projections; it is not the conversation state store. Application code never
creates or alters database objects.

### Slack setup

Required scopes:

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

Use `agent_view` for Slack's Agent experience. Restrict access with
`ALLOWED_SLACK_USERS` and, when needed, `ALLOWED_SLACK_CHANNELS`. Provider,
storage, OAuth, streaming, and tool settings are in
[configuration](docs/configuration.md).

## Operations and safety

| Endpoint | Purpose |
|---|---|
| `GET /livez` | Process liveness |
| `GET /readyz` | Dependency readiness; fails while draining |
| `POST /drain` | Loopback-only graceful drain |
| `GET /metrics` | Durable run and cost metrics |
| `GET /runs?limit=20` | Recent run history |
| `GET /health/dashboard` | Tool and service health |

Hosted processing is at least once, not distributed exactly once: inbox leases,
owner checks, turn replay, and deterministic Slack message IDs reduce duplicate
delivery but do not make Slack and PostgreSQL one transaction. See
[operations](docs/operations.md) for deployment and recovery details.

For local work, commands use argv rather than shell construction, workspace
paths are constrained, and macOS Seatbelt or Linux bubblewrap is required
unless an explicit development escape hatch is enabled. For hosted work, no
end-user action grants access to the server host.

## Evaluate agent harnesses

`evals/` deliberately does not import runtime packages. It copies each fixture
into an isolated workspace and HOME, invokes candidates as subprocesses, runs
the task grader, and retains logs, exit state, duration, workspace diffs, and a
`run.json` manifest.

```bash
make build
python3 evals/run.py \
  --suite evals/suites/smoke.json \
  --candidates evals/candidates.example.json \
  --model controlled-model \
  --output /tmp/kepler-agent-eval
```

The checked-in smoke task verifies evaluator wiring; it is not a quality claim.
Pin candidate versions and use one controlled model gateway before comparing
results. See the [evaluation protocol](evals/README.md).

## Repository map

```text
packages/agent/          Shared model, prompt, tool, transcript, and runtime contracts
packages/profiles/       Hosted and local composition roots
packages/surfaces/       Ingress and presentation adapters, including Slack
packages/tools/          Capability-oriented tool implementations
packages/providers/      Provider wire-format adapters
packages/connections/    Per-user OAuth connection lifecycle
packages/appserver/      Local profile JSON-RPC server over stdio
cmd/kepler/              Local CLI executable entrypoint
cli/                     Local CLI implementation and configuration example
apps/desktop/            Native Tauri desktop surface and its app-server bridge
evals/                   Black-box evaluation harness
gateway/ worker/         Hosted Slack ingress and durable worker commands
observability/           Runs, costs, metrics, and tool health command
schema/postgres.sql      Current PostgreSQL contract for fresh installs
architecture-site/       Bilingual v1/v2 architecture guide
```

## Documentation

- [Architecture guide](https://noknov.github.io/kepler-agent/)
- [v2 overview](docs/v2/README.md) and [v1 archive](docs/v1/README.md)
- [Shared runtime](docs/runtime.md)
- [Local CLI](docs/local-cli.md)
- [Configuration](docs/configuration.md)
- [Tools](docs/tools.md)
- [Prompts and private overlays](docs/prompts.md)
- [Operations](docs/operations.md)

## Development

```bash
make check
```

This runs formatting and boundary checks, vet, tests, builds, and the evaluation
dry-run. Some HTTP tests bind loopback ports and need that permission in
restricted environments.
