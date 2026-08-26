# Configuration

Each service loads its service-specific env file automatically at startup:

| Entrypoint | Env file |
|---|---|
| `./gateway/cmd/gateway` | `gateway/.env` |
| `./worker/cmd/worker` | `worker/.env` |
| `./observability/cmd/observability` | `observability/.env` |

Set `KEPLER_AGENT_ENV_FILE=/path/to/file` only for one-off local debugging.
Keep secrets out of git; the `*.example` files are templates only.

The packaged `kepler-agent` CLI is local-first and does not require Redis,
PostgreSQL, or Slack. It still requires the configured model credential; local
filesystem and argv tools are governed by the workspace sandbox and approval
policy.

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
| Local CLI / benchmark | provider configuration and API key |

Worker example:

```bash
SLACK_BOT_TOKEN=xoxb-...
SLACK_SIGNING_SECRET=...
SLACK_DEFAULT_LOCALE=en-US
ALLOWED_SLACK_USERS=U11111111,U22222222
POSTGRES_DSN=postgres://user:pass@localhost:5432/kepler_agent?sslmode=disable
```

`SLACK_DEFAULT_LOCALE` controls deterministic Slack status and attachment-note
localization. `zh` and `zh-*` select Chinese; other values use English. The
service does not guess locale from message characters.

`LLM_PROVIDER` selects the active model provider. Each provider has its own env
namespace so credentials do not accidentally leak between providers.

Optional output limit:

```bash
LLM_MAX_OUTPUT_TOKENS=8192
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

### Kimi / Moonshot

Both names use the Moonshot OpenAI-compatible endpoint; choose the namespace
that matches the credential you operate.

```bash
LLM_PROVIDER=kimi
KIMI_API_KEY=...
KIMI_BASE_URL=https://api.moonshot.ai/v1
KIMI_MODEL=kimi-k2.6
```

```bash
LLM_PROVIDER=moonshot
MOONSHOT_API_KEY=...
MOONSHOT_BASE_URL=https://api.moonshot.ai/v1
MOONSHOT_MODEL=kimi-k2.6
```

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
OPENCODE_GO_PROTOCOL=responses
```

Use `responses` for OpenCode Go when Slack image attachments or other
vision-capable models such as `gpt-5.6-luna` are in use. The `openai`
chat/completions path returns HTTP 400 for those multimodal requests.
Leave `OPENCODE_GO_TEMPERATURE` unset unless you explicitly need sampling
control; unset values are not sent to the provider. Reasoning models such as
`gpt-5.6-luna` reject `temperature` even when set to `0`.

## Secondary Model

The optional secondary model is used for compact summaries and
presentation-only Slack progress wording. Progress wording is generated from
the redacted user request and confirmed tool names, and never enters the
transcript or execution context. Slack keeps the native assistant status on the
localized thinking state; the secondary model only replaces the loading message
with a specific action-and-target label. If the secondary model is not
configured or cannot produce a valid structured label, the generic localized
thinking loading message remains unchanged.

```bash
SECONDARY_PROVIDER=opencode-zen
OPENCODE_ZEN_API_KEY=...
SECONDARY_MODEL=mimo-v2.5-free
```

When `SESSION_COMPACT_MODEL` is unset, compact summaries use `SECONDARY_MODEL`
when configured, otherwise the primary model.

## OpenTelemetry

The gateway, worker, observability service, and local CLI support standard
OTLP/HTTP trace export through the OpenTelemetry environment contract:

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318
OTEL_SERVICE_NAME=kepler-agent-worker
```

The shared runtime emits nested `agent.turn`, `model.generate`, and
`tool.execute` spans. Session/turn IDs and tool/model names are attributes;
prompt text, tool arguments, model output, credentials, and Slack message text
are never attached. With no OTLP endpoint configured, tracing is a no-op.

## Repository Freshness

Code-reading tools use immutable snapshot semantics. Each git-backed call
refreshes `origin` once per turn for each repository, then reads with `git show`
or `git grep` without touching the working tree. If `source` is omitted,
`code-search` and `code-read_file` use the repository's checked-out branch
upstream, normally `origin/<branch>`, after the refresh. A refresh failure is
returned to the caller; tools do not use stale refs. Within a turn, repeated
reads of the same repository reuse the refreshed refs to avoid redundant network
fetches. `source=working_tree` is an explicit checkout-view escape hatch, not
the default code investigation path. Deployment-specific default refs belong in
the private prompt overlay.

```bash
WORKSPACE_ROOTS=/path/to/repos
WORKSPACE_AUTO_FETCH=false
```

Set `WORKSPACE_AUTO_FETCH=true` only when background refreshes are acceptable.

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
Provider temperature env vars are optional: when unset, the runtime omits
`temperature` from provider requests instead of defaulting it to zero.

## Storage and Concurrency

All session, session-input, run, reminder, user preference, tool spill, and
event inbox states use PostgreSQL. The services do not contain a filesystem
persistence fallback:

```bash
POSTGRES_DSN=postgres://user:pass@localhost:5432/kepler_agent?sslmode=disable
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
that arrive while a session is active are written to
`agent_session_inputs` in PostgreSQL and use owner-checked claim/ack leases.
Redis stores the short-lived active-worker hint and publishes wakeups only; a
periodic PostgreSQL scan recovers missed wakeups and promotes abandoned
steering input to queued turns. There is no Redis or process-memory queue
fallback.

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
allowlist contains only reminder, Slack Canvas, and TTS operations.
Operators can replace it with an exact, comma-separated allowlist:

```bash
AGENT_ALLOWED_WRITE_TOOLS=reminder-create,reminder-cancel,slack-create_canvas,tts-speak
```

A tool's surface annotation limits where it may run; it never grants write
permission by itself. Repository edits, local commands, workflow dispatch, and
third-party MCP mutations therefore remain disabled unless explicitly enabled.
