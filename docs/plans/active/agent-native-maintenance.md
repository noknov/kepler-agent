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

CI's first run caught a nonportable sibling-checkout link in observability docs:
it passed locally because the deployment checkout existed. The link now uses
its canonical repository URL. The checker rejects repository-escaping local
links even when their target exists; a negative fixture preserves this behavior.
Structural evidence now runs before contract checks so failures keep the report.
The artifact upload explicitly includes only its hidden `.cache` report path
and fails if expected evidence is absent.

## Handoff

Next: inspect CI and review results for PR #22, then record completion evidence.
The staged report was reviewed: product/generated code and dependency manifests
are unchanged; the new package is the Go architecture checker. `make test-race`
and checker race tests passed. No runtime schema or product behavior changes.
The working branch is `feat/agent-native-maintenance`.
