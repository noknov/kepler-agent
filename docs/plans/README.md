# Execution records

Keep task plans, progress, comparisons, retrospectives and handoff evidence in
the task's GitHub issue or PR. These records explain a specific change at a
specific revision; they are not maintained product documentation.

For work spanning sessions or architectural/persistence boundaries, record:

- The observable problem, intended behavior and executable acceptance checks.
- The existing owner, alternatives considered and the chosen boundary.
- Rollout order and any external prerequisites.
- Evidence at meaningful revisions, unresolved concerns and the next action.

Link the issue/PR in the change description so another agent can resume from
Git history and the discussion. Do not require private chat history, create a
second ticket system, or commit task-specific reports to the main branch.

Committed guides describe current supported behavior, stable design decisions
and operating contracts. Update the owning guide when those contracts change;
keep its local links valid. Historical execution evidence stays in GitHub
discussions and the corresponding Git revisions.

To find work, inspect GitHub issues, the [maintenance backlog](../maintenance-backlog.md)
and `git log`. An existing execution record does not grant authorization for
external writes or deployment.
