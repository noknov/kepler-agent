# slack-copilot-agent

A Slack-native AI assistant written in Go. It runs inside Slack threads, keeps
durable conversation state in PostgreSQL, calls tools through structured
function-calling, and can help with research, writing, code reading, GitHub
Actions, Kubernetes/GCP diagnostics, runbooks, reminders, and browser tasks.

## Highlights

- **Slack-first workflow:** handles mentions, DMs, thread replies, files, App
  Home, and reaction feedback.
- **Structured tool use:** LLMs call typed tools instead of relying on prompt
  parsing.
- **Durable execution:** Slack events, sessions, runs, and reminders are backed
  by PostgreSQL.
- **Safe code access:** workspace allowlists, read-only command policy, secret
  redaction, immutable git snapshots, and per-tool action boundaries.
- **Production lifecycle:** `/livez`, `/readyz`, local-only `/drain`, inbox
  leases, graceful shutdown, Docker, and starter Kubernetes manifests.
- **Configurable models:** OpenAI-compatible and Anthropic-compatible providers,
  optional secondary model, multimodal routing, and context compaction.

## Quick Start

```bash
cp cmd/slack-copilot-agent/.env.example cmd/slack-copilot-agent/.env
# Fill Slack, LLM, PostgreSQL, Redis, and ALLOWED_SLACK_USERS values.
go run ./cmd/slack-copilot-agent
```

For split local services, use service-specific env files:

```bash
cp gateway/.env.example gateway/.env
cp worker/.env.example worker/.env
cp observability/.env.example observability/.env
```

The legacy `slack-copilot-agent` binary still runs all responsibilities in one
process. New service entrypoints live at the repository root:

```bash
go run ./gateway/cmd/gateway   # Slack Events / Interactions HTTP ingress
go run ./worker/cmd/worker     # Durable inbox consumer and agent runner
go run ./observability/cmd/observability
go run ./cli/cmd/slack-copilot tools list
```

The packaged `slack-copilot` CLI is local-first; its built-in read-only
commands do not require Redis, PostgreSQL, Slack, or LLM service env files.

The compatibility all-in-one process is kept for local development and rollback,
but split deployment should run `gateway`, `worker`, and `observability` as
separate services.

Expose the server to Slack with a public HTTPS URL such as ngrok:

```bash
ngrok http 8080
```

Use the forwarding URL as the Slack Events API request URL:

```text
https://<your-ngrok-domain>/slack/events
```

## Required Configuration

At minimum, configure:

```bash
SLACK_BOT_TOKEN=xoxb-...
SLACK_SIGNING_SECRET=...
ALLOWED_SLACK_USERS=U11111111,U22222222
POSTGRES_DSN=postgres://user:pass@localhost:5432/slack_copilot?sslmode=disable
REDIS_URL=redis://localhost:6379/0

LLM_PROVIDER=longcat
LONGCAT_API_KEY=Bearer lc-...
LONGCAT_BASE_URL=https://api.longcat.chat/anthropic
LONGCAT_MODEL=LongCat-2.0
LONGCAT_PROTOCOL=anthropic
```

For the full provider matrix, streaming knobs, multimodal routing, and storage
settings, see [Configuration](docs/configuration.md).

## Slack App Setup

Required OAuth scopes:

- `app_mentions:read`
- `channels:history`, `groups:history`, `im:history`
- `chat:write`
- `files:read`
- `chat:delete` if you want the bot to remove temporary thinking indicators

Event subscriptions:

```text
app_mention
message.channels
message.groups
message.im
app_home_opened
file_shared
reaction_added
```

Set `ALLOWED_SLACK_CHANNELS` to restrict channel use. `ALLOWED_SLACK_USERS`
controls who can use the bot in channels and DMs.

## Project Layout

```text
gateway/                   Independent Slack HTTP ingress service
worker/                    Durable Slack event worker service
observability/             Independent metrics, runs, and tool health service
cli/                       Local CLI shell for future packaged agent workflows
cmd/slack-copilot-agent/   Compatibility all-in-one process entrypoint
packages/                  Shared libraries: agent, conversation, runtime, tools, storage
packages/prompts/defaults/ Committed generic prompt defaults
deploy/shared/k8s/         Shared starter Kubernetes infrastructure
gateway/deploy/k8s/        Gateway Kubernetes manifests
worker/deploy/k8s/         Worker Kubernetes manifests
observability/deploy/k8s/  Observability Kubernetes manifests
```

## Operations

Health endpoints:

| Endpoint | Purpose |
|---|---|
| `GET /livez` | Liveness probe |
| `GET /readyz` | Readiness probe; fails while draining |
| `POST /drain` | Local-only drain switch for Kubernetes `preStop` |
| `GET /metrics` | Durable run and cost metrics from observability |
| `GET /runs?limit=20` | Recent run list |
| `GET /health/dashboard` | Browser health dashboard |

Slack events are written to a durable PostgreSQL inbox before processing.
Workers claim events with a time-bounded lease, which makes retries safe during
crashes and rolling updates.

See [Operations](docs/operations.md) for Docker, Kubernetes, graceful shutdown,
observability, ngrok, and browser automation details.

## Documentation

- [Configuration](docs/configuration.md): model providers, env vars, streaming,
  multimodal routing, repository freshness.
- [Prompts](docs/prompts.md): committed prompt catalog and private overlays.
- [Tools](docs/tools.md): code, GitHub, Kubernetes/GCP, search, Slack, browser,
  reminders, and agent-control tools.
- [Operations](docs/operations.md): deployment, health, shutdown, costs, and
  Playwright MCP.

## Development

```bash
go test ./...
go build ./cmd/slack-copilot-agent
go build ./gateway/cmd/gateway ./worker/cmd/worker ./observability/cmd/observability ./cli/cmd/slack-copilot
```

Some tests use `httptest.NewServer` and need permission to bind a local loopback
port in restricted environments.
