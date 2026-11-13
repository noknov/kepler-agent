# Noninteractive benchmark adapter

`benchmark/cmd/kepler-bench` is a single-shot process adapter over the canonical
runtime. It has no interactive prompt, terminal rendering, browser product,
app-server protocol, or dedicated review workflow. Local workspace tools,
JSONL transcript storage and the OS sandbox are retained for benchmark tasks.

Build from source for a local smoke test:

```sh
go build -o /tmp/kepler-bench ./benchmark/cmd/kepler-bench
/tmp/kepler-bench --cwd /path/to/isolated/workspace --output jsonl "Inspect the task and verify the result"
```

The adapter uses authenticated gateway bootstrap and generation. Supply
`KEPLER_API_URL` / `KEPLER_TOKEN`, or use `kepler-bench login --api-url <https-origin>`
for evaluation access. These operator endpoints live under `/benchmark/`;
retired `/cli/` and browser-chat routes are not aliases. Update the registered
Slack OAuth redirect to `<origin>/benchmark/oauth/callback` when using login.
Integration OAuth callbacks under `/oauth/` are independent and unchanged.

JSONL is the default: canonical events are written to stdout and diagnostics to
stderr. `--output text` prints only the final answer. Missing input on a terminal
fails immediately. Empty piped input is rejected. SIGINT/SIGTERM cancel execution
and allow the runtime to persist terminal state and close command descendants.
Output write failures produce a nonzero exit; they do not become successful runs.

`--config`, `--cwd`, `--state-dir`, `--session`, `--resume`, `--approval`,
`--output`, and `--api-url` are supported. The local profile configures MCP,
context, step/token budgets and sandbox settings. Approval defaults to deny;
benchmark adapters must choose their policy explicitly. A benchmark approval is
not permission to access the operator's host or an upstream resource.

Production packaging is `scripts/build-benchmark.sh` in the deploy repository.
Harbor and AppWorld adapters invoke this process directly and retain its events,
source identity and budgets. They do not substitute another agent loop.
