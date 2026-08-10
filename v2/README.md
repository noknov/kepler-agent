# Agent v2

v2 is a clean, parallel implementation. It keeps v1 available while establishing one shared harness for two products:

- **Local CLI:** executes the complete loop on the user machine, writes only inside the selected workspace, persists JSONL sessions locally, and asks for network/external approvals.
- **Hosted Agent:** embeds the same loop on server workspaces with authoritative server policy. Slack, HTTP, and app-server are ingress/presentation adapters, not separate agents.

This code is usable for development but is not the current stable Slack production path.

## Build and run

```sh
go build -o bin/slack-copilot-v2 ./v2/cmd/slack-copilot
cp v2/config.example.toml ~/.config/slack-copilot-agent/config.toml
export OPENAI_API_KEY=...
bin/slack-copilot-v2 --cwd /path/to/project
```

Interactive mode starts when stdin is a terminal and no prompt argument is supplied. Otherwise the same binary runs headlessly:

```sh
bin/slack-copilot-v2 --cwd . "diagnose the failing tests"
printf "review this repository\n" | bin/slack-copilot-v2 --cwd . --output jsonl
bin/slack-copilot-v2 --resume
```

Inputs typed during an active turn are either injected as steering at the next model boundary or queued as the next turn, controlled by `input_routing`. This setting belongs to the session surface; it is not hard-coded by Slack versus CLI.

## Security model

The local profile resolves file operations beneath the canonical workspace, blocks common credential paths, and uses Seatbelt on macOS or bubblewrap on Linux for shell commands. Shell subprocesses receive a minimal environment and do not inherit model API keys. Network is denied unless the tool call requests it and the user grants approval. Grants can apply once, to the current process session, or to the exact command for this project; persistent grants live in the agent state directory, not the repository.

If the OS sandbox is unavailable, command execution fails closed. `unsafe_allow_no_sandbox` / `--unsafe-allow-no-sandbox` is an explicit development escape hatch and should not be used for untrusted repositories or prompts.

The hosted profile has no end-user host approvals. Its policy rejects mutation effects and requires an operator allowlist. Optional command execution is an injected argv executor with no shell string; deployment code remains responsible for the container or kernel sandbox.

## Shared contracts

`packages/agentv2` contains provider-neutral messages and events, structured tools/effects, deterministic prompt layers, an append-only transcript, bounded context projection and compaction, retry/termination logic, session-level deferred tools, and product profiles. OpenAI Chat Completions-compatible and Anthropic Messages-compatible adapters convert wire events into the same canonical model.

Prompt order is fixed: core, product, environment, project, user overlay, skill, turn. Repository `AGENTS.md` is loaded as project guidance. Private overlays are read only from explicit `prompt_files`; do not commit internal repository instructions to this public project.

File skills are discovered from `.agents/skills`, `.codex/skills`, and configured `skill_roots`; only name and description enter the prompt, while `skill_load` reads the full `SKILL.md` on demand. Configured Streamable HTTP MCP servers are initialized at startup, their tools are namespaced and deferred, and calls flow through the same effect policy and approval system. A configured MCP endpoint is an explicit trust decision for tool discovery; individual network/external-write calls still require the declared policy decision.

## Evaluation

The independent [evaluation module](../evals/README.md) launches this CLI and other agents as subprocesses. It deliberately does not import v2 runtime code. Use it to compare Codex, Claude Code, Pi, OpenCode, and this harness against one controlled model gateway.
