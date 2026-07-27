# Prompts

Prompt text is loaded in two layers:

1. `prompts/` contains committed, generic defaults that are safe to maintain in
   git.
2. `PROMPT_DIR`, default `.prompts/`, adds small deployment-specific overlays
   that should stay gitignored.

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
- `delegates.json`, `runner.json`, `health.json`, and prompt-specific files own
  specialized utility behavior instead of bloating the main agent prompt.
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
| `delegates.json` | System prompts for delegate sub-agents |
| `app_messages.json` | Responses to empty mentions, empty DMs, file-only DMs |
| `tools.json` | Tool description and parameter overrides |
| `memory.json` | Labels for summary and thread context blocks |
| `runner.json` | Retry prompt templates for final-answer validation |
| `health.json` | Health summary header and rules text |
| `tool_statuses.json` | Slack status messages shown while tools run |
| `texts.json` | Shared prompt snippets and context wrappers |
| `rules/*.md` | Markdown rules injected into the main agent and delegates |
| `skills/<name>/SKILL.md` | Skill definitions with frontmatter |
| `runbooks/*.md` | Service runbooks searched by `knowledge-runbook_search` |

Only skill metadata appears in the base prompt. Full skill instructions are
loaded on demand through `skills-load`.

Repository inventory is not injected into the system prompt by default because
repository names can be sensitive. Enable it only when sending local repository
names to the model provider is acceptable:

```bash
PROMPT_INCLUDE_REPO_INVENTORY=true
```

## Private Overlay

A minimal `.prompts/` setup:

```text
.prompts/
  agent.md
  tools.json
  runtime.json
  rules/
  runbooks/
  skills/
```

`agent.md` should stay short. The main behavior belongs in `prompts/system.md`:

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
`internal/prompts/catalog.go` for merge semantics.
