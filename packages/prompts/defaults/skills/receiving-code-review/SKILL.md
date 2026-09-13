---
name: receiving-code-review
description: Use when evaluating code review feedback before implementing it, especially when comments are ambiguous, conflicting, or technically uncertain.
---

# Receiving code review

Treat review comments as technical claims to understand and verify, not commands
to apply blindly.

## Process the review

1. Read the complete review and group related comments.
2. Restate each requested behavior in concrete terms. Ask when the requirement,
   scope, or acceptance condition is unclear.
3. Verify the claim against the current code, tests, supported platforms,
   compatibility constraints, and prior architectural decisions.
4. Decide whether to accept, adapt, defer, or challenge the suggestion, and give
   evidence for that decision.
5. Implement accepted items in a safe dependency order and verify each coherent
   change before moving on.

Prioritize correctness and safety issues, then simple fixes, then larger design
changes. Do not mix optional cleanup into a blocking fix.

## Push back when necessary

Challenge feedback when it would break supported behavior, duplicates unused
functionality, conflicts with a verified constraint, assumes the wrong runtime,
or expands scope without a user need. Reference code and test evidence, and ask
a specific question when an architectural decision remains.

If verification requires unavailable systems or context, say what cannot be
verified and what evidence would resolve it. Do not present uncertainty as fact.

## Report outcomes

For accepted feedback, state what changed and how it was checked. For rejected
or deferred feedback, explain the technical reason and any remaining risk. If
new evidence disproves an earlier conclusion, correct it directly and continue.
