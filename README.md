# oncall-agent

A Slack-native general-purpose intelligent assistant built in Go, powered by configurable LLM backends. It helps with everyday questions, research, planning, writing, education, engineering, code analysis, CI/CD operations, and other practical tasks — all within a Slack thread, with full tool-call support and structured context management.

## ✨ Design principles

- 💬 **Slack-native conversation model.** Events arrive via the Slack Events API (`app_mention`, `message`, `file_shared`, `app_home_opened`). Each thread has its own session; follow-up mentions and pending clarification replies reuse existing context without re-reading thread history.
- 🔧 **Structured tool calls, not prompt parsing.** The model communicates with tools through the provider's native function-calling API (OpenAI-compatible or Anthropic-compatible), so arguments are typed and validated rather than parsed from free-form text.
- 🔒 **Layered runtime safety.** Slack user/channel authorization, system prompt guardrails, post-response secret redaction, workspace path allowlists, command deny rules, and per-tool read-only vs. action boundaries are all code-enforced.
- 📦 **Explicit context boundaries.** Slack thread context and agent conversation history are preserved as-is unless an LLM compact summary is produced. Tool results are the only locally lossy context: large outputs are persisted for `tool_spill-read` slice-by-slice analysis, and old tool results may be cleared or capped to protect the context window.
- 🗂️ **Layered prompt configuration.** Generic prompts live in the committed `prompts/` directory. Only small sensitive deployment addenda, such as company-specific repository names, workflow aliases, and internal runbook references, belong in a local `PROMPT_DIR` overlay (defaults to `.prompts/`, gitignored).

## 📁 Project layout

```text
cmd/oncall-agent/          Process entrypoint
internal/app/              HTTP server, Slack event routing, dependency wiring
internal/slack/            Signature verification, Events API types, Web API client
internal/conversation/     Thread lifecycle, per-session locks, idempotency, pending replies
internal/agent/            Provider-agnostic tool-call runner with step budget, tool-result spill, and LLM compaction
internal/memory/           Conversation turns, context packing, tool-result formatting, and compaction helpers
internal/session/          PostgreSQL-backed Slack thread sessions
internal/safety/           Access policy, prompt policy, secret redaction, workspace and command policy
internal/health/           Tool and RAG health probing, health dashboard
internal/prompts/          Prompt catalog: loads public defaults, then private PROMPT_DIR overrides
internal/delegation/       Focused delegate agent profiles for bounded sub-tasks
internal/observability/    In-memory metrics, cost tracking, reaction-based quality feedback
internal/llm/              Anthropic and OpenAI-compatible LLM clients with streaming
internal/rag/              Semantic code search: chunking, embedding, pgvector store, hybrid search
internal/toolkit/tools/    All tool modules: code, git, github, gcp, notion, youtrack, slack, rag, web
prompts/                   Committed generic prompt defaults
PROMPT_DIR/                Optional private prompt addendum, defaults to .prompts/ and stays gitignored
```

## 🚀 Running locally

```bash
cp .env.example .env
# fill in required values, then:
go run ./cmd/oncall-agent
```

