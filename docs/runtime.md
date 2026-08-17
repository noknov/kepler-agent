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
canonical runtime events rather than a second execution-state model. Slack may
use the optional secondary model to turn the redacted user request, confirmed
tool calls, and published tool descriptions into a structured action-and-target
loading message while the native Slack assistant status remains the localized
thinking status. Tool descriptions are the primary semantic source for the
current operation; the user request and arguments only identify the concrete
object, so progress labels do not merely restate the user's final task. That
label is presentation-only: it is never written to the transcript, returned to
the runtime, or placed in model context. Once Slack has a specific progress
loading message, later ordinary model-request lifecycle events do not replace it
with hard-coded loading text.
If no secondary model is configured, it fails, or its output violates the label
schema, Slack keeps the localized native thinking status without a dynamic
loading message. Context projection and compaction do not replace it with
momentary status flashes.

The current loop has no model-output repair layer. Only the owner of a
`pending_input` turn can continue it with an unmentioned thread reply;
unsupported image parts are removed before provider dispatch; and parallel tool
results share an aggregate inline budget. Empty model output fails the turn,
the tool-step limit stops without an extra synthesis request, and retryable
typed provider failures are retried only by the runtime. A zero retry count now
means zero retries; product profiles opt into their retry budget explicitly.
Slack buffers the final answer and posts one complete Block Kit `markdown`
message with a deterministic `client_msg_id`; if the Slack app does not support
that AI-only block, it retries as a plain message. It then persists the Slack
message link on the run. It does not create a
streaming placeholder or rewrite Markdown with regular expressions.

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
runtime over stdio with `turn/start`, `turn/steer`, `turn/cancel`, and
transcript `event` notifications.
