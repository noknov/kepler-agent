# Tools

The agent exposes tools through structured function-calling. Many heavier tool
families are deferred by default and become available through `tool_search`.

## Code and Repository

| Tool | Description |
|---|---|
| `code.search` | ripgrep search across the local working tree |
| `code.read_file` | Read a file from the local working tree |
| `repo-search` | Fetch a remote repo, pin a commit snapshot, search it |
| `repo-read_file` | Read a file from a pinned remote repo snapshot |
| `git.fetch_ref` | Fetch a branch and return an immutable ref |
| `git.search_ref` | Search code at a specific git ref |
| `git.read_file_ref` | Read a file at a specific git ref |
| `git.status` | Working tree and branch status |
| `git.log` | Recent commit history |
| `git.show` | Commit diff or file at a revision |
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
| `code.symbols` | Find Go/C# symbols via language server |
| `code.definition` | Go to symbol definition |
| `code.references` | Find symbol references |
| `code.diagnostics` | LSP diagnostics for a file |

`repo-search` and `repo-read_file` resolve branches to immutable snapshots, so
concurrent users can inspect different refs without checkout conflicts.

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
| `k8s-get_pods` | List Kubernetes pods |
| `k8s-describe` | Describe Kubernetes resources and events |
| `k8s-logs` | Fetch Kubernetes pod logs |
| `k8s-top` | Show Kubernetes CPU and memory usage |
| `readonly-shell` | Run allowlisted read-only CLI commands |
| `diagnostics-incident_brief` | Structured incident summary |
| `diagnostics-timeline` | Incident event timeline |
| `diagnostics-evidence_board` | Structured evidence board |

## Knowledge and Search

| Tool | Description |
|---|---|
| `web-search` | Public web search through DuckDuckGo, Brave, SearXNG, Google CSE, or SerpAPI |
| `web-read_page` | Fetch and read a public web page |
| `notion.search` | Search Notion pages |
| `notion.create_page` | Create a Notion page |
| `youtrack.get_issue` | Fetch a YouTrack issue |
| `youtrack.search` | Search YouTrack issues |
| `knowledge.runbook_search` | Search local runbooks under `PROMPT_DIR/runbooks/` |

Final Slack answers append a concise evidence section when the turn used
`web-search` or `web-read_page`.

## Slack

| Tool | Description |
|---|---|
| `slack.ask_user` | Ask for missing information and pause the run |
| `slack.file_search` | Search a large uploaded Slack file |
| `slack.json_analyze` | Structurally analyze uploaded JSON |

## Agent Control

| Tool | Description |
|---|---|
| `skills-load` | Load full instructions for a named skill |
| `delegate.run` | Run a focused sub-agent for bounded analysis |

## Browser Automation

Playwright MCP tools are enabled only when `PLAYWRIGHT_MCP_URL` is set.

Available tools include `pw-navigate`, `pw-snapshot`, `pw-click`, `pw-type`,
`pw-fill_form`, `pw-screenshot`, `pw-press_key`, `pw-wait`, `pw-evaluate`,
`pw-get_all_pages`, and `pw-switch_page`.

Use element `ref` values from `pw-snapshot` with actions. Browser state is scoped
to a single agent turn; each new Slack message starts a fresh session.

## Luckin Coffee

Luckin order management uses the official MCP endpoint and requires
`LUCKIN_MCP_TOKEN` from <https://open.lkcoffee.com/mcp>. Order creation and
cancellation require explicit confirmation.
