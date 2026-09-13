---
name: verification-before-completion
description: Use before claiming that work is complete, fixed, tested, or ready to commit, especially after code, configuration, or documentation changes.
---

# Verification before completion

Base completion claims on fresh evidence. A plausible diff, a successful
partial check, or another agent's report is not enough to establish that the
requested outcome is complete.

## Define the proof

Before reporting success, identify what evidence would demonstrate each claim:

- A bug fix needs a reproduction or regression test covering the original
  failure.
- A behavior change needs targeted tests for the affected contract.
- A build claim needs the relevant build command to exit successfully.
- A quality claim needs the configured formatter, linter, or static analysis.
- A documentation or UI change needs inspection of the rendered or consumed
  result when layout or navigation matters.
- Requirement completion needs a check against the user's actual request, not
  only a green test suite.

Choose checks proportional to risk, but do not silently substitute a narrower
check for the one that proves the claim.

## Run fresh verification

1. Run the targeted check that exercises the changed behavior.
2. Run the relevant broader suite, build, or validation command.
3. Read the complete result, including exit status, skipped tests, warnings,
   and reported failures.
4. Inspect the final diff or artifact for unintended changes and unresolved
   placeholders.
5. For delegated work, verify the shared filesystem or external state yourself;
   treat the worker report as a lead, not proof.

When a check cannot run because of environment limits, distinguish that from a
product failure. Record the exact limitation and run the strongest safe
alternative without describing the unavailable check as passing.

## Report the actual state

- If verification succeeds, name the checks that support the completion claim.
- If it fails, report the failure and remaining work directly.
- If verification is partial, say what was verified and what remains unknown.
- Do not use "should pass", "probably fixed", or equivalent language as a
  substitute for evidence.

Perform this gate before committing, opening a pull request, handing work off,
or telling the user that the task is done.
