# Configuration

Each service loads its service-specific env file automatically at startup:

| Entrypoint | Env file |
|---|---|
| `./gateway/cmd/gateway` | `gateway/.env` |
| `./worker/cmd/worker` | `worker/.env` |
| `./observability/cmd/observability` | `observability/.env` |

Set `SLACK_COPILOT_ENV_FILE=/path/to/file` only for one-off local debugging.
Keep secrets out of git; the `*.example` files are templates only.

The packaged `slack-copilot` CLI is local-first and does not require Redis,
PostgreSQL, Slack, or LLM service configuration for its built-in read-only
commands.

For local split deployment:

```bash
cp gateway/.env.example gateway/.env
cp worker/.env.example worker/.env
cp observability/.env.example observability/.env
```

## Required Values

Required values now depend on the service:

| Service | Required values |
|---|---|
| Gateway | `SLACK_SIGNING_SECRET`, `POSTGRES_DSN`, `REDIS_URL` |
| Worker | `SLACK_BOT_TOKEN`, `SLACK_SIGNING_SECRET`, `ALLOWED_SLACK_USERS`, `POSTGRES_DSN`, `REDIS_URL`, provider API key |
| Observability | `POSTGRES_DSN`, `REDIS_URL`; `OBSERVABILITY_TOKEN` for non-local access |
| Local agent / benchmark | provider API key |
| CLI | none for built-in local read-only commands |
| All-in-one | Worker requirements plus HTTP settings |

All-in-one example:

```bash
SLACK_BOT_TOKEN=xoxb-...
SLACK_SIGNING_SECRET=...
ALLOWED_SLACK_USERS=U11111111,U22222222
POSTGRES_DSN=postgres://user:pass@localhost:5432/slack_copilot?sslmode=disable
```

`LLM_PROVIDER` selects the active model provider. Each provider has its own env
namespace so credentials do not accidentally leak between providers.

Optional output limit:

```bash
LLM_MAX_OUTPUT_TOKEN=8192
```

When set to a positive value, the agent sends that value as `max_tokens` /
`max_completion_tokens`. When unset or set to `0`, the field is omitted and
output length is left to the provider default.

## LLM Providers

### LongCat

```bash
LLM_PROVIDER=longcat
LONGCAT_API_KEY=Bearer lc-...
LONGCAT_BASE_URL=https://api.longcat.chat/anthropic
LONGCAT_MODEL=LongCat-2.0
LONGCAT_PROTOCOL=anthropic
```

When using the Anthropic-compatible LongCat endpoint, the API key must include
the `Bearer ` prefix. For the OpenAI-compatible endpoint, use
`LONGCAT_BASE_URL=https://api.longcat.chat/openai`,
`LONGCAT_PROTOCOL=openai`, and omit the prefix.

### DeepSeek

```bash
LLM_PROVIDER=deepseek
DEEPSEEK_PROTOCOL=openai
DEEPSEEK_API_KEY=sk-...
DEEPSEEK_BASE_URL=https://api.deepseek.com
DEEPSEEK_MODEL=deepseek-v4-flash
```

### MiMo

```bash
LLM_PROVIDER=mimo
MIMO_PROTOCOL=anthropic
MIMO_API_KEY=...
MIMO_BASE_URL=https://token-plan-cn.xiaomimimo.com/anthropic
MIMO_MODEL=mimo-v2.5
MIMO_THINKING=disabled
```

MiMo thinking is disabled by default because multi-turn tool calls must preserve
provider-specific reasoning fields across turns.

### CLIProxyAPI

```bash
LLM_PROVIDER=cliproxyapi
CLIPROXYAPI_BASE_URL=http://127.0.0.1:8317/v1
CLIPROXYAPI_API_KEY=your-local-gateway-key
CLIPROXYAPI_MODEL=kimi/kimi-k2.7-code
```

Run and authenticate CLIProxyAPI locally first. It exposes OpenAI-compatible
endpoints and owns provider authentication separately.

### Anthropic

```bash
LLM_PROVIDER=anthropic
LLM_PROTOCOL=anthropic
LLM_ANTHROPIC_FLAVOR=official
ANTHROPIC_BASE_URL=https://api.anthropic.com
ANTHROPIC_API_KEY=sk-ant-...
ANTHROPIC_MODEL=claude-sonnet-4-5-20250929
```

### OpenAI-Compatible

```bash
LLM_PROVIDER=openai
OPENAI_API_KEY=...
OPENAI_BASE_URL=https://api.openai.com/v1
OPENAI_MODEL=gpt-4o-mini
```

Set `LLM_PROTOCOL=responses` for an OpenAI Responses endpoint. `openai`,
`responses`, and `anthropic` all adapt into the same canonical model contract;
the local CLI exposes the same choice as `--protocol`.

### OpenCode

OpenCode Zen and OpenCode Go use separate namespaces so free and subscription
credentials do not collide.

