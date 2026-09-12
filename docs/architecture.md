# Architecture

Kepler shares one agent loop across products. A **surface** receives input and
renders output; a **profile** selects storage, tools, credentials, and policy;
the **runtime** executes turns using canonical model and transcript contracts.

```text
Slack -> gateway -> durable inbox -> worker -> hosted profile --+
Web   -> gateway -> worker Web handler -----> hosted profile --+--> runtime
CLI   -> Go headless runner ----------------> local profile ---+     |
Ink   -> stdio app-server ------------------> local profile ---+     +--> providers
                                                                   +--> tools
                                                                   +--> transcript
```

Sharing the runtime does not make the surfaces feature-equivalent. Web has its
own conversations and catalog; Slack installs product routing and delivery;
local execution has a different filesystem and approval boundary.

## Ownership

| Layer | Responsibility | Source entry |
| --- | --- | --- |
| Runtime | Context projection, model/tool scheduling, termination, canonical events | [runtime](../packages/agent/runtime) |
| Model adapters | Translate provider requests and events | [providers](../packages/providers) |
| Profiles | Compose policy, storage, catalogs, resilient clients | [profiles](../packages/profiles) |
| Surfaces | Authentication, ingress, user-visible state and delivery | [surfaces](../packages/surfaces) |
| App-server | Local client protocol and admission | [appserver](../packages/appserver) |
| Delegation | Bounded child tasks with separate transcripts and parent links | [delegation](../packages/agent/delegation) |

Do not import Slack or provider wire assumptions into `packages/agent`.
[AGENTS.md](../AGENTS.md) records the repository's implementation constraints.

## State and identity

| Term | Meaning |
| --- | --- |
| Session/thread | Conversation identity used to load history and route input |
| Turn | One execution lifecycle, including model and tool steps |
| Transcript | Append-only canonical completed messages and lifecycle events |
| Run/step | Query-oriented observability projection, not a second conversation store |
| Stream delta | Transient presentation update; not the durable replay unit |
| Connection | A user's integration credential, distinct from a browser or CLI login |

Hosted transcripts, inboxes, session inputs, runs, and connections use
PostgreSQL. Redis supplies wakeups and coordination. Local transcripts use
JSONL. Browser identity, Slack thread identity, and local workspace identity
must not be treated as interchangeable.

A persisted transcript does not by itself prove durable acceptance or
exactly-once tool effects. Ingress, ownership, external side effects, and
presentation have separate failure windows. See [safety and limitations](safety.md).

## Read a request through the code

1. Start at [gateway/service.go](../gateway/service.go),
   [worker/service.go](../worker/service.go), or [CLI](../cli).
2. Find the selected [profile](../packages/profiles) and its catalog/policy.
3. Follow [runtime](../packages/agent/runtime) for projection, model calls,
   tool execution, and termination.
4. Follow the adapter in [providers](../packages/providers) or
   [tools](../packages/tools) for the external operation.
5. Return to the surface for delivery and to [runs](../packages/runs) for
   observability projection.

For details, read [runtime](runtime.md), [tools](tools.md), and
[the Chinese architecture walkthrough](../architecture-site/README.md).
For proposals and remaining work, read [design status](harness-alignment.md).
