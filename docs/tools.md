# Tools

The agent exposes tools through structured function-calling. Many heavier tool
families are deferred by default. `tool_search` lists their stable categories
and exact names; the model then activates only the named tools or categories it
needs. Discovery is explicit and deterministic rather than relevance-ranked.

## Code and Repository

| Tool | Description |
|---|---|
| `code-search` | Search source code at a freshly fetched current-branch remote ref by default, or use `source=working_tree` for an explicit checkout view |
| `code-read_file` | Read a source file at a freshly fetched current-branch remote ref by default, or use `source=working_tree` for an explicit checkout view |
| `repo-search` | Fetch a remote repo, pin a commit snapshot, search it |
| `repo-read_file` | Read a file from a pinned remote repo snapshot |
| `git-fetch_ref` | Fetch a branch and return an immutable ref |
| `git-search_ref` | Search code at a specific git ref |
| `git-read_file_ref` | Read a file at a specific git ref |
| `git-status` | Working tree and branch status |
| `git-log` | Recent commit history |
| `git-show` | Commit diff or file at a revision |
| `codegraph-overview` | Go/C# package dependency and function overview for a git branch snapshot |
| `codegraph-dependencies` | Internal imports and importers for one package |
| `codegraph-symbols` | Static Go/C# symbol search without language servers |
| `codegraph-definition` | Static Go/C# definition lookup by symbol name |
| `codegraph-references` | Static Go/C# symbol references by name |
| `codegraph-implementations` | Static Go interface implementers or C# base/interface implementations |
| `codegraph-callers` | Simple Go/C# call sites for a function or method name |
| `codegraph-callees` | Simple Go/C# outgoing calls from a function or method |
| `codegraph-callgraph` | Filtered Go/C# call graph edges |
| `codegraph-impact` | Package importers and direct callers for rough impact analysis |
| `code-symbols` | Find Go/C# symbols via language server |
| `code-definition` | Go to symbol definition |
| `code-references` | Find symbol references |
| `code-diagnostics` | LSP diagnostics for a file |

For ordinary `code-search` and `code-read_file` calls, omit `source`; each
repository then resolves its own checked-out branch upstream. Set `source` only
when the user explicitly names an exact ref or requests `working_tree`.

`repo-search` and `repo-read_file` resolve actual remote branches to immutable
snapshots, so concurrent users can inspect different refs without checkout
conflicts. GitHub pull requests should use `github-pr_diff` and
`github-pr_file_diff`, not a PR number or `refs/pull/...` as a branch.

Code-graph output is explicitly approximate: Go syntax is parsed with the Go
AST, but call edges are name-matched without type resolution; C# symbols and
calls use lexical extraction. Use LSP or source reads before making behavioral
claims from these results.

## GitHub

| Tool | Description |
|---|---|
| `github-dispatch_workflow` | Trigger a `workflow_dispatch` GitHub Actions run |
| `github-workflow_runs` | List recent workflow run status |
| `github-pr_diff` | Fetch PR metadata and unified diff |
| `github-job_logs` | Fetch failed-job or paginated workflow logs |

Set `GITHUB_DEFAULT_OWNER` and `GITHUB_DEFAULT_REPO` for default repository
selection. Workflow aliases can live in `PROMPT_DIR/runtime.json`.

## Operations

| Tool | Description |
|---|---|
| `gcp-logs` | Query GCP Cloud Logging |
| `gcp-run_services` | List or describe Cloud Run services |
| `gcp-run_revisions` | List Cloud Run revisions |
| `gcp-clusters` | List or describe GKE clusters |
| `k8s-*` | Kubernetes API (pods, logs, events, rollout, metrics) — requires Google Cloud OAuth connection |
| `diagnostics-incident_brief` | Structured incident summary |
| `diagnostics-timeline` | Incident event timeline |
| `diagnostics-evidence_board` | Structured evidence board |
| `mcp_clickstack_*` | ClickStack observability MCP tools (logs, traces, dashboards, alerts) |

### ClickStack MCP

ClickStack tools are discovered from the configured MCP endpoint once a usable token exists.
For ClickHouse Cloud, set `CLICKSTACK_SERVICE_ID` and have each user connect ClickStack from App Home (OAuth).
For OSS/BYOC, set `CLICKSTACK_MCP_URL` and connect a Personal API Access Key per user via `slack-copilot connect clickstack` or App Home.

| Deployment | URL | Auth |
|---|---|---|
| ClickHouse Cloud managed ClickStack | `https://mcp.clickhouse.cloud/clickstack` (default) | Per-user OAuth via Connections (`x-service-id` from deploy config) |
| Open Source / BYOC ClickStack | `https://<your-clickstack>/api/mcp` | Per-user Bearer token (Personal API Access Key) |

### Google Cloud (read-only OAuth)

Per-user Google OAuth with read-only scopes (`logging.read`, `cloud-platform.read-only`).
Users connect from App Home or `slack-copilot connect gcp`. Tools call GCP REST APIs with the user's token.

| Env | Purpose |
|---|---|
| `GCP_OAUTH_CLIENT_ID` / `GCP_OAUTH_CLIENT_SECRET` | OAuth app credentials |
| `CONNECTIONS_PUBLIC_BASE_URL` | OAuth callback base URL |
| `GCP_PROJECT`, `GKE_REGION`, `GCP_NAMESPACE` | Deployment defaults |

Create a Google Cloud OAuth client (Web application) with redirect URI
`{CONNECTIONS_PUBLIC_BASE_URL}/oauth/gcp/callback`.

When OAuth is configured, GCP and Kubernetes tools require the same per-user Google Cloud connection and call GCP/GKE APIs with read-only tokens.

Deploy routing headers:

- `CLICKSTACK_SERVICE_ID` — required for Cloud; shared `x-service-id` for the whole team
- `CLICKSTACK_TEAM_ID` — `x-hdx-team` for OSS/BYOC multi-team setups

### Notion MCP

Notion tools are discovered from the hosted Notion MCP endpoint once a usable token exists.
Users connect Notion from App Home or `slack-copilot connect notion` (OAuth via `https://mcp.notion.com`).

| Env | Purpose |
|---|---|
| `NOTION_MCP_URL` | Notion MCP endpoint (default `https://mcp.notion.com/mcp`) |
| `CONNECTIONS_PUBLIC_BASE_URL` | OAuth callback base URL |

## Knowledge and Search

| Tool | Description |
|---|---|
| `web-search` | Public web search through DuckDuckGo, Brave, SearXNG, Google CSE, or SerpAPI |
| `web-read_page` | Fetch and read a public web page |
| `mcp_notion_*` | Notion MCP tools (search, fetch, create, update pages and databases) |
| `youtrack-get_issue` | Fetch a YouTrack issue |
| `youtrack-search` | Search YouTrack issues |
| `knowledge-runbook_search` | Search local runbooks under `PROMPT_DIR/runbooks/` |

Final Slack answers append a concise evidence section when the turn used
`web-search` or `web-read_page`.

## Slack

| Tool | Description |
|---|---|
| `slack-ask_user` | Ask for missing information and pause the run |
| `slack-file_search` | Search a large uploaded Slack file |
| `slack-json_analyze` | Structurally analyze uploaded JSON |

## Agent Skills

| Tool | Description |
|---|---|
| `skills-load` | Load full instructions for a named skill |

## Luckin Coffee

Luckin order management uses the official MCP endpoint and requires
`LUCKIN_MCP_TOKEN` from <https://open.lkcoffee.com/mcp>. Order creation and
cancellation require explicit confirmation.
