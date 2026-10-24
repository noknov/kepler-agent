# Execution plans

Use a plan for cross-boundary/persistence changes and work spanning sessions.
Use a task/PR acceptance statement for small changes. Plans are versioned work
records, not a second ticket system. Active plans live in `active/`; completed
plans live in `completed/`. Read the relevant plan before overlapping work.

Use these sections so the next agent can continue without a chat transcript:

```markdown
# Concrete intended outcome

Status: active.

## Outcome and acceptance
Observable behavior, scope/non-goals, and executable acceptance commands.

## Work
A short checklist of independently verifiable steps.

## Decisions
Existing alternatives considered, chosen owner/abstraction, and consequences.

## Progress and evidence
What passed/failed, at which revision or milestone, and remaining uncertainty.

## Handoff
Next concrete action, blockers, and state that must survive a new session.
```

Update progress at meaningful boundaries, not after every command. Do not mark
an item complete without its evidence. Completed records can refer to code from
their original revision; they are not current architecture guidance. Their
local links are therefore excluded from the live documentation link check.

To find work, inspect GitHub issues, [maintenance backlog](../maintenance-backlog.md),
and `git log` alongside these directories. Do not invent backlog priorities or
start external writes simply because a plan exists.
