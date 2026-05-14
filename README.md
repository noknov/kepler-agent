# oncall-agent

Go-based Slack on-call debugging agent powered directly by Kimi/Moonshot Chat Completions, without Cursor CLI.

## Why this design

- Slack receives `app_mention`, verifies Slack signatures, checks allowlists, then starts an agent run in the thread.
- Kimi is called through the OpenAI-compatible HTTP API with native `tool_calls`, so tool execution is structured instead of parsed from free-form JSON text.
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

`oncall-agent` loads local `.env` automatically. Kimi can be configured either with Moonshot/OpenAI-compatible variables:

- `MOONSHOT_API_KEY` or `KIMI_API_KEY`
- `KIMI_BASE_URL` or `MOONSHOT_BASE_URL`
- `KIMI_MODEL`

or with Claude Code-style variables:

- `ANTHROPIC_BASE_URL`
- `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN`
- `ANTHROPIC_MODEL`
- `CLAUDE_CODE_MAX_OUTPUT_TOKENS`
- `API_TIMEOUT_MS`

When `ANTHROPIC_BASE_URL=https://api.kimi.com/coding/`, the agent normalizes it to the OpenAI-compatible `https://api.kimi.com/coding/v1` endpoint and defaults the model to `kimi-for-coding`.

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
- Event subscriptions: `app_mention`, `message.channels`, `message.groups`, `reaction_added`

Only users in `ALLOWED_SLACK_USERS` are sent to the LLM.

The old App Home / modal flow for per-user agent tokens is not needed. If you reuse an existing Slack app, remove or disable App Home and Interactivity callbacks that only served token registration. This service does not read per-user LLM tokens; it uses the service-level Kimi credentials and `ALLOWED_SLACK_USERS` for access control.

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

Kimi currently documents an OpenAI-compatible base URL at `https://api.moonshot.ai/v1`, native `tool_calls`, and `kimi-k2.6` as the latest recommended agent/code model. The implementation keeps these values configurable through env vars.
