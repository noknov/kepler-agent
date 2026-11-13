# Shared Agent Runtime

The hosted Slack agent and local benchmark adapter use `packages/agent/runtime` as their
only model/tool loop. Product profiles inject storage, policy, tools, model
providers, and presentation; they do not implement another loop.

## Transcript and presentation

Tool-result completeness is projected from its canonical `Truncated` flag into
a model-visible notice. This applies to source-limited results and runtime
omission/spill alike; referenced artifacts remain accessible. The derived notice
is included in context budgeting and does not modify stored source evidence.

The canonical transcript is append-only. Hosted sessions persist events in
`agent_transcript_events`; local sessions persist the same event model as
JSONL. Context projection, compaction, steering, retries, tool execution, and
termination all derive from that transcript. Initial user input is recovered
independently of the turn-start marker, so a failed input write cannot let a
retried model request run without the user message. Stable event IDs make
ambiguous commits idempotent. Completed, failed, and canceled turns are terminal
for durable queue acknowledgement. Terminal persistence runs under a bounded
cleanup context (10 seconds by default), including after cancellation.

Session routing reads the latest terminal turn and its original owner/scope.
PostgreSQL retrieves only the terminal and start facts in one snapshot through
`transcript.TerminalTurnLoader`; unrelated model and tool payloads are not
loaded for this projection. Other stores retain the canonical full-read path.
A missing start never revives an older pending turn, and a newer active turn
does not replace the latest terminal state. Full audit and replay reads are unchanged.

Local JSONL storage maintains up to 64 session indexes of IDs and byte offsets.
Warm appends parse only external additions. File replacement or truncation
invalidates the index; a malformed incomplete tail is truncated and a valid
unterminated JSON line gains a newline. Different sessions do not share an I/O
mutex. Per-session file locks coordinate writers across processes and honor
cancellation while waiting; appends retain file sync and atomic batch replacement.
Local benchmark adapter also hold a separate OS turn lease for the complete
session execution, so multiple processes cannot concurrently resume the same
session. Runtime admission waiting honors cancellation.

Context projection includes ephemeral environment context and current-turn images,
and reserves the configured response cap as well as tool-schema and safety
overhead. Complete assembled requests are checked again before dispatch. An impossible input budget fails before dispatch instead
of falling back to the full context window. Before dropping older turn groups
or invoking model-driven compaction, `BoundedProjector` applies **two-stage
observation masking**: historical `ToolResult` text and JSON blocks older than
the most recent two complete exchanges and larger than 2,400 bytes are folded into
UTF-8-safe head/tail previews with omitted-byte annotations and session-scoped
`context_read` references. The reader is activated only when historical tool
evidence is masked or compacted; it cannot select another session. The compactor
receives original recorded evidence, not masked previews, and writes a structured
continuation checkpoint covering task constraints, decisions, verified work,
failures, next actions and evidence references. With a compactor configured, the
runtime first targets 85% of the available input budget. A proactive summary
failure falls back to the hard budget only when no history must be discarded.
Incomplete model responses cannot become portable compaction checkpoints. Projection and final admission use one input-budget calculation for history,
tool schemas, output and safety reserve. On a provider context-limit rejection,
the same projector selects removable exchanges while retaining the current input
and complete latest tool batch; recovery does not guess a smaller model window.
Both paths commit through one checkpoint owner. Empty or oversized checkpoints
are rejected before durable append. Checkpoint metadata records retained/summary
tokens, the output allowance, elapsed time, coverage and recovery reason.

Static prompt layers
(`LayerCore`, `LayerProduct`, `LayerEnvironment`) are automatically separated
from dynamic layers (`LayerProject`, `LayerUser`, `LayerSkill`, `LayerTurn`) via
`---DYNAMIC_CONTEXT_BELOW---`, enabling prefix and incremental prompt caching.

`agent_runs` and
`agent_run_steps` are query-oriented observability projections, not a second
conversation state store.

Runtime-supplied durable event keys are scoped by the exact session, turn and
logical operation. Their encoded tuple produces an opaque stable ID, so local
step/request/steering labels cannot collide across sessions or ambiguous string
separators. Store-level duplicate appends return facts only in the requested
session/turn. Event IDs are opaque; consumers use typed fields and metadata.

Model providers translate their wire formats into canonical messages and typed
stream events. Stream deltas are sent only to transient presentation sinks;
the durable transcript stores completed model messages and lifecycle events so
replay does not duplicate token fragments. Provider web-search citations remain structured provenance on content blocks.
Prompts decide when and how to cite; presentation adapters decide how to render
the provider-supplied citation records. Dynamic status remains a projection of
canonical runtime events rather than a second execution-state model. Slack sets
its native initial status when execution starts and derives session lifecycle
and plan updates from canonical events. There is no progress-model request or
separate progress-model configuration.
Status is presentation-only: it is never written to the transcript, returned to
the runtime, or placed in model context.

