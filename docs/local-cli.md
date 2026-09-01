# Local CLI

The local CLI, GUI app server, and hosted Slack agent execute the same canonical harness:

- **Local CLI:** tools and sandbox run on the user machine; model calls go to Kepler through the gateway after Slack OAuth. Sessions persist as local JSONL.
- **Hosted Agent:** the same loop on server workspaces. Slack is ingress and presentation, not a separate agent.

Eval remains a later phase. The CLI is the surface that can later be driven like Claude Code; Slack cannot be evaled directly.

## Build and run

Binaries and Docker images are built from `kepler-agent-deploy`, with this repo
as `SOURCE_DIR` only:

```sh
cd ../kepler-agent-deploy
SOURCE_DIR=../kepler-agent scripts/local-stack.sh start
SOURCE_DIR=../kepler-agent scripts/build-cli.sh
./bin/kepler-agent login
./bin/kepler-agent --cwd /path/to/project
```

Login talks only to the public gateway compiled into the binary from
`CONNECTIONS_PUBLIC_BASE_URL` (override with `--api-url` or `KEPLER_API_URL`).
Slack OAuth callback is `{CONNECTIONS_PUBLIC_BASE_URL}/cli/oauth/callback`.

Interactive mode starts when stdin is a terminal and no prompt argument is supplied:

```sh
bin/kepler-agent --cwd . "diagnose the failing tests"
printf "review this repository\n" | bin/kepler-agent --cwd . --output jsonl
bin/kepler-agent --resume
```

Optional workspace TOML (`kepler-agent config init`) only covers routing, sandbox, MCP, and prompt overlays. It does not contain provider URLs or API keys. Models are whatever the Kepler worker is configured to use.

Interactive sessions show a compact header, streamed text, and live tool lines. Use `/help`, `/status`, `/clear`, and `/exit`. Inputs during a turn are steered or queued via `input_routing`.

## Security model

The local profile resolves file operations beneath the workspace, blocks common credential paths, and uses Seatbelt on macOS or bubblewrap on Linux for execution. Subprocesses receive a minimal environment and do not inherit the Kepler session token. Network is denied unless the tool call requests it and the user grants approval.

`exec` accepts a shell `command` string (`/bin/bash -lc`) or argv. `unsafe_allow_no_sandbox` remains an explicit escape hatch.

The hosted profile has no end-user host approvals. Its policy rejects mutation effects unless the tool is on the operator allowlist.

## Shared contracts

`packages/agent` is the shared loop. Slack, CLI, and GUI construct it with different tools and policy. Model traffic from CLI/GUI is authenticated at the gateway and forwarded to the worker, which injects operator LLM credentials. Users never point the CLI at OpenAI/Anthropic directly.

Register the Slack redirect URL `{CONNECTIONS_PUBLIC_BASE_URL}/cli/oauth/callback`
(the ngrok public origin). CLI login polls that same origin; it never binds a
local OAuth port.
