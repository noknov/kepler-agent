# Execution and recovery walkthrough

Read [architecture](architecture.md) for the owners and
[runtime](runtime.md) for context, tool and accounting details.

## From Slack acceptance to a result

1. Gateway verifies the Slack signature and stores the event once. A duplicate
   event ID does not create a second input.
2. The handler authenticates the actor and conversation. Shared-channel input
   requires an explicit mention; DMs do not. Admission persists the request in
   the session input queue before the event inbox is acknowledged.
3. A bounded dispatcher claims input. Intake and Stop remain serviceable while
   execution slots are full. Same-user steering reaches the active turn;
   another user's input remains that user's later turn.
4. Runtime session ownership serializes canonical execution. Short input leases
   renew while work runs. Losing ownership cancels the former executor.
5. The runtime records model/tool decisions and completed results. Approval
   binds the exact actor, call, descriptor and arguments. Unknown external
   effects require reconciliation; they are not silently repeated.
6. Slack records delivery intent before creating a message and keeps the known
   message ID before final acknowledgement. Recovery repairs that message. An
   interrupted create without a receipt is uncertain and cannot be reposted
   automatically.
7. Native session status is a projection. Its recovery takes session ownership
   and checks the latest turn so an older completion cannot clear a new run.

A successful model call is not a successful delivery. A run projection or trace
is not authorization and is not the delivery source of truth.

## Restart and backpressure

Event retries use persisted due times and a single deadline-driven poller.
Periodic polling remains a recovery path when notification delivery fails.
Execution failures keep retry deadlines and become dead-lettered after their
attempt budget; failure is never acknowledged as successful completion.

Dispatch is bounded and wake-ups are hints. Input remains durable when the local
channel is full. Draining stops new executions, waits for owned work and then
cancels service lifetimes with bounded canonical/delivery cleanup.

## Context, memory and task state

Context projection budgets complete messages, tool schemas and output reserve.
Historical evidence can be masked and read back through session-scoped readers.
Compaction checkpoints preserve continuation evidence but do not erase raw
transcript history. Task-state archival is explicit and revision-checked. Active
constraints and unresolved external effects cannot be discarded to make room.

Cross-conversation memory uses authenticated team/user scope. Reset generations
fence old writers. Semantic retrieval is opt-in and uses independently configured
embedding credentials; lexical fallback is observable. Retrieved memory is task
data, never authority to change tool policy.

## Benchmark and generic delegation

The benchmark adapter uses the same runtime with local tools and an OS sandbox.
Its single-shot JSONL boundary allows Harbor/AppWorld to observe completion,
usage and termination without a terminal UI or another execution protocol.

Read-only delegated tasks inherit the parent's deadline, budget and actor. They
have independent transcripts and evidence manifests. They are leaf tasks;
their reports are claims to verify, not permission to execute writes. They do
not form an automatic PR-review workflow or durable background task system.