## Execution and termination

The current loop has no model-output repair layer. unsupported image parts are removed before provider dispatch; and parallel tool
results share an aggregate inline budget. A model step admits parallel-safe
tools through a shared read lock and mutating tools through an exclusive write
lock, so independent reads overlap instead of waiting on list order. The tool-step limit stops without an extra synthesis request. Empty model
messages without tool calls are retried in place up to
`MaxEmptyResponseRetries` before the turn terminates as `empty_response`. A
zero retry count means zero retries; product profiles opt into their retry
budget explicitly. Provider retries, primary/fallback selection, and circuit
breaking are handled by the profile's resilient model client rather than an
extra runtime loop. An open primary circuit skips that provider while still
allowing a configured healthy fallback; a stream that has committed visible
output remains non-retryable.

Slack buffers streamed answer text and delivers it through Slack's native
`chat.startStream` / `chat.appendStream` / `chat.stopStream` APIs. One scheduled
or in-flight delivery coalesces new deltas without blocking the model callback;
completion drains pending text before final delivery. A final answer that revises
rather than extends the streamed text replaces the existing message. If delivery fails after a
message ID is known, the final answer repairs that message. An uncertain start
without a message ID does not post a duplicate. Other failures fall back to a
normal markdown message with a deterministic `client_msg_id`. If the Slack app does not support that AI-only
block, it retries as a plain message. It then persists the Slack message link
on the run. It does not create a streaming placeholder or rewrite Markdown
with regular expressions.

## Tools and policy

Git-backed code tools refresh `origin` once per turn before reading remote refs.
When the caller omits a source, code read/search uses the repository's
checked-out branch upstream, normally `origin/<branch>`, without checkout. Explicit
repository-specific default refs still belong in a private environment prompt,
not in runtime discovery or broad branch-name guessing.

Hosted capability policy is authoritative and non-interactive. Tool implementations
declare neutral capability effects in their descriptors; surface catalogs add
visibility metadata such as `Surfaces` and integration dependencies at
registration time. Local tools use the workspace sandbox and scoped approvals. TTS is
an optional external-write tool and is never automatic orchestration.

The `evals` package treats the benchmark adapter as a black-box process.
Because it runs the same harness, its context, tool, retry, and termination
results exercise the shared runtime used by Slack.

Hosted and local profiles enable the optional circuit breaker by default. It blocks
identical repeated failed tool calls at the configured failure threshold and
appends a pre-trip self-correction warning (`CircuitWarning`) to repeated tool
error results on the penultimate failure. This is a liveness/cost guard, not
authorization. The implementation and reset semantics live in
[circuit.go](../packages/agent/runtime/circuit.go).


## Delegation

`agent-explore` is a read-only delegation tool, not a second product runtime. It
creates isolated child turns from a filtered catalog with its own circuit breaker
(`MaxIdenticalFailedAttempts: 3`), records a parent link and its own transcript,
and returns a factual report to the parent. Child `model.Usage` across single or
batch subtasks is rolled up into the tool result metadata and added to the
parent turn's `TurnResult.Usage`. Each fresh child
has one invocation owner and inherits the parent's cancellation context. It does
not acquire another distributed session lease, avoiding starvation when all
lease connections belong to parents awaiting children. Child stream
events are not sent to the parent Slack presentation sink. Read-only and
network tools default to `Parallel` so a step with multiple independent calls
runs concurrently; mutating tools stay sequential unless marked otherwise.

Model dispatch shares a process-wide governor (16 requests by default) across parent turns,
workers and compaction. Embeddings can supply a shared `model.Governor` through
runtime dependencies; hosted operators can set `AGENT_MAX_CONCURRENT_MODEL_CALLS`. Optional `AGENT_MAX_TOTAL_TOKENS` (hosted) or
`max_total_tokens` (local) adds a parent/worker token allowance; zero keeps it
disabled. Concurrent workers reserve estimated input plus output before dispatch
and settle against reported usage. Unknown failed requests retain their estimate.
These are local estimates, not a provider billing guarantee. Usage has a typed
tool-result field, and terminal results restore the same usage and step counts
on replay. Live and recovered turn results derive generation usage from committed
model completions and deduplicated tool results, including completed empty-response
recovery requests. Embedding usage stays separate. Per-attempt receipts and budget
settlement also cover failed attempts; turn-result usage is not a billing ledger.
Aggregated turn and delegated-batch input counts include cache tokens.
`model.Usage.Add` normalizes each receipt before addition, so mixing providers
whose input counts include or exclude cache tokens preserves the exact total.
Per-attempt receipts keep their provider's original accounting convention.
Terminal events retain structured model and context error categories; replay
restores cancellation, deadlines, and safe failure classification without
interpreting diagnostic text. Original Go error types and arbitrary causes are
not serialized.

