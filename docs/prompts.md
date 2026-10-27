# Prompts

Prompt text loads embedded public defaults and the embedded private catalog,
then an optional deployment overlay selected by `PROMPT_DIR`. Unset `PROMPT_DIR`
adds no filesystem overlay. An explicit relative path is relative to the process
working directory; absolute paths select the actual directory. Loading never
searches parent directories or infers embedded catalogs from path suffixes.

A configured directory must exist. Individual sections may be absent, but
present files must be readable and valid. Invalid JSON or skill metadata rejects
the complete load without replacing the active catalog; worker bootstrap reports
the error before opening stores or starting background work.

Catalog skills, local skill discovery, and uploaded user skills share metadata
parsing through `prompts.ParseSkill`. Plain Markdown may use the directory
or upload filename as its name. Malformed YAML is rejected, not repaired or
silently interpreted as missing metadata.

Keep the main assistant behavior in git so remote branches and local deployments
stay aligned. Use the private overlay only for narrow sensitive details such as
company-specific repository names, workflow aliases, private runbook references,
or a short local identity addendum.

## Design Principles

Prompt space is layered by stability:

- `system.md` is the small kernel: identity, confidentiality, evidence,
  autonomy, communication, and safety. It should not contain project details,
  one-off incidents, long workflows, or tool-specific recipes.
- `rules/*.md` contains reusable operating policy such as investigation style,
  evidence discipline, safety boundaries, and communication norms.
- `tools.json` owns tool-specific routing and parameter guidance.
- `health.json`, `tool_statuses.json`, and prompt-specific files own specialized
  utility behavior instead of bloating the main agent prompt.
- Private overlays should contain only deployment-specific context such as
  repository names, environment mappings, and workflow aliases.

When adding prompt text, prefer the lowest layer that can carry the instruction
reliably. A useful rule of thumb: if an instruction names a company repo,
workflow, tenant, branch pattern, incident, or tool implementation detail, it
does not belong in `system.md`.

## Committed Prompt Catalog

| File | Purpose |
|---|---|
| `system.md` | Main system prompt |
| `app_messages.json` | Responses to empty mentions, empty DMs, file-only DMs |
| `tools.json` | Tool description and parameter overrides |
| `health.json` | Health summary header and rules text |
| `tool_statuses.json` | Slack status messages shown while tools run |
| `texts.json` | Shared prompt snippets and context wrappers |
| `rules/*.md` | Markdown rules injected into the main agent |
| `skills/<name>/SKILL.md` | Skill definitions with frontmatter |
| `runbooks/*.md` | Service runbooks searched by `knowledge-runbook_search` |

Only skill metadata appears in the base prompt. Full skill instructions are
loaded on demand through `skills-load`.

Skill instructions describe task-specific decisions and invariants. Keep wire
examples, copied schemas, credentials, and generic syntax tutorials out of them.
Registered tools own parameters and authentication; a skill must not introduce
an alternative shell/HTTP path around the active tool policy. Skill descriptions
should describe user intent rather than lists of trigger words.

The committed catalog includes systematic debugging, incident diagnostics,
test-driven development, implementation planning, and verification before
completion. Four of these workflows are adapted for this runtime from the
Superpowers 6.3.0 plugin curated in OpenAI's `openai/plugins` repository.
Superpowers is authored by Jesse Vincent and licensed under MIT; it is curated
plugin content rather than an OpenAI-authored system skill. The adaptation keeps
the core methods while removing dependencies on Superpowers-specific skill
names, worktree conventions, and orchestration. See
`skills/SUPERPOWERS-LICENSE.txt` for the upstream license.

Keep general skills transport-neutral and evidence-driven. Product-specific
workflows belong in their owning adapter or workflow package.

Runtime facts such as the current date, timezone, and configured workspace
roots are injected as an ephemeral `<environment_context>` user fragment at
request time. Repository inventories are no longer embedded in the system
prompt; use the deferred `workspace-list_repos` tool when the model needs a
repository catalog.

Assistant replies use standard Markdown across Slack, Web, and CLI. Bold is
`**text**`, and links are `[label](url)`. Slack's native `markdown` blocks and
`markdown_text` stream chunks consume this same format. Legacy Slack `mrkdwn`
(`*text*` for bold and `<url|label>` links) is reserved for adapter-owned UI blocks;
asking the model to use it makes intended bold text render as italics. If native
Markdown blocks are unavailable, the Slack client falls back to literal text
with legacy markup parsing disabled.

## Private Overlay

A minimal `worker/.prompts/` setup:

```text
worker/.prompts/
  agent.md
  tools.json
  runtime.json
  rules/
  runbooks/
  skills/
```

`agent.md` should stay short. The main behavior belongs in
`packages/prompts/defaults/system.md`:

```markdown
Identity:
- Your name is <ASSISTANT_NAME>.
- You serve the <COMPANY_NAME> engineering team.

Deployment-specific CI/CD:
- The default GitHub workflow repository is `<ORG>/<REPO>`.
- For service deployments, use the `deploy` workflow alias.
```

`runtime.json` is useful for workflow aliases:

```json
{
  "github_workflows": {
    "deploy": "cicd-deploy.yml",
    "rollback": "cicd-rollback.yml"
  }
}
```

`tools.json` can override specific tool descriptions:

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

Private files are merged on top of public defaults at startup. See
`packages/prompts/catalog.go` for merge semantics.

## Change and verify

Check which profile loads a prompt layer before editing it; Web and Slack do
not necessarily load identical rules/skills. Validate representative user
requests, tool selection, and failure behavior. Prompt text cannot grant a
capability that policy denies. See [development](development.md).
