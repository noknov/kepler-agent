*What's new*
*2026-06-04*
- Evidence-first answers: tool observations are passed as evidence blocks so final incident answers can cite their sources.
- Code intelligence: the agent can look up Go/C# symbols, definitions, references, and diagnostics when the language server is available.
- More reliable Slack delivery: if streaming fails, the agent reposts the complete answer as a normal reply instead of failing silently.
- Better C# project detection: mixed repositories with nested solutions, such as backend code under a subdirectory, are recognized by code intelligence tools.

*2026-06-03*
- Streaming responses: the final answer now appears progressively in real-time instead of all at once.
- Added creative Chinese thinking prompts.
- Removed confirmation prompts: no more "type confirm" friction — tools execute directly.
- More reliable investigations: on-call questions are now organized around symptoms, evidence, likely causes, and next checks.
- Better follow-up behavior: longer Slack threads keep a compact memory of earlier context, so the conversation stays easier to continue.
- Clearer incident summaries: the agent can turn messy debugging context into a brief with status, evidence, hypotheses, and next actions.
