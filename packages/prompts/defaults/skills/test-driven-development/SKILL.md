---
name: test-driven-development
description: Use when implementing a feature, bug fix, refactor, or behavior change that can be specified with executable tests.
---

# Test-driven development

Use a short red-green-refactor loop. The test should describe an observable
contract and fail for the expected reason before the implementation changes.

## Red

Write one minimal test for one behavior. Before writing the body, name the
production defect that the test would catch. Derive expected values independently
with literals or hand-checked fixtures; do not reproduce the implementation's
logic in the assertion.

Run the narrowest test command and inspect the failure:

- A passing test may be exercising existing behavior or the wrong path.
- A setup error is not a useful red state; correct it until the assertion fails
  for the missing or broken behavior.
- Prefer real components. Mock only slow, nondeterministic, destructive, or
  external boundaries, and preserve the side effects the test relies on.

## Green

Implement the smallest coherent change that satisfies the contract. Avoid
unrequested options, unrelated refactors, and speculative abstractions. Run the
targeted test, then nearby tests that cover the changed boundary.

## Refactor

Only after the tests are green, improve names, structure, duplication, or test
helpers without adding behavior. Keep the suite green throughout.

## Test quality gates

- Test public behavior and boundary contracts, not private structure or exact
  source text.
- Assert outcomes and side effects, not merely that a mock was called.
- Make doubles specific enough that the wrong branch cannot satisfy the test.
- Keep test-only cleanup and helpers out of production APIs.
- Cover realistic boundaries: empty values, malformed input, authorization,
  retries, concurrency, cancellation, and persistence failures as applicable.
- Mentally mutate the implementation; each realistic wrong branch or missing
  side effect should break at least one test.

Generated code, pure documentation, experiments, and emergency containment may
need a different verification strategy. State the exception and use the closest
repeatable check rather than manufacturing a low-value test.
