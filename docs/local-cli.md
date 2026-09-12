# Local CLI

The CLI runs tools in a selected local workspace and uses the authenticated
Kepler gateway for models. Sessions persist as local JSONL. The same local
profile is available through the stdio app-server used by the terminal UI.

## Build and log in

Prerequisites: Go, Node.js, pnpm, a supported OS sandbox, and access to a
configured Kepler gateway. Start in the sibling `kepler-agent-deploy` repository:

```sh
SOURCE_DIR=../kepler-agent scripts/build-cli.sh
./bin/kepler-agent login
./bin/kepler-agent whoami
./bin/kepler-agent --cwd ../kepler-agent
```

The bundle contains `bin/kepler-agent`, `bin/kepler-agent-app-server`, and
`bin/ui/main.js`. Interactive mode needs Node.js on PATH. Keep the bundle
layout intact when moving it. Use `--cwd /path/to/project` to select another
workspace without changing the path to the executable.

The public gateway URL is compiled from deploy configuration; `--api-url` or
`KEPLER_API_URL` can override it. Slack OAuth returns to
`https://<public-origin>/cli/oauth/callback`. The CLI polls the public gateway;
it does not open a local OAuth callback server.

## Interactive and headless modes

With terminal stdin and no prompt argument, the launcher starts Ink, which
spawns the Go app-server and communicates over stdio JSON-RPC. A prompt
argument or piped input selects headless execution:

```sh
# Run from kepler-agent-deploy; tools use the selected source workspace.
./bin/kepler-agent --cwd ../kepler-agent "Explain the runtime entry points"
printf 'Explain the runtime entry points
' | ./bin/kepler-agent --cwd ../kepler-agent --output jsonl
```

Headless approval defaults to `deny`. `--approval` accepts `deny`, `once`,
`session`, or `project`; select the intended scope explicitly for automated
work. Interactive commands include `/help`, `/status`, `/clear`, and `/exit`.
Do not treat clearing the visible UI as proof that persisted context was deleted.

## Configuration and sessions

| Option | Purpose |
| --- | --- |
| `--cwd` | Workspace root; defaults to current directory |
| `--config` | Explicit local TOML path |
| `--state-dir` | Session and approval state location |
| `--session` | Session ID to create or resume |
| `--resume` | Resume the most recently modified session |
| `--input-routing` | `steer` or `queue` for input arriving during execution |
| `--output` | `text` or `jsonl`; interactive mode requires text |

Run `kepler-agent config init` to create the local configuration. See the
[example](../cli/config.example.toml) and [loader](../packages/profiles/local/config.go).
TOML configures runtime limits, input routing, sandbox read roots, prompt files,
skills, and MCP. Provider/model selection is received through gateway bootstrap;
the current CLI does not accept direct `--provider`, `--model`, `--protocol`,
or `--api-key-env` flags. Older benchmark adapters must be matched to their
source revision, not assumed compatible with this launcher.

Use `kepler-agent connect <provider>` for supported integration connections.
That is separate from the CLI login session.

## Troubleshooting

| Symptom | First check |
| --- | --- |
| Login fails or polls indefinitely | Public gateway URL, Slack redirect URI, allowlist, gateway readiness |
| Headless works but interactive mode fails | Node.js on PATH and complete UI/app-server bundle |
| An old example reports an unknown flag | Current [flag definitions](../cli/cli.go); do not add provider credentials to TOML |
| Tool is denied | Workspace/read roots, approval scope, available OS sandbox |
| Model bootstrap or generation fails | Gateway/worker configuration and logs; model keys belong to the operator |

See [safety](safety.md) for filesystem and execution boundaries,
[runtime](runtime.md) for protocol/lifecycle behavior, and
[operations](operations.md) for hosted dependencies.
