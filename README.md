# oncall-agent

A Slack-native intelligent assistant built in Go, powered by configurable LLM backends. It handles engineering investigations, code analysis, CI/CD operations, and general-purpose tasks — all within a Slack thread, with full tool-call support and structured context management.

## ✨ Design principles

- 💬 **Slack-native conversation model.** Events arrive via the Slack Events API (`app_mention`, `message`, `file_shared`, `app_home_opened`). Each thread has its own session; follow-up mentions and pending clarification replies reuse existing context without re-reading thread history.
- 🔧 **Structured tool calls, not prompt parsing.** The model communicates with tools through the provider's native function-calling API (OpenAI-compatible or Anthropic-compatible), so arguments are typed and validated rather than parsed from free-form text.
- 🔒 **Layered runtime safety.** Slack user/channel authorization, system prompt guardrails, post-response secret redaction, workspace path allowlists, command deny rules, and per-tool read-only vs. action boundaries are all code-enforced.
- 📦 **Explicit context budgets.** Thread context, session history, and tool observations are bounded and compressed before reaching the model. Large Slack files stay searchable by file ID without flooding the context window.
- 🗂️ **Layered prompt configuration.** Generic prompts live in the committed `prompts/` directory. Only small sensitive deployment addenda, such as company-specific repository names, workflow aliases, and internal runbook references, belong in a local `PROMPT_DIR` overlay (defaults to `.prompts/`, gitignored).

## 📁 Project layout

```text
cmd/oncall-agent/          Process entrypoint
internal/app/              HTTP server, Slack event routing, dependency wiring
internal/slack/            Signature verification, Events API types, Web API client
internal/conversation/     Thread lifecycle, per-session locks, idempotency, pending replies
internal/agent/            Provider-agnostic tool-call runner with step budget and context compression
internal/memory/           Conversation turns, context packing, tool-result formatting
internal/session/          File-backed Slack thread sessions
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

## 🤖 LLM configuration

`LLM_PROVIDER` selects the active provider. Each provider has its own env namespace so credentials are never shared between providers unintentionally.

**MiMo (default)**

```bash
LLM_PROVIDER=mimo
MIMO_PROTOCOL=anthropic
MIMO_API_KEY=...
MIMO_BASE_URL=https://token-plan-cn.xiaomimimo.com/anthropic
MIMO_MODEL=mimo-v2.5
MIMO_THINKING=disabled
```

MiMo thinking is disabled by default because multi-turn tool calls must preserve provider-specific reasoning fields across turns. Enable it only after validating that path.

**Kimi For Coding**

```bash
LLM_PROTOCOL=anthropic
LLM_ANTHROPIC_FLAVOR=claude-code
ANTHROPIC_BASE_URL=https://api.kimi.com/coding/
ANTHROPIC_AUTH_TOKEN=sk-...
ANTHROPIC_MODEL=kimi-for-coding
```

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

### 🖼️ Multimodal and model switching

`AVAILABLE_MODELS` enables a model selector in the Slack App Home tab. `MULTIMODAL_MODELS` controls which models receive image parts; images sent to non-listed models are stripped and replaced with a text description prompt.

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
| `runner.json` | Retry and budget-warning prompt templates |
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

The App Home tab shows the configured provider, model, base URL, and protocol. It also includes a model selector when `AVAILABLE_MODELS` is set.

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
| `rag-search` | Hybrid semantic + full-text code search across indexed repositories |

`repo-search` and `repo-read_file` fetch only the requested repository on demand and resolve the branch to an immutable snapshot, so concurrent users can inspect different branches without checkout conflicts.

### 🐙 GitHub

| Tool | Description |
|---|---|
| `github.dispatch_workflow` | Trigger a `workflow_dispatch` GitHub Actions run |
| `github.workflow_runs` | List recent workflow run status |
| `github.pr_diff` | Fetch a PR's metadata and unified diff |

Workflow aliases can be defined in `PROMPT_DIR/runtime.json` under `github_workflows` or in the legacy `PROMPT_DIR/github_workflows.json`. `GITHUB_DEFAULT_OWNER` and `GITHUB_DEFAULT_REPO` set the default repository.

### ☁️ Observability

| Tool | Description |
|---|---|
| `gcp.logs` | Query GCP Cloud Logging (project, namespace, service, or raw filter) |
| `diagnostics.incident_brief` | Structured incident diagnostic summary |
| `diagnostics.timeline` | Incident event timeline |
| `diagnostics.evidence_board` | Structured evidence board |

### 🔎 Knowledge and search

| Tool | Description |
|---|---|
| `web-search` | Public web search (Google Custom Search or SerpAPI) |
| `web-read_page` | Fetch and read a public web page |
| `notion.search` | Search Notion pages |
| `notion.create_page` | Create a Notion page |
| `youtrack.get_issue` | Fetch a YouTrack issue |
| `youtrack.search` | Search YouTrack issues |
| `knowledge.runbook_search` | Search local runbooks under `PROMPT_DIR/runbooks/` |

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

## 🧠 RAG semantic code search

RAG indexes repositories into PostgreSQL with pgvector and provides hybrid search combining vector similarity (70%), full-text (30%), and grep.

**Start the local database:**

```bash
docker compose -f docker-compose.rag.yml up -d
```

**Configuration:**

```bash
RAG_ENABLED=true
RAG_POSTGRES_DSN=postgres://oncall:oncall@localhost:5432/oncall_rag?sslmode=disable

# Embedding provider — any OpenAI-compatible /v1/embeddings endpoint works:
# 🖥️  Local Ollama:    http://localhost:11434/v1       model: nomic-embed-text  dims: 768
# ⚡  SiliconFlow:     https://api.siliconflow.cn/v1   model: BAAI/bge-m3       dims: 1024
# 🌐  OpenAI:          https://api.openai.com/v1        model: text-embedding-3-small  dims: 1536
RAG_EMBEDDING_BASE_URL=http://localhost:11434/v1
RAG_EMBEDDING_API_KEY=ollama
RAG_EMBEDDING_MODEL=nomic-embed-text
RAG_EMBEDDING_DIMS=768

RAG_BACKGROUND_INDEX=true   # optional workspace prewarming on startup
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
