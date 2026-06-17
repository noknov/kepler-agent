# Private Prompt Overlay Example

This directory is a non-hidden example for sensitive or deployment-specific prompt configuration. It is not loaded by default.

To use it, copy the files you need into your private `PROMPT_DIR` directory, usually `.prompts/`, then replace placeholder names with your real company, repositories, workflows, and operating rules.

Prefer the compact prompt layout for new local configuration:

- `agent.md` for a small local system prompt addendum. Keep the main behavior prompt in git.
- `tools.json` for tool description and parameter overrides.
- `runtime.json` for app messages, status text, retry prompts, memory labels, health text, shared snippets, and workflow aliases.
- `rules/*.md`, `skills/*/SKILL.md`, and `runbooks/*.md` only when you need separate local policy, skills, or operational knowledge.

Keep these categories in private prompt overlays:

- Company identity and internal assistant persona.
- Internal engineering, on-call, release, escalation, or approval workflows.
- Service names, repository names, workflow aliases, runbook content, and private skill instructions.
- Any text that reveals internal process, customer data handling, or codebase-specific assumptions.