```bash
LLM_PROVIDER=opencode-zen
OPENCODE_ZEN_API_KEY=...
OPENCODE_ZEN_BASE_URL=https://opencode.ai/zen/v1
OPENCODE_ZEN_MODEL=mimo-v2.5-free
OPENCODE_ZEN_PROTOCOL=openai
```

```bash
LLM_PROVIDER=opencode-go
OPENCODE_GO_API_KEY=...
OPENCODE_GO_BASE_URL=https://opencode.ai/zen/go/v1
OPENCODE_GO_MODEL=glm-5.2
OPENCODE_GO_PROTOCOL=openai
```

## Secondary Model

The optional secondary model is used for cheaper/faster background work such as
read-only code exploration, dynamic statuses, and compact summaries.

```bash
SECONDARY_PROVIDER=opencode-zen
OPENCODE_ZEN_API_KEY=...
SECONDARY_MODEL=mimo-v2.5-free
```

When `SESSION_COMPACT_MODEL` is unset, compact summaries use `SECONDARY_MODEL`
when configured, otherwise the primary model.

## Repository Freshness

Code-reading tools use immutable snapshot semantics. The first code/repo read
for a repository refreshes `origin` refs only when that repo has not been
fetched recently, then reads with `git show` or `git grep` without touching the
working tree.

```bash
WORKSPACE_ROOTS=/path/to/repos
WORKSPACE_AUTO_FETCH=false
```

Set `WORKSPACE_AUTO_FETCH=true` only when background refreshes are acceptable.

## Streaming

Final answers are flushed in small batches:

| Variable | Default | Purpose |
|---|---:|---|
| `WEB_STREAM_FLUSH_INTERVAL` | `16ms` | Max time to buffer web SSE chunks |
| `WEB_STREAM_FLUSH_CHARS` | `16` | Max buffered web characters |
| `SLACK_STREAM_FLUSH_INTERVAL` | `50ms` | Max time to buffer Slack stream chunks |
| `SLACK_STREAM_FLUSH_CHARS` | `48` | Max buffered Slack characters |
| `STREAM_FLUSH_INTERVAL` | `35ms` | Shared fallback interval |
| `STREAM_FLUSH_CHARS` | `32` | Shared fallback character threshold |

## Multimodal Routing

Slack App Home shows the configured primary plus Explorer/Summary models.
`MULTIMODAL_MODELS` declares which models can receive image parts.
`MODEL_ROUTING_MULTIMODAL_MODEL` is an optional fallback used only when an
image arrives and the primary model is not listed in `MULTIMODAL_MODELS`.

```bash
MODEL_ROUTING_MULTIMODAL_MODEL=
MULTIMODAL_MODELS=
```

If neither the selected model nor the fallback is listed as multimodal, the
image is stripped and replaced with a text note asking for a description.

## Storage and Concurrency

All session, run, reminder, user preference, tool spill, and event inbox state
uses PostgreSQL. The services do not contain a filesystem persistence fallback:

```bash
POSTGRES_DSN=postgres://user:pass@localhost:5432/slack_copilot?sslmode=disable
SLACK_EVENT_WORKERS=8
SLACK_EVENT_QUEUE_SIZE=512
SLACK_EVENT_ENQUEUE_TIMEOUT=2s
SLACK_EVENT_TIMEOUT=15m
SLACK_EVENT_INBOX_LEASE=16m
SLACK_EVENT_MAX_ATTEMPTS=5
SLACK_EVENT_RETRY_BASE=1s
SLACK_EVENT_RETRY_MAX=1m
```

Workers renew the inbox lease while an event is running. Failed events use
bounded exponential backoff and move to `dead_letter` after the configured
attempt limit; malformed payloads are dead-lettered immediately. The inbox
lease must be greater than the event timeout.

`SLACK_EVENT_WORKERS` is the worker-level execution concurrency limit. Inputs
that arrive while a session is active are durably queued in Redis and drained
by the session owner; the PostgreSQL inbox remains the source of truth for
Slack event delivery.

Services verify the required tables at startup but never execute DDL. Initialize
a new PostgreSQL database with `schema/postgres.sql` using the administration
workflow of your choice. The runtime database role only needs data access.

For multi-replica deployments, keep database connections bounded:

```bash
POSTGRES_MAX_CONNS=4
```

## Agent Runtime Policy

Hosted and local products execute the same harness. The hosted profile applies
authoritative server policy; the local profile applies its sandbox and scoped
approval policy.

Write and external-write tools are authorized entirely by server policy; users
are never asked to approve access to the host running the agent. The default
allowlist contains only reminder, Slack screenshot/Canvas, and TTS operations.
Operators can replace it with an exact, comma-separated allowlist:

```bash
AGENT_ALLOWED_WRITE_TOOLS=reminder-create,reminder-cancel,slack-create_canvas
```

A tool's surface annotation limits where it may run; it never grants write
permission by itself. Repository edits, local commands, workflow dispatch, and
third-party MCP mutations therefore remain disabled unless explicitly enabled.
