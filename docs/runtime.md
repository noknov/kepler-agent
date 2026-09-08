# Shared Agent Runtime

The hosted Slack agent and local CLI use `packages/agent/runtime` as their
only model/tool loop. Product profiles inject storage, policy, tools, model
providers, and presentation; they do not implement another loop.

The canonical transcript is append-only. Hosted sessions persist events in
`agent_transcript_events`; local sessions persist the same event model as
JSONL. Context projection, compaction, steering, retries, tool execution, and
termination all derive from that transcript. `agent_runs` and
`agent_run_steps` are query-oriented observability projections, not a second
conversation state store.

Model providers translate their wire formats into canonical messages and typed
stream events. Stream deltas are sent only to transient presentation sinks;
the durable transcript stores completed model messages and lifecycle events so
replay does not duplicate token fragments. Web citations remain structured provenance on content blocks.
Prompts decide when and how to cite; presentation adapters decide how to render
the provider-supplied citation records. Dynamic status remains a projection of
canonical runtime events rather than a second execution-state model. Slack sets
its native initial status when execution starts. Once a tool call is ready to
execute, an optional progress model generates one English loading message from
sanitized tool intent. Slack displays that message through `loading_messages`;
the progress model does not decide whether a status is shown or change execution.
Status is presentation-only: it is never written to the transcript, returned to
the runtime, or placed in model context.

The current loop has no model-output repair layer. Only the owner of a
`pending_input` turn can continue it with an unmentioned thread reply;
unsupported image parts are removed before provider dispatch; and parallel tool
results share an aggregate inline budget. A model step admits parallel-safe
tools through a shared read lock and mutating tools through an exclusive write
lock, so independent reads overlap instead of waiting on list order. Empty model output fails the turn,
and the tool-step limit stops without an extra synthesis request. Empty model
messages without tool calls are retried in place up to
`MaxEmptyResponseRetries` before the turn terminates as `empty_response`. A
zero retry count means zero retries; product profiles opt into their retry
budget explicitly. Provider retries, primary/fallback selection, and circuit
breaking are handled by the profile's resilient model client rather than an
extra runtime loop.
Slack buffers streamed answer text and delivers it through Slack's native
`chat.startStream` / `chat.appendStream` / `chat.stopStream` APIs. When stream
delivery fails, the final answer is posted as a normal markdown message with a
deterministic `client_msg_id`. If the Slack app does not support that AI-only
block, it retries as a plain message. It then persists the Slack message link
on the run. It does not create a streaming placeholder or rewrite Markdown
with regular expressions.

Git-backed code tools refresh `origin` once per turn before reading remote refs.
When the caller omits a source, code read/search uses the repository's
checked-out branch upstream, normally `origin/<branch>`, without checkout. Explicit
repository-specific default refs still belong in the private deployment prompt,
not in runtime discovery or broad branch-name guessing.

Hosted capability policy is authoritative and non-interactive. Tool implementations
declare neutral capability effects in their descriptors; surface catalogs add
visibility metadata such as `Surfaces` and integration dependencies at
registration time. Local tools use the workspace sandbox and scoped approvals. TTS is
an optional external-write tool and is never automatic orchestration.

The `evals` package treats the CLI and other products as black-box processes.
Because the CLI runs the same harness, its context, tool, retry, and termination
results exercise the shared runtime used by Slack.

Hosted profiles enable the optional circuit breaker by default. It blocks
identical repeated tool calls after configurable failure or success thresholds.

The JSON-RPC app server (`appserver/cmd/app-server`) exposes the same local
runtime over stdio with `thread/start`, `thread/resume`, `thread/fork`,
`turn/start`, `turn/steer`, `turn/interrupt`, and Codex-style item
notifications including `item/agentMessage/delta`.

`thread/trajectory` is a read-only reconstruction API. It derives a redacted
sequence of prompt/context, provider-attempt, tool, recovery, and termination
facts from the canonical transcript; it never exposes message content, tool
arguments, or arbitrary metadata by default.

The server admits at most eight active turns by default. When its local
bulkhead is full, `turn/start` returns JSON-RPC error `-32001` (`server
overloaded; retry later`); clients retry with exponential backoff and jitter.
This protects the app-server process from stalled providers or tools without
discarding an already accepted turn.

`agent-explore` is a hosted read-only tool, not a second product runtime. It
creates isolated child turns from a filtered catalog, records a parent link and
its own transcript, and returns a factual report to the parent. Child stream
events are not sent to the parent Slack presentation sink. Read-only and
network tools default to `Parallel` so a step with multiple independent calls
runs concurrently; mutating tools stay sequential unless marked otherwise.

Delegated work uses a transport-neutral task contract: a stable worker name,
role, objective, boundaries, required deliverable, and observable success
criteria. `delegation.Runner.RunTask` exposes the same primitive to product
workflows without model tool-call JSON. A task batch is bounded and
cancellation-aware; each result includes its child session/turn, identity,
termination, usage, and error as durable audit metadata.

Batch delegation accepts only fully described, uniquely named leaf tasks. The
lead remains the sole coordinator and chooses roles, risk hypotheses, team
size, and follow-up work from the evidence it discovers; the runtime does not
encode a fixed review graph or require every fan-out to have the same size.

Delegation inherits the parent turn deadline by default. A deployment may add
optional batch and worker deadlines when it needs tighter isolation. A
configured worker deadline begins only after that worker acquires a concurrency
slot, so queueing cannot consume its model retry budget before it starts.

Slack uses the configured secondary model as a small semantic router for new
conversations. The router returns a validated `general|code_review` decision;
it has no tools and never uses keyword or regular-expression intent matching.
Code Review then extracts and validates one to four GitHub PR URLs from the
original message and accepts an optional `fast|standard|deep` mode. Routing
failures fall back to general conversation. CLI, app-server, and other surfaces
do not install this Slack product router and remain generic.
The ordinary hosted agent becomes the coordinator and uses the same runtime to
triage an immutable GitHub PR head, launch a bounded risk-based reviewer batch,
verify candidate findings itself or with targeted follow-up workers, and
synthesize one report. Slack renders coordinator-authored plan tasks as the
team view and defers model text until the final response. Worker streams and
reports remain internal, while their completion, usage, result, and parent link
remain durable delegation events. The lead alone receives those reports,
verifies candidate evidence, and publishes the normal Slack response after
fan-in.

Workflow selection produces a transport-neutral activation containing its
prompt, durable scope, required tools, and thread-ownership policy. Code Review
stores the normalized PR URLs and mode in turn scope. A later reply from the
same user in the same Slack thread restores that activation without reparsing
the follow-up as a new review command; a new root message remains a new session.
The activation also places the validated PR set in delegation shared context,
which the execution layer injects into every worker independently of the
lead-authored task description. Worker targets therefore cannot disappear
during dynamic decomposition. GitHub PR manifests are indexed independently by
repository and pull-request number inside each worker turn; file reads must
select a PR URL when more than one manifest is active, so concurrent multi-PR
inspection cannot overwrite or ambiguously reuse another PR's head context.
