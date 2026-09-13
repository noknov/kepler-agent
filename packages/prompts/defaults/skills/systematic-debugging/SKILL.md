---
name: systematic-debugging
description: Use when diagnosing a bug, test failure, performance regression, flaky behavior, or integration failure before proposing a fix.
---

# Systematic debugging

Find the cause before changing the code. Treat error messages, traces, tests,
logs, configuration, and recent changes as evidence rather than jumping from a
symptom to a plausible patch.

## 1. Establish the failure

- State the expected and observed behavior.
- Reproduce the problem with the smallest reliable command or request.
- Read the complete error and stack trace. Record the relevant paths, lines,
  status codes, correlation IDs, and timestamps.
- Check recent code, dependency, configuration, and environment changes.
- If reproduction is intermittent, gather more evidence instead of guessing.

## 2. Locate the failing boundary

For a multi-component path, inspect what enters and leaves each boundary. Check
that state, configuration, identity, and errors propagate as expected. Add
temporary, non-sensitive instrumentation when existing evidence is insufficient.

Trace invalid data backward through callers until its origin is known. Prefer
fixing the producer or violated contract over adding a guard only where the
failure finally appears.

## 3. Compare patterns

- Find a similar working path in the same repository.
- Read the relevant implementation and tests completely.
- List material differences between the working and failing paths.
- Identify assumptions about lifecycle, concurrency, persistence, permissions,
  versions, and external services.

## 4. Test one hypothesis

State one falsifiable hypothesis: "X causes the failure because Y." Test it with
the smallest safe observation or change. Change one variable at a time.

If the hypothesis fails, discard it and return to the evidence. Do not stack
unverified fixes. After several failed fixes that expose new coupling or shared
state, pause and reassess the architecture with the user.

## 5. Implement and verify

- Add the smallest regression test or reproducible check that fails for the
  identified cause.
- Make one focused fix.
- Run the targeted check, then the relevant wider suite.
- Remove temporary instrumentation unless it is useful, safe observability.
- Report the root cause, evidence, fix, and verification separately.

When the cause is external or timing-dependent, document what was ruled out and
add bounded handling such as a deadline, condition-based wait, retry policy, or
clear error. Never retry an external write whose outcome is uncertain without
an idempotency strategy.
