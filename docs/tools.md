# Tools

The agent exposes tools through structured function-calling. Many heavier tool
families are deferred by default. `tool_search` lists their stable categories
and exact names; the model then activates only the exact tools it needs. Discovery supports category listing and lexical relevance-ranked search over
names, descriptions, tags, and schemas. Categories filter discovery, never bulk-load schemas. Activation is atomic and
uses the runtime context projector to admit the candidate schemas and their
acknowledgement before publishing them. Rejected loads leave active tools
unchanged and return a recoverable tool error; discovery does not grant permission.

## Code and Repository

| Tool | Description |
|---|---|
| `code-search` | Search source code at a freshly fetched current-branch remote ref by default, or use `source=working_tree` for an explicit checkout view |
| `code-read_file` | Read a source file at a freshly fetched current-branch remote ref by default, or use `source=working_tree` for an explicit checkout view |
| `repo-search` | Fetch a remote repo, pin a commit snapshot, and search it (`query_mode=literal` by default, or `regex`) |
| `repo-read_file` | Read a file from a pinned remote repo snapshot |
| `git-fetch_ref` | Fetch a branch and return an immutable ref |
| `git-search_ref` | Search code at a specific git ref (`query_mode=literal` by default, or `regex`) |
| `git-read_file_ref` | Read a file at a specific git ref |
| `git-status` | Working tree and optional branch comparison status |
| `git-log` | Recent commit history |
| `git-show` | Commit diff or file at a revision |

For ordinary `code-search` and `code-read_file` calls, omit `source`; each
repository then resolves its own checked-out branch upstream. Set `source` only
when the user explicitly names an exact ref or requests `working_tree`.

`repo-search` and `repo-read_file` resolve actual remote branches to immutable
snapshots, so concurrent users can inspect different refs without checkout
conflicts. GitHub pull requests should use `github-pr_diff` and
`github-pr_file_diff`, not a PR number or `refs/pull/...` as a branch.

Code investigation uses search followed by exact source reads. Validate behavior
with the repository's compiler, tests, linters, or other project-native checks.

## GitHub

| Tool | Description |
|---|---|
| `github-dispatch_workflow` | Trigger a `workflow_dispatch` GitHub Actions run |
| `github-workflow_runs` | List recent workflow run status |
| `github-pr_diff` | Fetch PR metadata and unified diff |
| `github-job_logs` | Fetch failed-job or paginated workflow logs |

Set `GITHUB_DEFAULT_OWNER` and `GITHUB_DEFAULT_REPO` for default repository
selection. Workflow aliases can live in `PROMPT_DIR/runtime.json`.

GitHub workflow dispatch is an external write. `github-dispatch_workflow` must
be explicitly present in `AGENT_ALLOWED_WRITE_TOOLS`; an allowed dispatch still
requires the hosted runtime's user confirmation. Read-only workflow/run queries
do not grant dispatch permission.

## Operations

| Tool | Description |
|---|---|
| `gcp-logs` | Query GCP Cloud Logging |
| `gcp-run_services` | List or describe Cloud Run services |
| `gcp-run_revisions` | List Cloud Run revisions |
| `gcp-clusters` | List or describe GKE clusters |
| `k8s-*` | Kubernetes API (pods, logs, events, rollout, metrics) — requires Google Cloud OAuth connection |
| `clickstack_connections` | Complete, compact inventory of the user's saved ClickStack instances and resolved targets |
| `clickstack_tools` | Paginated tool names or the full schema of one selected remote tool |
| `clickstack_call` | Execute an explicitly selected read-only tool on one connection instance |

### ClickStack MCP

Connection inventory does not contact remote servers, refresh tokens, or include
remote schemas. It returns every saved instance, including connections without
a custom name, with a total count and opaque `instance_id`. The stored status
is not a live access check. Names are presentation only and never select a
connection. This keeps a large tool schema from hiding later instances in an
overflow artifact or wasting model context.

Use `clickstack_tools` with the selected `instance_id` to list compact tool
names, then supply an exact `tool_name` to retrieve only that tool's full
description and schema. `clickstack_call` validates the current remote
definition's `readOnlyHint` before execution. Both resolve credentials and
targets using the call's authenticated user and selected instance, and reuse a
cached MCP router session (5-minute TTL, automatically invalidated on transport
error) to avoid repeating `initialize` and `tools/list` handshakes on every
tool call. Each request uses a fresh opaque string ID independent of client
instances, so reusing a session cannot reset its request identity. Responses
must echo the same string value; notifications carry no request ID.

