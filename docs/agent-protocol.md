# Agent Core Protocol

`packages/agentcore` is the transport-neutral execution engine. Slack and the
stdio app server are adapters; neither owns the model/tool loop.

The current protocol version is `1.0`. Versions use `major.minor` semantics:

- a major change may break consumers;
- a minor change may add fields or event types;
- consumers must ignore unknown fields and unknown event types within a known
  major version.

Every event contains `version`, `id`, `type`, `thread_id`, `at`, and, when
applicable, `turn_id`, `status`, `item`, or `error`. Ordered transports assign a
monotonic `sequence` per thread.

Lifecycle events:

```text
thread.started
turn.started
  item.started
  item.delta
  item.completed | item.failed | item.canceled
turn.completed | turn.failed | turn.canceled
thread.closed
error
```

`agentprotocol.Broker` retains bounded per-thread history and supports replay
after a sequence cursor. Slow subscribers never block agent execution; they
reconnect and replay from their last sequence.

## JSON-RPC app server

`slack-copilot-app-server` reads JSON-RPC 2.0 objects from stdin and writes one
response or notification per line to stdout.

```json
{"jsonrpc":"2.0","id":1,"method":"initialize"}
```

```json
{"jsonrpc":"2.0","id":2,"method":"turn/start","params":{"threadId":"thread-1","question":"Summarize the incident","messages":[{"role":"user","content":"Summarize the incident"}]}}
```

The immediate response contains the generated `turnId`. Execution events are
subsequent JSON-RPC notifications with method `event`.

```json
{"jsonrpc":"2.0","id":3,"method":"turn/cancel","params":{"turnId":"turn-..."}}
```

Queue additional user guidance for an active turn with `turn/steer` and
`{"turnId":"turn-...","text":"Focus on the database path"}`. The core
injects queued guidance before the next model step.

Close a thread with `thread/close` and `{"threadId":"thread-1"}`. Closing
ends the transport lifecycle; persisted runs remain in PostgreSQL.

The stdio transport is intentionally small. Authentication, public networking,
and deployment topology belong outside this process.
