# oncall-agent

Go-based Slack on-call debugging agent powered by configurable OpenAI-compatible or Anthropic-compatible LLM APIs, without Cursor CLI.

## Why this design

- Slack receives `app_mention`, verifies Slack signatures, checks allowlists, then starts an agent run in the thread.
- The model is called through an OpenAI-compatible Chat Completions API or Anthropic-compatible Messages API with native tool calls, so tool execution is structured instead of parsed from free-form JSON text.
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
internal/toolkit/tools/    Tool modules: code, git, github, gcp, notion, youtrack, slack
internal/delegation/       Focused delegate agent profiles plus rules/skills loading
internal/observability/    In-memory metrics and reaction feedback
PROMPT_DIR/rules/          Local runtime rules injected into prompts, gitignored by default
PROMPT_DIR/skills/         Local skill docs injected into prompts and delegate profiles, gitignored by default
```

## Run locally

```bash
cd /Users/shelton/Documents/wati/oncall-agent
cp .env.example .env
go run ./cmd/oncall-agent
```

`oncall-agent` loads local `.env` automatically. The default setup uses Xiaomi MiMo Token Plan with the Anthropic-compatible API:

- `LLM_PROVIDER=mimo`
- `MIMO_PROTOCOL=anthropic`
- `MIMO_API_KEY`
- `MIMO_BASE_URL=https://token-plan-cn.xiaomimimo.com/anthropic`
- `MIMO_MODEL=mimo-v2.5`
- `MIMO_THINKING=disabled`

`mimo-v2.5` is selected so Slack image uploads can be passed as multimodal `image_url` parts. MiMo thinking is disabled by default because multi-turn tool calls must preserve provider-specific reasoning fields across turns; enabling it should be done deliberately after that history path is validated.

`LLM_PROVIDER` selects the provider-specific environment namespace. `MIMO_*`, `KIMI_*`, `MOONSHOT_*`, `OPENAI_*`, and `ANTHROPIC_*` are intentionally separate; the app does not borrow MiMo tokens from Anthropic config or vice versa. `MIMO_PROTOCOL=anthropic` only means the MiMo provider uses an Anthropic-compatible transport.

You can still point the service at other providers by changing `LLM_PROVIDER`, but startup now fails if your shell and `.env` disagree on provider settings unless you explicitly set `PREFER_DOTENV=true` to use `.env`, or `ALLOW_ENV_MIXING=true` to allow the shell to keep precedence.

Prompt text can be kept out of git by placing local files under `PROMPT_DIR` (defaults to `.prompts/`, which is gitignored). Supported files are `system.md`, `delegates.json`, `app_messages.json`, `memory.json`, `tools.json`, `tool_statuses.json`, and `github_workflows.json`.

For Kimi For Coding, use the Claude Code-style Anthropic-compatible endpoint:

```bash
LLM_PROTOCOL=anthropic
LLM_ANTHROPIC_FLAVOR=claude-code
ANTHROPIC_BASE_URL=https://api.kimi.com/coding/
ANTHROPIC_AUTH_TOKEN=sk-...
ANTHROPIC_MODEL=kimi-for-coding
```

`ANTHROPIC_BASE_URL` automatically selects the Anthropic-compatible client when no `KIMI_BASE_URL`, `MOONSHOT_BASE_URL`, or `OPENAI_BASE_URL` is set. The Anthropic flavor defaults to `claude-code` for `api.kimi.com/coding` and `official` otherwise. If you want to keep a `KIMI_BASE_URL` value for this endpoint, set `LLM_PROTOCOL=anthropic` explicitly and use `https://api.kimi.com/coding/` rather than the OpenAI-compatible `/coding/v1` URL.

For the real Anthropic API, use:

```bash
LLM_PROTOCOL=anthropic
LLM_ANTHROPIC_FLAVOR=official
ANTHROPIC_BASE_URL=https://api.anthropic.com
ANTHROPIC_API_KEY=sk-ant-...
ANTHROPIC_MODEL=claude-sonnet-4-5-20250929
```

If you point the bot at `https://api.kimi.com/coding/` with the OpenAI-compatible protocol, startup still fails by default. That path must be explicitly opted into with `ALLOW_EXPERIMENTAL_CODING_ENDPOINT=true`.

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

Channel mentions are allowed by `ALLOWED_SLACK_CHANNELS`. `ALLOWED_SLACK_USERS` is used for app DMs.

The old App Home / modal flow for per-user agent tokens is not needed. If you reuse an existing Slack app, remove or disable App Home and Interactivity callbacks that only served token registration. The App Home page now shows the actual provider, base URL host, protocol, Anthropic flavor, and model the service is configured to use.

## Tools

Current tool modules:

- `code.search`, `code.read_file`
- `git.fetch_ref`, `git.search_ref`, `git.read_file_ref`, `git.status`, `git.log`, `git.show`
- `gcp.logs`
- `github.dispatch_workflow`, `github.workflow_runs`
- `notion.search`, `notion.create_page`
- `youtrack.get_issue`, `youtrack.search`
- `slack.ask_user`
- `delegate.run`

`git.fetch_ref` fetches origin and resolves the requested branch to an `origin/<branch>` ref without changing the working tree. If no branch is specified, it tries `main`, then `master`. Branch-specific analysis should use `git.search_ref` and `git.read_file_ref`, so concurrent users can inspect different branches without checkout conflicts. Sensitive actions are deliberately absent. The command guard blocks destructive command patterns, and the shipped tools use argumentized `exec.CommandContext` instead of shell strings.

GCP values in `.env` are defaults and hints, not fixed environments. `gcp.logs` accepts `project`, `namespace`, `service`, and raw `filter` per call. If `namespace` is omitted, the tool no longer silently injects `GCP_NAMESPACE`; environment mappings can be added later in `PROMPT_DIR/rules`.

GitHub Actions uses `GITHUB_TOKEN`. Set `GITHUB_DEFAULT_OWNER` and `GITHUB_DEFAULT_REPO` locally, and define workflow aliases in the gitignored `PROMPT_DIR/github_workflows.json` file. The dispatch tool requires an explicit user request plus a workflow ref and passes workflow_dispatch inputs through as strings. The runs tool can check recent workflow status after a dispatch.

## Notes

The implementation keeps provider choice configurable through env vars. `MIMO_*` is the default MiMo Token Plan backend, `OPENAI_*`, `KIMI_*`, and `MOONSHOT_*` cover alternate OpenAI-compatible backends, and `ANTHROPIC_*` is reserved for Anthropic-compatible providers such as the real Anthropic API or Kimi For Coding.
