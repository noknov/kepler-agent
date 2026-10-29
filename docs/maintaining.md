# Maintaining Kepler with agents

This is the development contract, not an additional agent runtime. Humans define
product intent and operating constraints; agents may investigate, implement,
review, and verify within the authorized task. Repository checks make some
invariants enforceable. They do not prove a change is useful or grant deployment
permission. An agent allowed to edit CI can also edit these checks; repository
permissions and required status checks remain hosting policy, not a guarantee
provided by this document.

## Choose the right change

Kepler shares a canonical loop across hosted Slack/Web and local CLI. Preserve
correct identity, explicit authorization, durable recovery, bounded resource
use, and usable product feedback before adding orchestration features. Product
boundaries are owned by [architecture](architecture.md); detailed mechanisms
are in [runtime](runtime.md), [safety](safety.md), and [evaluation](../evals/README.md).
A coding benchmark or line-count reduction alone does not establish an improvement.

For each task, record the observable problem, intended behavior, and a check
that can falsify the result. Reproduce a defect before changing the mechanism
when feasible. A timeout, fallback, or prompt rule is not a substitute for fixing
an ownership or state-transition defect.

## Navigate before changing

Read Git status/log, the root and applicable scoped AGENTS.md files, the owning
guide, and the [issue/PR execution record](plans/README.md). Trace one real
request through the current implementation and tests. Treat historical audits and completed records
as dated evidence, not specifications for current code. Update an owning guide
instead of copying its facts into several new documents.

`make doctor` explains missing toolchains and optional integration fixtures.
`make repo-check` validates Go boundaries, literal runtime DDL, documentation
file destinations, and the checker regression tests. It needs no credentials
or running services. Use `PYTHON=/path/to/python3.11` if the host's `python3` is
older than the evaluator's minimum. Normal Go commands may resolve the declared
toolchain and populate caches; these targets provide no custom installers or
service provisioning.

## Control expansion at the design boundary

Before adding a package, dependency, interface, wrapper, cache, queue, or durable
state, answer:

1. Which existing owner was considered, and why cannot it own this behavior?
2. Which policy, lifetime, consistency, or substitution boundary does the new
   abstraction own? Where does it have independent behavior to test?
3. Which repeated logic, old mechanism, or coupling does it replace or remove?
4. What failure case and observable result justify its long-term maintenance?

A small delegation layer is appropriate when it centralizes a real contract,
such as atomic storage or delivery ownership. A forwarding interface with no
separate policy/lifetime is usually extra navigation. Extract based on cohesive
responsibility, not to meet a file-size target. Prefer an existing dependency
and the standard library when they provide the required contract. Delete the
superseded path in the same change when compatibility permits; otherwise record
its removal trigger and acceptance in the [backlog](maintenance-backlog.md).

Do not create speculative framework hooks, parallel implementations, global
registries, or exception lists for one task. Keep a new mechanism as local as
its actual responsibility. When the architecture itself must evolve, document
the alternatives, chosen boundary, consequences, and verification in the plan
or owning design guide, then update the executable rule and its rejection tests.

## Make progress recoverable

Small changes need an acceptance statement and validation in their PR. A change
crossing architectural or persistence boundaries needs an
[execution record](plans/README.md) in its GitHub issue/PR for alternatives,
rollout order, acceptance criteria, progress and handoff. Update the owning
guide for supported contracts and durable design decisions.

Keep task progress reports, retrospectives, and capability comparisons in issue
or PR discussions. Committed guides describe current supported behavior, stable
design decisions, and operating contracts.
Use English for commit messages and maintenance/development documentation.

A discovered problem outside scope becomes a concrete backlog item with an
owning area and acceptance check. Avoid an unbounded "cleanup" project or a
second task database beside GitHub issues. Retire backlog items when evidence
shows they are fixed, superseded, or intentionally declined.

## Review with evidence

Stage new files and run `make change-report BASE=<base-ref>`. Its JSON exposes
product/test/documentation/maintenance/generated diff sizes, new Go packages,
changed dependency manifests, and untracked files excluded from line totals.
The baseline must resolve to a real commit. CI compares exact PR base/head
revisions and uploads the report. It is descriptive: no arbitrary LOC quota,
complexity score, or refreshed baseline can excuse a weak design. In particular,
include the maintenance machinery itself in the cost of the change.

Review the final diff independently of the implementation narrative:

- Does it solve the acceptance case at the correct owner and durable boundary?
- Is there a simpler existing path, and was the superseded path removed?
- Do actor, policy, retry, cancellation, and persistence contracts survive the
  relevant failure windows? Do wrappers preserve those contracts?
- Are new dependencies and abstractions justified by the four questions above?
- Do tests observe behavior or outcomes rather than mirror private structure?
- Are documentation, generated artifacts, migrations, and rollout order aligned?

Use the [PR template](../.github/pull_request_template.md). Review can be done
by another agent when separately authorized, or as a fresh pass over the final
diff. This repository does not require a human approval ceremony for routine
reversible work. Merge/deploy actions still follow the user's authorization
and actual repository protections.

## Verify and finish

Run the smallest relevant tests while iterating. Broad changes require
`make check`; concurrency changes require affected race suites. See
[verification scope](development.md#verification) for frontend and integration
commands. `make integration-check` requires explicitly configured isolated
PostgreSQL and Redis; it does not provision them or test live Slack/LLM behavior.
Preserve failure output, fix the cause, and rerun the affected scope. Do not
remove assertions or weaken an architecture check to make an implementation fit.

Record exact commands and scope in the PR, including skips and environmental
limits. A successful mock, build, evaluator dry-run, or environment-variable
check is not a production reliability measurement. Leave a clean, reviewable
Git state or an explicit handoff. When a review finds a recurring failure mode,
encode the invariant in the narrowest relevant checker/test, then update the
owning guide; do not continuously grow the root instruction file.

## Sources and adaptation

Reviewed 2026-10-24. These sources motivate the workflow; the concrete contracts
and checks above are Kepler-specific engineering decisions.

- [OpenAI: Harness engineering](https://openai.com/index/harness-engineering/):
  a navigable knowledge base, executable boundaries, plans, and entropy control.
- [OpenAI: Iterating development workflows with Codex](https://developers.openai.com/cookbook/examples/codex/iterating-development-workflows-with-codex):
  repository-local instructions and reproducible development feedback.
- [Anthropic: Effective harnesses for long-running agents](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents):
  incremental work, external progress artifacts, and explicit verification.
- [Claude Code best practices](https://code.claude.com/docs/en/best-practices)
  and [project memory](https://code.claude.com/docs/en/memory): concise shared
  guidance and checks an agent can run.

We do not copy their product architecture, require a particular coding model,
install an autonomous merge bot, or equate "AI-native" with more agent code.
