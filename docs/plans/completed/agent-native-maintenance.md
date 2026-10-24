# Agent-native repository maintenance

Status: completed. Scope: source repository maintenance, not a runtime rewrite.

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
- [x] Review the final diff, run broad checks, record outcomes and limitations.

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
local repository checks do not claim live Slack/LLM or database verification.
The hosted integration scopes were subsequently verified in CI as recorded below.

CI's first run caught a nonportable sibling-checkout link in observability docs:
it passed locally because the deployment checkout existed. The link now uses
its canonical repository URL. The checker rejects repository-escaping local
links even when their target exists; a negative fixture preserves this behavior.
Structural evidence now runs before contract checks so failures keep the report.
The artifact upload explicitly includes only its hidden `.cache` report path
and fails if expected evidence is absent.

## Completion and handoff

All acceptance checks passed. CI run
[104](https://github.com/noknov/kepler-agent/actions/runs/40091251359) passed on
candidate `308a507894a0ef9148fd934556d4ed3be3d0bc70`, including the new repository
contracts/report artifact, existing PostgreSQL/Redis/Linux sandbox tests,
Slack regressions, CLI protocol/bundle, Web interactions, evaluator checks, and
race suites. No review findings remained at completion.

The source/runtime and dependency manifests are unchanged. The repository now
has one tested Go architecture checker and one stdlib Python maintenance entry
point. Follow-up opportunities are bounded in the maintenance backlog; no
service provisioning, recurring agent, automatic merge bot, or deployment was
introduced. The final documentation commit records this verified result.
