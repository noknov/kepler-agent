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
stream events. Web citations remain structured provenance on content blocks.
Prompts decide when and how to cite; presentation adapters decide how to render
the provider-supplied citation records. Dynamic status remains a projection of
canonical runtime events rather than a second execution-state model. Slack may
use the optional secondary model to turn the redacted user request and confirmed
tool names into a structured action-and-target label. That label is
presentation-only: it is never written to the transcript, returned to the
runtime, or placed in model context.
If no secondary model is configured, it fails, or its output violates the label
schema, Slack keeps the localized `Thinking...` state. Context projection and
compaction do not replace it with momentary status flashes.

The current loop has no model-output repair layer. Only the owner of a
`pending_input` turn can continue it with an unmentioned thread reply;
unsupported image parts are removed before provider dispatch; and parallel tool
results share an aggregate inline budget. Empty model output fails the turn,
the tool-step limit stops without an extra synthesis request, and retryable
typed provider failures are retried only by the runtime. Slack buffers the final
answer and posts one complete Block Kit `markdown` message. It does not create a
streaming placeholder or rewrite Markdown with regular expressions.

Git-backed code tools require an explicit branch or ref. Repository-specific
default refs belong in the private deployment prompt, not in runtime discovery
or branch-name guessing.

Hosted capability policy is authoritative and non-interactive. Local tools use
the workspace sandbox and scoped approvals. TTS is an optional external-write
tool and is never automatic orchestration.

The `evals` package treats the CLI and other products as black-box processes.
Because the CLI runs the same harness, its context, tool, retry, and termination
results exercise the shared runtime used by Slack.
