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
the provider-supplied citation records. Dynamic status uses deterministic
runtime and tool lifecycle events, so it does not require a second status model.
For tool steps, the primary model includes a short action-and-target narration
in the same tool-call turn; Slack projects that assistant event into transient
`loading_messages`. Turn, context, compaction, retry, approval, tool, and
terminal events provide lifecycle states. If a tool-call turn has no narration,
Slack keeps the existing localized `Thinking...` state; it never substitutes a
tool name or a synthetic tool-status fallback. Status display is not
conversation state beyond the canonical assistant event.

The current loop preserves the product-critical behavior from v1 without its
heuristic repair layers: only the owner of a `pending_input` turn can continue
it with an unmentioned thread reply; unsupported image parts are removed before
provider dispatch; parallel tool results share an aggregate inline budget; an
empty response is retried; and reaching the tool-step limit triggers one final,
tool-free synthesis pass. Slack falls back to a normal thread message if final
stream delivery fails.

Hosted capability policy is authoritative and non-interactive. Local tools use
the workspace sandbox and scoped approvals. TTS is an optional external-write
tool and is never automatic orchestration.

The `evals` package treats the CLI and other products as black-box processes.
Because the CLI runs the same harness, its context, tool, retry, and termination
results exercise the shared runtime used by Slack.