Recovery closes generated-but-unstarted tool calls with an explicit
`execution_not_started` result. Started calls with uncertain effects remain
`execution_interrupted` and are never automatically repeated. A model must make
a fresh tool decision after either result. Public commentary remains assistant
text with a commentary phase, separately from provider-native reasoning. Tool
images survive canonical/provider conversion and use a separate 8 MiB aggregate
image budget rather than being counted as inline prose.

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


Local spilled tool evidence uses opaque `artifact://` references and an eager,
session-scoped `artifact_read` tool in both the benchmark adapter (including
read-only exploration workers). Its bounded UTF-8 pages do not require extending
workspace read permissions. Artifact IDs bind turn and payload to prevent reused
call IDs from overwriting earlier evidence. Legacy bare-path artifacts are not
accepted by this reader.

Related: [architecture](architecture.md), [Slack usage](slack.md), [safety and limitations](safety.md).

## Structured model completions

The shared model request accepts an optional `OutputFormat` containing a name,
self-contained JSON Schema, and explicit mode. `native` (also the default)
sends provider constraints through OpenAI Chat `response_format`, Responses
`text.format`, or Anthropic `output_config.format`; provider/model support and
schema-subset restrictions still apply. Unsupported native requests fail rather
than retrying as prose. The native Qoder protocol rejects this mode explicitly.
`validated` preserves unconstrained provider generation and validates the final
JSON locally, including exact JSON numbers. It is useful for existing providers
without native constraints; it does not promise grammar-constrained generation.

Structured completions are tool-free and separate from ordinary agent turns.
The provider adapter checks the schema before dispatch, disallows external
schema loading, and checks completion and schema compliance before publishing
any content. A rejected completion retains usage but exposes neither raw output
nor value-bearing validator diagnostics. Internal schema references are allowed;
provider-specific schema vocabulary is not silently rewritten. Free-text agent
turns retain incremental streaming. Memory reranking uses explicit validated
mode and still applies its own candidate/selection/version checks; schema
validation cannot establish factual relevance or authorization.
Memory recall traces distinguish disabled ranking, no candidates, completed
selection, ranker failure and rejected selections. Ranking failures retain the
original candidates and stay observable without marking a successful fallback
as a failed recall. Trace data includes counts and bounded error kinds, never
query text, memory contents/keys, identities or raw error messages.

References: [OpenAI structured outputs](https://developers.openai.com/api/docs/guides/structured-outputs),
[Anthropic structured outputs](https://platform.claude.com/docs/en/build-with-claude/structured-outputs).

Across a remote Kepler model connection, native output also requires the returned
`output_mode=native` receipt. A legacy server ignoring the optional request field
cannot silently turn native mode into local validation. Missing/wrong receipts
fail without publishing content or retrying the paid request; reported usage is
retained. Explicit validated mode remains usable with legacy remote models.

Tool-call IDs are nonempty and unique within each model batch. Before persisting
an executable assistant message or consulting policy, the runtime rejects an
ambiguous batch as a non-retryable provider-protocol failure. Within a turn, a
completed ID is bound to its original tool name and JSON arguments. Equivalent
JSONB key ordering and exact numeric values survive replay; retargeting an ID
fails before any sibling executes. Reusing an identical durable result adds the
required tool reply without recharging child usage or reapplying plan/task-state
transitions. A fresh action requires a fresh ID. This complements, rather than
replaces, downstream idempotency and interrupted-effect reconciliation.

Empty-response recovery applies only to an explicit normal `stop` receipt and
uses the configured retry budget. Length limits, refusal/filtering, cancellation,
missing canonical finish reasons and malformed tool-call completions do not enter that
recovery path. An empty refused/filtered response uses the existing non-retryable
content-policy error; a canceled provider receipt uses the existing canceled
turn state. Reported usage is retained for these terminal attempts.

## Slack admission, delivery and control

Slack inbox handlers persist authenticated input and return without waiting for
model execution. Bounded execution slots consume the session queue. Stop and
same-user steering remain intake operations while every execution slot is busy.
Input claims renew independently of turn timeouts. Execution failures use durable
retry deadlines and dead letters; uncertain delivery is not a retryable create.

`slack_reply_receipts` records creation intent, known message identity and native
session status. Recovery reuses the known message instead of creating another.
An interrupted intent without a receipt becomes uncertain. Terminal status is
reconciled under runtime session ownership so it cannot overwrite a successor.
These are surface facts, not model claims or run-projection completion flags.

Task-state updates can explicitly archive completed/superseded IDs under the
same revision check. Active constraints and unresolved effects cannot be archived.
Previous snapshots and evidence remain in the append-only transcript.