For ClickHouse Cloud, each new App Home connection requires its own Service ID
and OAuth authorization. Copy `x-service-id` from the corresponding service's
Team Settings → API & Agents MCP configuration. OAuth identifies the account;
the service header selects the query target. A custom name does not select a
Cloud service. See the [ClickStack MCP documentation](https://clickhouse.com/docs/clickstack/mcp#managed-service-override).
For OSS/BYOC, set `CLICKSTACK_MCP_URL` and connect a Personal API Access Key per user via `kepler-agent connect clickstack` or App Home.

| Deployment | URL | Auth |
|---|---|---|
| ClickHouse Cloud managed ClickStack | `https://mcp.clickhouse.cloud/clickstack` (default) | Per-user OAuth and per-instance `service_id` (`x-service-id`) |
| Open Source / BYOC ClickStack | `https://<your-clickstack>/api/mcp` | Per-user Bearer token (Personal API Access Key) |

### Google Cloud (read-only OAuth)

Per-user Google OAuth with read-only scopes (`logging.read`, `cloud-platform.read-only`).
Users connect from App Home or `kepler-agent connect gcp`. Tools call GCP REST APIs with the user's token.

| Env | Purpose |
|---|---|
| `GCP_OAUTH_CLIENT_ID` / `GCP_OAUTH_CLIENT_SECRET` | OAuth app credentials |
| `CONNECTIONS_PUBLIC_BASE_URL` | OAuth callback base URL |
| `GCP_PROJECT`, `GKE_REGION`, `GCP_NAMESPACE` | Deployment defaults |

Create a Google Cloud OAuth client (Web application) with redirect URI
`{CONNECTIONS_PUBLIC_BASE_URL}/oauth/gcp/callback`.

When OAuth is configured, GCP and Kubernetes tools require the same per-user Google Cloud connection and call GCP/GKE APIs with read-only tokens.

Deploy routing headers:

- `CLICKSTACK_SERVICE_ID` — legacy deployment target for connections without saved target metadata
- `CLICKSTACK_TEAM_ID` — legacy `x-hdx-team` for OSS/BYOC multi-team setups

Per-instance `mcp_url`, `service_id`, and `team_id` select an independent target.
Selecting a new target clears inherited routing headers for another target.
The inventory reports `target_origin=connection` or `deployment_config`, so an
agent can see when two differently named connections still share the same
deployment target. Existing connections are not silently renamed or migrated;
remove and add a connection with the intended Service ID to change its target.

### Notion MCP

Notion tools are selected through stable, user-scoped routers once a usable token exists.
`notion_call` only invokes remote tools explicitly marked `readOnlyHint=true`.
`notion_write` is a separate external-write wrapper for update/create operations;
it is denied unless the exact wrapper name is added to
`AGENT_ALLOWED_WRITE_TOOLS`, and still requires user confirmation. Operators
should not add dynamically discovered remote names to the allowlist.
Users connect Notion from App Home or directly from a Slack chat connection action
(OAuth via `https://mcp.notion.com`).

| Env | Purpose |
|---|---|
| `NOTION_MCP_URL` | Notion MCP endpoint (default `https://mcp.notion.com/mcp`) |
| `CONNECTIONS_PUBLIC_BASE_URL` | OAuth callback base URL |

## Knowledge and Search

| Tool | Description |
|---|---|
| `web-search` | Public web search through DuckDuckGo, Brave, SearXNG, Google CSE, or SerpAPI |
| `web-read_page` | Fetch and read a public web page |
| `notion_connections` | List the current user's Notion instances and advertised remote tools |
| `notion_call` | Invoke an explicitly selected, explicitly read-only Notion MCP tool |
| `notion_write` | Invoke an explicitly selected Notion write tool after operator allowlisting and user confirmation |
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
`LUCKIN_MCP_TOKEN` from <https://open.lkcoffee.com/mcp>. Order creation
(`luckin-create_order`) enforces a turn-scoped preview fingerprint check: the
model must call `luckin-preview_order` with matching store, delivery mode, and
item SKUs/quantities in the same turn before creating an order. Order creation
and cancellation also require explicit user confirmation.

## Local Workspace Tools

The local CLI and stdio `app-server` share one catalog (`packages/tools/local`):

| Tool | Description |
|---|---|
| `read_file` | Read UTF-8 byte ranges from one or more workspace files |
| `write_file` | Create or overwrite a file within the workspace |
| `edit_file` | Replace exact text; require a unique match unless `replace_all` is explicit |
| `list_files` | List workspace files with `rg` |
| `search` | Search workspace file contents with `rg` |
| `exec` | Execute an argv command inside the OS sandbox, without shell parsing |
| `memory` | Manage durable project-scoped notes across sessions |
| `agent-explore` | Delegate bounded read-only exploration subtasks |
| `update_plan` | Publish structured multi-step execution progress |

Local file reads accept regular files only. Unix opens are nonblocking so a FIFO
cannot hold a turn waiting for a writer. `read_file` defaults to 64 KiB per file
and admits at most 16 MiB of requested bytes across a batch; reduce `paths` or
`limit` when the request exceeds that budget. Any shortened member marks the
whole result as truncated. Offsets and limits are byte counts, not line counts.

Local `search` and `list_files` ignore ambient ripgrep configuration and use the
same case-insensitive credential-path exclusions as direct reads. Search limits
count matching output lines across files; there is no lower implicit per-file
cap. Both tools bound captured output to 4 MiB and mark results as truncated
when either the line or byte budget is exceeded.

Both local `edit_file` and hosted `code-replace` require byte-exact `old_text`.
Indentation, trailing whitespace and line endings are not normalized. A mismatch
returns an error without changing the file; reread the source and provide the
intended exact span. Other file bytes remain unchanged by a successful edit.

Local `write_file`/`edit_file` and hosted `code-write_file`/`code-replace` share
workspace file ownership in `packages/safety`. They resolve existing ancestors
against workspace policy, then perform file I/O through an opened `os.Root`. Symlinks
must resolve inside an explicitly allowed root and sensitive targets are rejected.
Writes use a temporary sibling and atomic rename, preserving existing permission
bits without mutating other hard links. Replacement reads accept regular files
up to 16 MiB. Writes check cancellation before directory creation and before
commit. New directories use mode 0700; new files retain profile defaults (local
0644, hosted 0600). Runtime authorization and the hosted operator write allowlist
still apply.

## Registration and policy

Tool availability depends on the profile, integration connection, and surface.
This catalog is a guide, not an exhaustive list of live MCP tools. Check the
registered descriptor and actual upstream operation when changing capability
effects. See [safety](safety.md) and [development](development.md).

## Search provider configuration

`WEB_SEARCH_PROVIDER` selects `duckduckgo`, `searxng`, `brave`, `google_cse`, or
`serpapi`. Configure the corresponding endpoint/credentials in the worker:

| Provider | Configuration |
| --- | --- |
| DuckDuckGo | No paid-provider key required by this adapter |
| SearXNG | `WEB_SEARCH_SEARXNG_URL` |
| Brave | `WEB_SEARCH_BRAVE_API_KEY`, optional `WEB_SEARCH_BRAVE_BASE_URL` |
| Google CSE | `WEB_SEARCH_GOOGLE_API_KEY`, `WEB_SEARCH_GOOGLE_CX` |
| SerpAPI | `WEB_SEARCH_SERPAPI_KEY`, optional `WEB_SEARCH_SERPAPI_BASE_URL` |

The configuration loader and provider response determine actual availability;
verify a representative search after changing providers.

### MCP transport contract

The HTTP client requests protocol `2025-11-25` and accepts negotiated
`2025-03-26`, `2025-06-18`, or `2025-11-25` for the implemented tools subset.
Other or missing versions fail before the initialized notification. Subsequent
requests carry the negotiated `MCP-Protocol-Version`, including when the server
has no session ID. This is not a claim of full protocol-feature conformance or
support for the newer stateless `2026-07-28` specification.

RPC results must identify the numeric request ID and JSON-RPC 2.0 envelope.
SSE consumption stops at the matching result/error and closes its body, rather
than waiting for a persistent connection to end. Notifications and responses
for other IDs do not substitute for our result. No tool call is automatically
repeated by this transport after an uncertain response.

A response is bounded to 8 MiB. Discovery additionally bounds the complete list
to 8 MiB and 128 pages and rejects repeated cursors and duplicate/empty names.
These are resource/identity contracts, not relevance filters; oversized servers
need narrower catalogs rather than silent result truncation. Caller context and
HTTP timeout still govern network waiting. References:
[MCP lifecycle](https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle),
[Streamable HTTP](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports).