The server loads `.env` automatically on startup. Expose `POST /slack/events` through a public HTTPS URL (see [ngrok](#-ngrok) below).

### Health and shutdown

The service exposes lightweight operational endpoints:

| Endpoint | Purpose |
|---|---|
| `GET /livez` | Liveness probe. Returns `ok` while the process is alive. |
| `GET /readyz` | Readiness probe. Fails while the process is draining or storage wiring is unavailable. |
| `POST /drain` | Local-only drain switch used by Kubernetes `preStop`; it stops new Slack work before SIGTERM. |

Slack events are first written to a durable PostgreSQL inbox and then claimed by
workers with a time-bounded lease. Tune `SLACK_EVENT_TIMEOUT` for the maximum
duration of one event and `SLACK_EVENT_INBOX_LEASE` for how long a claimed event
can stay owned before another worker may retry it. By default the lease is one
minute longer than the event timeout.

For multi-replica deployments, keep database connections bounded with
`POSTGRES_MAX_CONNS` or the per-store overrides:
`POSTGRES_SESSION_MAX_CONNS`, `POSTGRES_RUNS_MAX_CONNS`,
`POSTGRES_REMINDER_MAX_CONNS`, `POSTGRES_INBOX_MAX_CONNS`, and
`RAG_POSTGRES_MAX_CONNS`.

### Docker and Kubernetes

A production-oriented Dockerfile and starter Kubernetes manifests live under
`deploy/k8s/`. Start with one replica, then scale after PostgreSQL capacity,
Slack retries, and any workspace/RAG background jobs are understood for your
environment.

## 🤖 LLM configuration

`LLM_PROVIDER` selects the active provider. Each provider has its own env namespace so credentials are never shared between providers unintentionally.

**LongCat (default)**

LongCat 2.0 is a high-performance Agentic model with a 1M-token context window and 128K max output. It exposes both OpenAI-compatible and Anthropic-compatible endpoints; the integration uses the OpenAI-compatible path by default.

```bash
LLM_PROVIDER=longcat
LONGCAT_API_KEY=Bearer lc-...
LONGCAT_BASE_URL=https://api.longcat.chat/anthropic
LONGCAT_MODEL=LongCat-2.0
LONGCAT_PROTOCOL=anthropic
```

The API key must be prefixed with `Bearer ` (e.g. `Bearer lc-abc123`) so the client sends the correct `Authorization` header. To use the OpenAI-compatible endpoint instead, set `LONGCAT_BASE_URL=https://api.longcat.chat/openai`, `LONGCAT_PROTOCOL=openai`, and drop the `Bearer ` prefix.

**DeepSeek**

DeepSeek uses the OpenAI-compatible `/chat/completions` protocol, including
structured function/tool calls. The default model is the official flash model;
`DEEPSEEK_AVAILABLE_MODELS` exposes both official V4 models in Slack.

```bash
LLM_PROVIDER=deepseek
DEEPSEEK_PROTOCOL=openai
DEEPSEEK_API_KEY=sk-...
DEEPSEEK_BASE_URL=https://api.deepseek.com
DEEPSEEK_MODEL=deepseek-v4-flash
DEEPSEEK_AVAILABLE_MODELS=deepseek-v4-flash,deepseek-v4-pro
```

**MiMo**

```bash
LLM_PROVIDER=mimo
MIMO_PROTOCOL=anthropic
MIMO_API_KEY=...
MIMO_BASE_URL=https://token-plan-cn.xiaomimimo.com/anthropic
MIMO_MODEL=mimo-v2.5
MIMO_THINKING=disabled
```

MiMo thinking is disabled by default because multi-turn tool calls must preserve provider-specific reasoning fields across turns. Enable it only after validating that path.

**CLIProxyAPI (Kimi or Codex through a local gateway)**

```bash
LLM_PROVIDER=cliproxyapi
CLIPROXYAPI_BASE_URL=http://127.0.0.1:8317/v1
CLIPROXYAPI_API_KEY=your-local-gateway-key
CLIPROXYAPI_MODEL=kimi/kimi-k2.7-code
CLIPROXYAPI_AVAILABLE_MODELS=kimi/kimi-k2.7-code,codex/gpt-5-codex
```

Run and authenticate [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)
locally first. It exposes OpenAI-compatible endpoints and manages provider
authentication separately; this application stores only the gateway API key.
For Codex, complete the gateway's supported login flow and select the model
name exposed by its `/v1/models` endpoint. Direct requests to Kimi's
`/coding` endpoint that imitate another client are intentionally unsupported.

**Anthropic**

```bash
LLM_PROTOCOL=anthropic
LLM_ANTHROPIC_FLAVOR=official
ANTHROPIC_BASE_URL=https://api.anthropic.com
ANTHROPIC_API_KEY=sk-ant-...
ANTHROPIC_MODEL=claude-sonnet-4-5-20250929
```

**OpenAI-compatible**

```bash
LLM_PROVIDER=openai
OPENAI_API_KEY=...
OPENAI_BASE_URL=https://api.openai.com/v1
OPENAI_MODEL=gpt-4o
```

**OpenCode Zen**

Use this for the free OpenCode Zen models. It is intentionally separate
from OpenCode Go so the free key and model list do not collide with the
Go subscription provider.

```bash
LLM_PROVIDER=opencode-zen
OPENCODE_ZEN_API_KEY=...
OPENCODE_ZEN_BASE_URL=https://opencode.ai/zen/v1
OPENCODE_ZEN_MODEL=mimo-v2.5-free
OPENCODE_ZEN_PROTOCOL=openai
```

```bash
OPENCODE_ZEN_AVAILABLE_MODELS=mimo-v2.5-free,minimax-m3-free,nemotron-3-ultra-free,north-mini-code-free
```

**OpenCode Go**

OpenCode exposes selected subscription models through OpenAI-compatible
`/chat/completions` and Anthropic-compatible `/messages` endpoints. The default
here uses the OpenAI-compatible endpoint and `glm-5.2`. When `OPENCODE_GO_AVAILABLE_MODELS`
is omitted, the app exposes the full OpenCode model list in Slack. The runtime routes each selected model to the
documented OpenAI-compatible or Anthropic-compatible endpoint automatically.

```bash
LLM_PROVIDER=opencode-go
OPENCODE_GO_API_KEY=...
OPENCODE_GO_BASE_URL=https://opencode.ai/zen/go/v1
OPENCODE_GO_MODEL=glm-5.2
OPENCODE_GO_PROTOCOL=openai
```

`OPENCODE_GO_PROTOCOL` describes the default protocol shown in configuration.
You do not need to restart or change this value when a user picks a `/messages`
model such as `minimax-m3` or `qwen3.7-max`; the OpenCode client handles
that per request.

```bash
OPENCODE_GO_AVAILABLE_MODELS=glm-5.2,kimi-k2.7-code,mimo-v2.5
```

### 🧠 Primary and secondary models

The primary model handles user-facing answers. The optional secondary model is
used for cheaper/faster background work such as read-only code exploration and
context compact summaries.

```bash
SECONDARY_PROVIDER=opencode-zen
OPENCODE_ZEN_API_KEY=...
SECONDARY_MODEL=mimo-v2.5-free
```

When `SESSION_COMPACT_MODEL` is unset, compact summaries use `SECONDARY_MODEL`
when configured, otherwise they fall back to the primary model.

### Repository freshness

Code-reading tools keep Claude Code-style snapshot semantics without fetching
on every request. The first code/repo read for a repository refreshes `origin`
refs only when that repo has not been fetched in the last 5 minutes, then reads
from the upstream ref with `git show`/`git grep` without touching the working
tree. This keeps Slack responses responsive while avoiding stale branch
analysis for long-running sessions.

Set `WORKSPACE_AUTO_FETCH=true` to also refresh workspace repositories in the
background every 5 minutes. The background fetch shares the same process-wide
cache as on-demand tool reads.

### 💬 Streaming responsiveness

Final answer streaming is flushed in small batches to keep the UI responsive without sending one request per token. These settings can be tuned from the environment; duration values accept either milliseconds as a number or Go duration strings such as `35ms`.

| Variable | Default | Purpose |
|---|---:|---|
| `WEB_STREAM_FLUSH_INTERVAL` | `16ms` | Maximum time to buffer generated text before appending a web SSE stream chunk. |
| `WEB_STREAM_FLUSH_CHARS` | `16` | Maximum buffered characters before appending a web SSE stream chunk. |
| `SLACK_STREAM_FLUSH_INTERVAL` | `100ms` | Maximum time to buffer generated text before calling Slack `chat.appendStream`. |
| `SLACK_STREAM_FLUSH_CHARS` | `96` | Maximum buffered characters before calling Slack `chat.appendStream`. |
| `STREAM_FLUSH_INTERVAL` | `35ms` | Shared fallback used when the Slack-specific interval is not set. |
| `STREAM_FLUSH_CHARS` | `32` | Shared fallback used when the Slack-specific character threshold is not set. |

### 🖼️ Multimodal and model display

Slack App Home shows the configured primary plus Explorer / Summary models.
`MODEL_ROUTING_MULTIMODAL_MODEL` controls the internal model used for
image-containing turns, while `MULTIMODAL_MODELS` controls which models receive
image parts; images sent to non-listed models are stripped and replaced with a
text description prompt. The multimodal routing model is not exposed in the UI.

For deployments with separate text and vision-capable models, set the primary
provider model as usual, then set `MODEL_ROUTING_MULTIMODAL_MODEL` to the
vision-capable model and include it in `MULTIMODAL_MODELS`.

## 📝 Prompt configuration

Prompt text is loaded in two layers:

1. `prompts/` contains committed, generic defaults that are safe to maintain in git.
2. `PROMPT_DIR` (defaults to `.prompts/`, gitignored) adds only sensitive deployment-specific supplements.

Put generic assistant behavior, retry prompts, memory labels, public tool descriptions, and reusable coding rules in `prompts/`. Put only narrow sensitive details in `PROMPT_DIR`, such as company-specific repository names, workflow aliases, private runbook references, or a short local identity addendum.

Keep the main assistant behavior in git so remote branches and local deployments stay aligned. The private `PROMPT_DIR/agent.md` is appended as a small local addendum for sensitive deployment details; it should not carry a full fork of the main prompt.

The private overlay should stay intentionally small:

| File | Purpose |
|---|---|
| `agent.md` | Short local addendum appended after the git-tracked main prompt. Do not fork the whole main prompt here. |
| `runtime.json` | Optional private runtime mappings, mainly workflow aliases. |
| `tools.json` | Optional private tool-description addenda for internal repositories or workflows. |
| `runbooks/*.md` | Optional private operational runbooks searched by `knowledge.runbook_search`. |
| `skills/<name>/SKILL.md` | Optional private skills for genuinely internal workflows. |

The committed `prompts/` directory contains only runtime prompt files. Do not add README-style placeholder files under loaded directories such as `runbooks/`; if a file can be read by a tool or prompt loader, its content should be useful at runtime.

| File | Purpose |
|---|---|
| `system.md` | Main system prompt |
| `delegates.json` | System prompts for delegate sub-agents |
| `app_messages.json` | Responses to empty mentions, empty DMs, file-only DMs |
| `tools.json` | Tool description and parameter overrides |
| `memory.json` | Labels for session summary and thread context blocks |
| `runner.json` | Retry prompt templates for final-answer validation |
| `health.json` | Health summary header and rules text |
| `tool_statuses.json` | Slack status messages shown while tools run |
| `texts.json` | Shared prompt snippets such as section headers and context wrappers |
| `rules/*.md` | Markdown rules injected into the main agent and delegates |
| `skills/<name>/SKILL.md` | Skill definitions with `name` and `description` frontmatter |
| `runbooks/*.md` | Service runbooks searched by `knowledge.runbook_search` |

Only skill metadata appears in the base prompt; full skill instructions are loaded on demand when the agent calls `skills-load`.

Repository inventory is not injected into the system prompt by default, because repository names can be sensitive. Set `PROMPT_INCLUDE_REPO_INVENTORY=true` only for deployments where sending local repository names to the model provider is acceptable.

### Private overlay example

A minimal `.prompts/` (or `PROMPT_DIR`) setup:

```text
.prompts/
  agent.md            # Short identity addendum appended after system.md
  tools.json          # Override tool descriptions for internal repos/workflows
  runtime.json        # Workflow aliases, status text, and other runtime mappings
  rules/              # Internal policy rules (matched by filename with public rules)
  runbooks/           # Operational runbooks searched by knowledge-runbook_search
  skills/             # Internal skills (matched by name with public skills)
```

**`agent.md`** — keep it short; the main behavior lives in the committed `system.md`:

```markdown
Identity:
- Your name is <ASSISTANT_NAME>.
- You serve the <COMPANY_NAME> engineering team.

Deployment-specific CI/CD:
- The default GitHub workflow repository is `<ORG>/<REPO>`.
- For service deployments, use the `deploy` workflow alias.
```

**`runtime.json`** — compact overlay for workflow aliases and other runtime mappings:

```json
{
  "github_workflows": {
    "deploy": "cicd-deploy.yml",
    "rollback": "cicd-rollback.yml"
  }
}
```

**`tools.json`** — override specific tool descriptions for internal context:

```json
{
  "github-dispatch_workflow": {
    "description": "Trigger a workflow in <ORG>/<REPO>.",
    "parameters": {
      "repository": "Defaults to <ORG>/<REPO>."
    }
  }
}
```

Private files are merged on top of public defaults at startup. See `internal/prompts/catalog.go` for the full merge semantics.

## 🌐 ngrok

```bash
ngrok http 8080
```

Use the HTTPS forwarding URL as the Slack Request URL:

```
https://<your-ngrok-domain>/slack/events
```

Set `HTTP_ADDR=:8080`, fill `SLACK_SIGNING_SECRET` from **Slack App → Basic Information**, and keep Socket Mode disabled. A reserved ngrok domain avoids regenerating the URL on each restart.

## ⚙️ Slack app setup

Required OAuth scopes:

- `app_mentions:read`
- `channels:history`, `groups:history`, `im:history` (for thread context)
- `chat:write`
- `files:read` (for file downloads)
- `chat:delete` (optional, to remove the temporary thinking indicator)

Event subscriptions: `app_mention`, `message.channels`, `message.groups`, `message.im`, `app_home_opened`, `file_shared`, `reaction_added`.

- `ALLOWED_SLACK_CHANNELS` controls which channels the bot responds to in channel threads.
- `ALLOWED_SLACK_USERS` controls who can use the bot in app DMs.

The App Home tab shows access status plus the configured primary and Explorer / Summary models.

## 🛠️ Tools

### 🔍 Code and repository

| Tool | Description |
|---|---|
| `code.search` | ripgrep search across the local working tree |
| `code.read_file` | Read a file from the local working tree |
| `repo-search` | Lazily fetch a remote repo, pin a commit snapshot, search it |
| `repo-read_file` | Read a file from a pinned remote repo snapshot |
| `git.fetch_ref` | Fetch a branch and return an immutable ref for subsequent calls |
| `git.search_ref` | Search code at a specific git ref |
| `git.read_file_ref` | Read a file at a specific git ref |
| `git.status` | Working tree and branch status |
| `git.log` | Recent commit history |
| `git.show` | Commit diff or file at a revision |
| `code.symbols` | Find Go/C# symbols via language server (gopls / csharp-ls) |
| `code.definition` | Go to symbol definition |
| `code.references` | Find symbol references |
| `code.diagnostics` | LSP diagnostics for a file |
| `rag-search` | Optional hybrid semantic + full-text code search across indexed repositories |

`repo-search` and `repo-read_file` fetch only the requested repository on demand and resolve the branch to an immutable snapshot, so concurrent users can inspect different branches without checkout conflicts.
By default, code questions are handled with agentic search across grep, repo snapshots, file reads, and LSP tools. Enable `rag-search` only when semantic recall is useful as a hint source.

### 🐙 GitHub

| Tool | Description |
|---|---|
| `github-dispatch_workflow` | Trigger a `workflow_dispatch` GitHub Actions run |
| `github-workflow_runs` | List recent workflow run status |
| `github-pr_diff` | Fetch a PR's metadata and unified diff |
| `github-job_logs` | Fetch failed-job logs or paginated job logs for a workflow run |

Workflow aliases can be defined in `PROMPT_DIR/runtime.json` under `github_workflows` or in the legacy `PROMPT_DIR/github_workflows.json`. `GITHUB_DEFAULT_OWNER` and `GITHUB_DEFAULT_REPO` set the default repository.

### ☁️ Observability

| Tool | Description |
|---|---|
| `gcp-logs` | Query GCP Cloud Logging (project, namespace, service, or raw filter) |
| `k8s-get_pods` | List Kubernetes pods with status, restarts, and node placement |
| `k8s-describe` | Describe Kubernetes resources and events |
| `k8s-logs` | Fetch Kubernetes pod logs |
| `k8s-top` | Show Kubernetes CPU and memory usage |
| `readonly-shell` | Run one allowlisted read-only CLI command for `gcloud`, `kubectl`, `gh`, or `date` |
| `diagnostics-incident_brief` | Structured incident diagnostic summary |
| `diagnostics-timeline` | Incident event timeline |
| `diagnostics-evidence_board` | Structured evidence board |

### 🔎 Knowledge and search

| Tool | Description |
|---|---|
| `web-search` | Public web search (DuckDuckGo by default, or Brave / SearXNG / Google Custom Search / SerpAPI) |
| `web-read_page` | Fetch and read a public web page |
| `notion.search` | Search Notion pages |
| `notion.create_page` | Create a Notion page |
| `youtrack.get_issue` | Fetch a YouTrack issue |
| `youtrack.search` | Search YouTrack issues |
| `knowledge.runbook_search` | Search local runbooks under `PROMPT_DIR/runbooks/` |

Final Slack answers automatically append a concise "Web Evidence" / "网页证据" section when the turn used `web-search` or `web-read_page`, so URLs remain visible even if the model forgets to list sources.

`web-search` is available without paid credentials by default through DuckDuckGo HTML search:

```bash
WEB_SEARCH_PROVIDER=duckduckgo
```

For a local/self-hosted search stack, run SearXNG and point the agent at it:

```bash
WEB_SEARCH_PROVIDER=searxng
WEB_SEARCH_SEARXNG_URL=http://127.0.0.1:8097
```

For more reliable hosted JSON search without scraping HTML, configure Brave Search:

```bash
WEB_SEARCH_PROVIDER=brave
WEB_SEARCH_BRAVE_API_KEY=...
WEB_SEARCH_BRAVE_BASE_URL=https://api.search.brave.com/res/v1/web/search
```

Google Custom Search and SerpAPI remain supported for deployments that already have keys:

```bash
WEB_SEARCH_PROVIDER=google_cse
WEB_SEARCH_GOOGLE_API_KEY=...
WEB_SEARCH_GOOGLE_CX=...

WEB_SEARCH_PROVIDER=serpapi
WEB_SEARCH_SERPAPI_KEY=...
WEB_SEARCH_SERPAPI_BASE_URL=https://serpapi.com/search.json
```

### 💬 Slack

| Tool | Description |
|---|---|
| `slack.ask_user` | Ask the user for missing information and pause the run |
| `slack.file_search` | Search a large uploaded Slack file by query |
| `slack.json_analyze` | Structurally analyze an uploaded JSON file |

### 🤝 Agent control

| Tool | Description |
|---|---|
| `skills-load` | Load full SKILL.md instructions for a named skill |
| `delegate.run` | Run a focused sub-agent for bounded analysis without tools |

### ☕ Luckin Coffee

Order management via the official Luckin MCP endpoint. Requires `LUCKIN_MCP_TOKEN` from <https://open.lkcoffee.com/mcp>. Order creation and cancellation require an explicit confirmation step.

### 🌐 Browser automation (Playwright)

Headless browser control via a local Playwright MCP server. Set `PLAYWRIGHT_MCP_URL` to enable; leave it unset to disable all `pw-*` tools entirely.

Start the MCP server with Docker:

```bash
docker run -d \
  --name playwright-mcp \
  --restart unless-stopped \
  -p 8931:8931 \
  -v "$(pwd)/scripts/playwright-stealth.js:/stealth.js:ro" \
  mcr.microsoft.com/playwright/mcp:latest \
  --port 8931 --host 0.0.0.0 --headless \
  --no-sandbox \
  --browser chromium \
  --viewport-size 1920x1080 \
  --user-agent "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36" \
  --ignore-https-errors \
  --init-script /stealth.js
```

Then set in your env:

```
PLAYWRIGHT_MCP_URL=http://localhost:8931/mcp
```

> **Note:** Use `--browser chromium`, not `--browser chrome`. The official `mcr.microsoft.com/playwright/mcp` image ships Chromium only; passing `--browser chrome` causes the server to start but fail silently on every navigation (all pages land on `about:blank`).

> **Stealth mode:** The `-v` mount and `--init-script /stealth.js` flags load `scripts/playwright-stealth.js` into every page context before the page's own JavaScript runs. This suppresses the primary headless-browser detection signals (`navigator.webdriver`, missing `window.chrome`, empty `navigator.plugins`). If you omit the `--init-script`, the agent still injects a lightweight patch via `browser_evaluate` after each navigation, but that runs after page init so it may miss early detection scripts. For best results use `--init-script`.

Available tools: `pw-navigate`, `pw-snapshot`, `pw-click`, `pw-type`, `pw-fill_form`, `pw-screenshot`, `pw-press_key`, `pw-wait`, `pw-evaluate`, `pw-get_all_pages`, `pw-switch_page`. Use element `ref` values from `pw-snapshot` with `pw-click` and `pw-type`; the older `target` key is still accepted as a compatibility alias. Navigation, screenshots, snapshots, and page-mutating actions include automatic page-state stabilization and about:blank tab recovery, so the recommended test loop is `pw-navigate` → `pw-snapshot` → action → `pw-snapshot`/`pw-screenshot` to verify the result. Screenshots are stored internally for Slack upload instead of being placed directly in LLM context. Browser state is scoped to a single agent turn — each new Slack message starts a fresh session.

Run the real browser smoke test after the MCP server is up:

```bash
PLAYWRIGHT_MCP_URL=http://127.0.0.1:8931/mcp go test ./internal/toolkit/tools/playwright -run TestIntegration_PlaywrightMCPRealBrowserSmoke -count=1 -v
```

This test opens a real page through Playwright MCP, reads snapshot refs, types into an input, clicks a button, verifies DOM state, and captures a screenshot. It is skipped unless `PLAYWRIGHT_MCP_URL` is set, so normal `go test ./...` runs do not require a browser.

## 📊 Observability endpoints

| Endpoint | Description |
|---|---|
| `GET /healthz` | Liveness check |
| `GET /health/dashboard` | Interactive health dashboard (browser) |
| `GET /health/tools` | Tool health status (JSON) |
| `GET /metrics` | Run and cost metrics |
| `GET /runs?limit=20` | Recent run list |
| `GET /runs/<run_id>` | Run detail with LLM/tool steps, token usage, and cost |

`/metrics` and `/runs` require `Authorization: Bearer <token>` or `X-Admin-Token: <token>` matching `OBSERVABILITY_TOKEN`. Set `OBSERVABILITY_ALLOW_UNAUTHENTICATED=true` only for direct loopback development access.

## 🧠 Optional RAG semantic code search

Code questions use agentic search by default: the assistant chooses among grep, immutable repo snapshot search, file reads, and LSP symbol/definition/reference tools. RAG can be enabled as an additional hint source for architectural or fuzzy natural-language queries.

When enabled, RAG indexes repositories into PostgreSQL with pgvector and provides hybrid search combining vector similarity (70%), full-text (30%), and grep.

**Start the local database:**

```bash
docker compose -f docker-compose.rag.yml up -d
```

**Configuration:**

```bash
RAG_ENABLED=false
POSTGRES_DSN=postgres://oncall:oncall@localhost:5432/oncall?sslmode=disable
RAG_POSTGRES_DSN= # optional; defaults to POSTGRES_DSN

# Embedding provider — any OpenAI-compatible /v1/embeddings endpoint works:
# 🖥️  Local Ollama:    http://localhost:11434/v1       model: nomic-embed-text  dims: 768
# ⚡  SiliconFlow:     https://api.siliconflow.cn/v1   model: BAAI/bge-m3       dims: 1024
# 🌐  OpenAI:          https://api.openai.com/v1        model: text-embedding-3-small  dims: 1536
RAG_EMBEDDING_BASE_URL=http://localhost:11434/v1
RAG_EMBEDDING_API_KEY=ollama
RAG_EMBEDDING_MODEL=nomic-embed-text
RAG_EMBEDDING_DIMS=768

RAG_BACKGROUND_INDEX=false  # set true for periodic workspace prewarming
RAG_INDEX_INTERVAL=5m
```

Indexing is incremental: only changed files are re-chunked, and only chunks whose content changed are re-embedded. The agent also queues per-repo indexing on demand when `rag-search` is called for an un-indexed repository.

## 💰 Cost tracking

Each Slack request is recorded as a run under `RUN_DATA_DIR` (default `.data/runs`). Runs include LLM and tool steps, token usage, estimated cost, errors, Slack message linkage, and quality feedback from emoji reactions.

Override cost rates to match your provider:

```bash
LLM_INPUT_COST_PER_MTOK=0
LLM_OUTPUT_COST_PER_MTOK=0
LLM_CACHE_READ_COST_PER_MTOK=0
LLM_CACHE_CREATION_COST_PER_MTOK=0
```
