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
canonical runtime events rather than a second execution-state model. Slack does
not set a status while the model is considering the request. Once a tool call is
ready to execute, a secondary model generates one English loading message from
the sanitized tool intent. Slack displays that message through
`loading_messages`; no model text is used to decide whether a message is shown.
Status is presentation-only: it is never written to the transcript, returned to
the runtime, or placed in model context.

The current loop has no model-output repair layer. Only the owner of a
`pending_input` turn can continue it with an unmentioned thread reply;
unsupported image parts are removed before provider dispatch; and parallel tool
results share an aggregate inline budget. Empty model output fails the turn,
the tool-step limit stops without an extra synthesis request, and retryable
typed provider failures are retried only by the runtime. Empty model messages
without tool calls are retried in place up to `MaxEmptyResponseRetries` before
the turn terminates as `empty_response`. A zero retry count now means zero
retries; product profiles opt into their retry budget explicitly.
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
