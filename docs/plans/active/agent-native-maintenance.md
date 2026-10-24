# Agent-native repository maintenance

Status: active. Scope: source repository maintenance, not a runtime rewrite.

## Outcome and acceptance

An agent starting without this chat can find the correct owning package, run
checks, understand failed checks, and hand off unfinished work. Architecture
violations fail mechanically. Reviewers can inspect growth and new dependencies
without treating a line-count score as product quality. Existing product,
authorization, protocol, and deployment contracts remain authoritative.

Acceptance commands: `make doctor`, `make repo-check`, `make check`,
`make test-race`, and `make change-report BASE=<base-ref>`. CI must execute the
repository checks and publish the change report. A missing integration fixture
must remain visible rather than be reported as tested behavior.

## Work

- [x] Replace regex Go import scanning with tested Go AST rules, including core
  dependency direction, entrypoint isolation, and runtime DDL prevention.
- [x] Add environment diagnostics, navigable documentation validation, and
  reproducible structural change reports.
- [x] Introduce concise shared/scoped agent guidance, plans, design decisions,
  and a concrete maintenance backlog with acceptance criteria.
- [x] Wire the checks and reports into Make and CI; verify rejection paths.
- [ ] Review the final diff, run broad checks, record outcomes and limitations.

## Decisions

- Reuse Go/Python standard libraries and existing Make/CI. Do not add an agent
  orchestration service, another package manager, or a parallel test framework.
- Keep AGENTS.md a map; Claude uses the same instructions through an import.
- Enforce structural invariants. Report growth without a fixed LOC quota that
  rewards compressed code or arbitrary file splitting.
- Preserve meaningful local implementation freedom; architecture changes need
  an explicit decision and updated rule/tests, not a new silent exemption.

## Progress and evidence

Initial inventory: 291 production Go files, 53,367 physical Go lines. This is
scope information, not a quality grade. Existing `scripts/check_boundaries.py`
uses regex prefix matches and has no tests. Full product checks already exist;
this change composes with them rather than replacing them.

Local verification: `make repo-check`, `make doctor`, and `make check` passed
with Python 3.11.16 and Go 1.25.0. Negative fixtures exercise wrong dependency
direction, prefix collisions, aliased/commented imports, Windows-only sources,
static DDL, broken documentation destinations, invalid Git baselines, and
staged/untracked change accounting. Integration stores are absent locally;
repository checks do not claim live Slack/LLM or database verification.

## Handoff

Next: review the staged change report and run CI on the submitted candidate.
All local core checks pass. No runtime schema or production behavior changes.
The working branch is `feat/agent-native-maintenance`.
