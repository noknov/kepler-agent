# Configuration

Choose the configuration owner before editing a value. A variable's existence
in source does not mean every surface reads it.

| Owner | Where to edit | Used by |
| --- | --- | --- |
| Hosted deployment | Deploy repo `k8s/configmaps/<service>.yaml` and SOPS secrets | Docker gateway, worker, observability |
| Source debugging | Service `.env` or `KEPLER_AGENT_ENV_FILE` | Directly launched Go service |
| Local CLI | TOML created by `kepler-agent config init` | Local limits, routing, sandbox, MCP, prompts |
| Provider credentials/model selection | Hosted deploy configuration | Worker and local-client bootstrap |
| Private prompt context | `PROMPT_DIR` overlay | Profile-specific prompt composition |

The local Docker scripts read the YAML files directly; the `k8s/` directory
name does not mean a Kubernetes deployment is required. See the deploy repo's
`docs/configuration-workflow.md` for applying changes.

## Service requirements

| Service | Main configuration |
| --- | --- |
| Gateway | Slack signing secret, PostgreSQL, Redis; OAuth/public origin/allowlist and worker upstream for CLI/Web |
| Worker | Slack bot/signing credentials, allowlist, PostgreSQL, Redis, active provider credentials |
| Observability | PostgreSQL, Redis, admin token for protected HTTP access |
| Local CLI | Gateway URL and Slack login; optional local TOML |

The source loader is [packages/config/config.go](../packages/config/config.go).
It validates requirements by service. For direct source debugging, its default
files are `gateway/.env`, `worker/.env`, and `observability/.env`. Use
`KEPLER_AGENT_ENV_FILE=/path/to/file` to select another file. Do not copy an
`.env.example` unless that file exists in your checkout; deployed configuration
is maintained in the deploy repository.

## Related references

- [Models](models.md): provider namespaces, protocols, primary/secondary settings.
- [Observability](observability.md): OTLP, Langfuse, cost rates.
- [Web](web.md): browser origin, cookies, OIDC, and static assets.
- [Tools](tools.md): integration credentials and search providers.
- [Prompts](prompts.md): public defaults and private overlays.
- [Local CLI](local-cli.md): local TOML and supported flags.

`SLACK_DEFAULT_LOCALE` selects deterministic status/attachment localization:
`zh` and `zh-*` use Chinese; other values use English. It does not infer locale
from message characters.

## Workspace snapshots

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

Delegated agents use separate orchestration and execution budgets:

```bash
AGENT_EXPLORE_MAX_STEPS=64
AGENT_EXPLORE_MAX_WORKERS=5
AGENT_EXPLORE_WORKER_TIMEOUT=
AGENT_EXPLORE_BATCH_TIMEOUT=
```

By default, delegated work inherits the parent turn deadline instead of
assuming a short review duration. Deployments may set tighter optional
deadlines: the batch timeout bounds one complete `agent-explore` call, while a
worker's timeout starts only after it acquires a concurrency slot. If both are
set, the batch timeout cannot be shorter than the worker timeout. The step
limit is a final liveness guard for the complete runtime loop.

Services verify the required tables at startup but never execute DDL. The source
`schema/postgres.sql` is the fresh-install contract; apply deploy migrations
through the [operations workflow](operations.md#schema-and-release-coordination).
The runtime database role only needs data access.

Bound connection pools, but size them against session locks, active turns,
child workers, and control-plane queries. This is an example, not a safe
concurrency recommendation for every deployment:

```bash
POSTGRES_MAX_CONNS=4
```

## Agent Runtime Policy

Hosted and local products execute the same harness. The hosted profile applies
authoritative server policy; the local profile applies its sandbox and scoped
approval policy.

Write and external-write tools are authorized entirely by server policy; users
are never asked to approve access to the host running the agent. The default
allowlist contains reminder operations, Slack Canvas creation, Slack
user-attributed message posting, TTS, and Luckin order creation/canceling.
Operators can replace it with an exact, comma-separated allowlist:

```bash
AGENT_ALLOWED_WRITE_TOOLS=luckin-cancel_order,luckin-create_order,reminder-create,reminder-cancel,slack-create_canvas,slack-user_post_message,tts-speak
```

A tool's surface annotation limits where it may run; it never grants write
permission by itself. Repository edits, local commands, workflow dispatch, and
third-party MCP mutations must be accurately classified and authorized before
execution. Do not infer safety from a dynamically discovered tool name or
surface annotation; see [safety](safety.md).
