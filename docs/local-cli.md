# Local CLI

The local CLI and hosted Slack agent execute the same canonical harness:

- **Local CLI:** executes the complete loop on the user machine, writes only inside the selected workspace, persists JSONL sessions locally, and asks for network/external approvals.
- **Hosted Agent:** embeds the same loop on server workspaces with authoritative server policy. Slack is an ingress and presentation adapter, not a separate agent.

## Build and run

```sh
go build -o bin/slack-copilot ./cli/cmd/slack-copilot
cp cli/config.example.toml ~/.config/slack-copilot-agent/config.toml
export OPENAI_API_KEY=...
bin/slack-copilot --cwd /path/to/project
```

Interactive mode starts when stdin is a terminal and no prompt argument is supplied. Otherwise the same binary runs headlessly:

```sh
bin/slack-copilot --cwd . "diagnose the failing tests"
printf "review this repository\n" | bin/slack-copilot --cwd . --output jsonl
bin/slack-copilot --resume
```

Inputs typed during an active turn are either injected as steering at the next model boundary or queued as the next turn, controlled by `input_routing`. This setting belongs to the session surface; it is not hard-coded by Slack versus CLI.

## Security model

The local profile resolves file operations beneath the canonical workspace, blocks common credential paths, and uses Seatbelt on macOS or bubblewrap on Linux for shell commands. Shell subprocesses receive a minimal environment and do not inherit model API keys. Network is denied unless the tool call requests it and the user grants approval. Grants can apply once, to the current process session, or to the exact command for this project; persistent grants live in the agent state directory, not the repository.

If the OS sandbox is unavailable, command execution fails closed. `unsafe_allow_no_sandbox` / `--unsafe-allow-no-sandbox` is an explicit development escape hatch and should not be used for untrusted repositories or prompts.

The sandbox canonicalizes additional read roots and rejects broad `/` or home
directory grants. Linux execution also isolates PID, IPC, UTS, cgroup, and
network namespaces and drops capabilities. Subprocess environments cannot
override `HOME`, `PATH`, `TMPDIR`, or inject loader variables. Common repository
credential files—including `.git/config` when it contains an embedded remote
credential—are denied to file tools and sandboxed commands.

The hosted profile has no end-user host approvals. Its policy rejects mutation effects and requires an operator allowlist. Optional command execution is an injected argv executor with no shell string; deployment code remains responsible for the container or kernel sandbox.

## Shared contracts

`packages/agent` contains provider-neutral messages and events, structured tools/effects, deterministic prompt layers, an append-only transcript, bounded context projection and compaction, retry/termination logic, session-level deferred tools, and product profiles. OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages adapters convert wire events into the same canonical model. The local and hosted profiles construct those adapters through one factory, so CLI harness evaluation covers production provider behavior.

Prompt order is fixed: core, product, environment, project, user overlay, skill, turn. Repository `AGENTS.md` is loaded as project guidance. Private overlays are read only from explicit `prompt_files`; do not commit internal repository instructions to this public project.

File skills are discovered from `.agents/skills`, `.codex/skills`, and configured `skill_roots`; only name and description enter the prompt, while `skill_load` reads the full `SKILL.md` on demand. Configured Streamable HTTP MCP servers are initialized at startup, their tools are namespaced and deferred, and calls flow through the same effect policy and approval system. A configured MCP endpoint is an explicit trust decision for tool discovery; individual network/external-write calls still require the declared policy decision.

## Evaluation

The independent [evaluation module](../evals/README.md) launches this CLI and other agents as subprocesses. It deliberately does not import runtime code. Use it to compare Codex, Claude Code, Pi, OpenCode, and this harness against one controlled model gateway.
