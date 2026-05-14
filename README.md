# oncall-agent

Go-based Slack on-call debugging agent powered by configurable OpenAI-compatible LLM APIs, without Cursor CLI.

## Why this design

- Slack receives `app_mention`, verifies Slack signatures, checks allowlists, then starts an agent run in the thread.
- The model is called through an OpenAI-compatible HTTP API with native `tool_calls`, so tool execution is structured instead of parsed from free-form JSON text.
- Runtime safety is code-enforced: Slack user/channel authorization, prompt guardrails, post-response redaction, path allowlists, command deny rules, and read-only tool boundaries.
- Context is managed explicitly. Slack thread context, session messages, and tool observations are truncated before they reach the model.
- Sessions are persisted by Slack `channel:thread_ts`, so follow-up mentions and pending clarification replies reuse context.
- Delegation, rules, and skills are modeled as focused agent profiles, not as one-off prompt folders. RAG and caching should be added under `internal/memory` or infrastructure once there is a concrete retrieval source.

## Project layout

```text
cmd/oncall-agent/          Minimal process entrypoint
internal/app/              Dependency wiring and HTTP server
internal/slack/            Slack signature verification, Events API types, Web API client
internal/conversation/     Slack thread lifecycle, per-session locks, idempotency, pending replies
internal/agent/            Provider-agnostic tool-calls runner
internal/memory/           Conversation turns, context packing, tool-result truncation
internal/session/          File-backed Slack thread sessions
internal/safety/           Access policy, prompt policy, redaction, workspace and command policy
internal/toolkit/tools/    Tool modules: code, git, gcp, notion, youtrack, slack
internal/delegation/       Focused delegate agent profiles plus rules/skills loading
internal/observability/    In-memory metrics and reaction feedback
config/rules/              Runtime rules injected into prompts
config/skills/             Skill docs injected into prompts and delegate profiles
```

## Run locally

```bash
cd /Users/shelton/Documents/wati/oncall-agent
cp .env.example .env
go run ./cmd/oncall-agent
```

`oncall-agent` loads local `.env` automatically. The simplest setup uses DeepSeek-compatible variables:

- `OPENAI_API_KEY`
- `OPENAI_BASE_URL=https://api.deepseek.com`
- `OPENAI_MODEL=deepseek-chat`

You can still point the service at other OpenAI-compatible providers with `KIMI_*`, `MOONSHOT_*`, or `ANTHROPIC_*` compatibility variables, but startup now fails if your shell and `.env` disagree on provider settings unless you explicitly set `ALLOW_ENV_MIXING=true`.

If you point the bot at `https://api.kimi.com/coding/`, startup now fails by default. That endpoint must be explicitly opted into with `ALLOW_EXPERIMENTAL_CODING_ENDPOINT=true`.

Expose `POST /slack/events` to Slack. The app also exposes:

- `GET /healthz`
- `GET /metrics`

## ngrok

For local development:

```bash
ngrok http 8080
```

Use the HTTPS forwarding URL as the Slack Request URL:

```text
https://<your-ngrok-domain>/slack/events
```

Set `HTTP_ADDR=:8080`, fill `SLACK_SIGNING_SECRET` from Slack App > Basic Information, and keep Socket Mode disabled. If you use a free ngrok URL, update Slack's Request URL whenever ngrok gives you a new domain. A reserved ngrok domain avoids that churn.

## Slack app setup

Use Slack Events API with:

- `app_mentions:read`
- `channels:history`, `groups:history`, `im:history` as needed for thread context
- `chat:write`
- `chat:delete` if you want the temporary thinking message removed
- Event subscriptions: `app_mention`, `message.channels`, `message.groups`, `app_home_opened`, `reaction_added`

Only users in `ALLOWED_SLACK_USERS` are sent to the LLM.

The old App Home / modal flow for per-user agent tokens is not needed. If you reuse an existing Slack app, remove or disable App Home and Interactivity callbacks that only served token registration. The App Home page now shows the actual provider, base URL host, and model the service is configured to use.

## Tools

Current tool modules:

- `code.search`, `code.read_file`
- `git.status`, `git.log`, `git.show`
- `gcp.logs`
- `notion.search`, `notion.create_page`
- `youtrack.get_issue`, `youtrack.search`
- `slack.ask_user`
- `delegate.run`

Sensitive actions are deliberately absent. The command guard blocks destructive command patterns, and the shipped tools use argumentized `exec.CommandContext` instead of shell strings.

GCP values in `.env` are defaults and hints, not fixed environments. `gcp.logs` accepts `project`, `namespace`, `service`, and raw `filter` per call. If `namespace` is omitted, the tool no longer silently injects `GCP_NAMESPACE`; environment mappings can be added later in `config/rules`.

## Notes

The implementation keeps provider choice configurable through env vars. `OPENAI_*` reflects the original DeepSeek setup, while `KIMI_*`, `MOONSHOT_*`, and `ANTHROPIC_*` remain available for other OpenAI-compatible backends.
