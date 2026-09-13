---
name: writing-plans
description: Use when turning a specification or multi-step engineering request into an implementation plan before changing code.
---

# Writing implementation plans

Create a plan that another engineer or agent can execute without rediscovering
the system. Ground it in the current repository, its tests, and the stated
requirements.

## Understand the change

1. Inspect the entry points, owning packages, interfaces, state boundaries, and
   existing tests.
2. State the user-visible goal, invariants, constraints, and explicit non-goals.
3. Resolve ambiguity that would materially change architecture, compatibility,
   security, data, or rollout.
4. Prefer existing patterns and the smallest design that satisfies the contract.

## Define the file map

List the files to create, modify, test, or remove and explain each responsibility.
Keep ownership boundaries clear. Do not use a plan as an excuse for unrelated
restructuring.

## Break work into verifiable tasks

Each task should produce a coherent, independently testable result and include:

- the behavior or contract it establishes;
- exact files and important interfaces involved;
- prerequisites from earlier tasks;
- implementation steps in dependency order;
- the targeted checks and expected observable result;
- documentation, compatibility, migration, or rollback work when applicable.

Use concrete names, commands, signatures, and examples when they are known from
the repository. Do not invent exact details that have not been verified, and do
not hide missing analysis behind "TBD", "handle errors", or "add tests".

## Review the plan

Before presenting it:

- map every requirement to a task;
- check that interface names and data shapes are consistent across tasks;
- confirm tests cover success, failure, and affected boundaries;
- identify destructive or externally visible steps;
- separate verified current behavior from recommendations;
- call out the few remaining decisions that truly require user input.

Keep the plan proportional. A small local change may need only a few precise
steps; a cross-system migration needs explicit sequencing and rollback.
