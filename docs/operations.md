# Operations

## Health and Shutdown

| Endpoint | Description |
|---|---|
| `GET /livez` | Liveness check |
| `GET /readyz` | Readiness check; fails during drain |
| `POST /drain` | Local-only drain switch for an orchestrator shutdown hook |
| `GET /health/dashboard` | Interactive health dashboard |
| `GET /health/tools` | Tool health status JSON |
| `GET /metrics` | Durable run and cost metrics from observability |
| `GET /runs?limit=20` | Recent run list |
| `GET /runs/<run_id>` | Run detail with LLM/tool steps and cost |

`/metrics`, `/runs`, and the health dashboard require
`Authorization: Bearer <token>` or
`X-Slack-Copilot-Agent-Admin-Token: <token>` matching `OBSERVABILITY_TOKEN`.
`X-Slack-Copilot-Admin-Token` is also accepted as a shorter equivalent. Set
`OBSERVABILITY_ALLOW_UNAUTHENTICATED=true` only for direct loopback development
access.

Slack events are first written to a durable PostgreSQL inbox. Workers claim
events with `claim_owner` and `claim_until`; abandoned events become retryable
after the lease expires. Active workers renew leases, failures use bounded
exponential backoff, and malformed or exhausted events become dead letters.

## Packaging Contract

This repository does not prescribe a production topology or include Compose,
Kubernetes, database, or Redis manifests. Run the binaries directly or package
them with the orchestrator of your choice. All modes use environment-based
configuration and the health/shutdown endpoints above.

The generic Dockerfile exposes independent targets:

```bash
docker build --target gateway -t slack-copilot-gateway .
docker build --target worker -t slack-copilot-worker .
docker build --target observability -t slack-copilot-observability .
docker build --target app-server -t slack-copilot-app-server .
docker build --target all-in-one -t slack-copilot-agent .
```

Gateway and observability use a minimal CA-only runtime. Worker and all-in-one
add Git, ripgrep, curl, and SSH for repository access. Infrastructure CLIs such
as `kubectl` and `gcloud` are deliberately not bundled; derive a worker image or
mount administrator-pinned binaries when those optional tools are enabled.

An orchestrator should:

- send ingress traffic only while `/readyz` succeeds;
- call local `POST /drain` before termination;
- allow at least `HTTP_SHUTDOWN_TIMEOUT` for graceful shutdown;
- keep `/metrics` and observability endpoints private;
- inject credentials through its own secret mechanism;
- pin built images by immutable digest.

Important runtime knobs:

```bash
SLACK_EVENT_TIMEOUT=15m
SLACK_EVENT_INBOX_LEASE=16m
SLACK_EVENT_MAX_ATTEMPTS=5
HTTP_SHUTDOWN_TIMEOUT=90s
POSTGRES_MAX_CONNS=4
WORKSPACE_AUTO_FETCH=false
```

Application processes never create or alter database objects. For a new
PostgreSQL database, apply the repository's current schema contract with your
preferred administration tool before starting services, for example:

```bash
psql "$POSTGRES_DSN" -f schema/postgres.sql
```

Startup fails with the names of missing tables. This keeps DDL privileges and
database lifecycle policy outside the agent's business code.

## ngrok

```bash
ngrok http 8080
```

Use the HTTPS forwarding URL as the Slack Request URL:

```text
https://<your-ngrok-domain>/slack/events
```

Set `HTTP_ADDR=:8080`, fill `SLACK_SIGNING_SECRET` from Slack App Basic
Information, and keep Socket Mode disabled. A reserved ngrok domain avoids
regenerating the URL on each restart.

## Search Providers

DuckDuckGo HTML search works without paid credentials:

```bash
WEB_SEARCH_PROVIDER=duckduckgo
```

For a separately managed SearXNG instance:

```bash
WEB_SEARCH_PROVIDER=searxng
WEB_SEARCH_SEARXNG_URL=http://127.0.0.1:8097
```

Hosted JSON providers:

```bash
WEB_SEARCH_PROVIDER=brave
WEB_SEARCH_BRAVE_API_KEY=...
WEB_SEARCH_BRAVE_BASE_URL=https://api.search.brave.com/res/v1/web/search

WEB_SEARCH_PROVIDER=google_cse
WEB_SEARCH_GOOGLE_API_KEY=...
WEB_SEARCH_GOOGLE_CX=...

WEB_SEARCH_PROVIDER=serpapi
WEB_SEARCH_SERPAPI_KEY=...
WEB_SEARCH_SERPAPI_BASE_URL=https://serpapi.com/search.json
```

## Playwright MCP

Start the MCP server with Docker:

```bash
docker run -d \
  --name playwright-mcp \
  --restart unless-stopped \
  -p 8931:8931 \
  -v "$(pwd)/scripts/playwright-stealth.js:/stealth.js:ro" \
  mcr.microsoft.com/playwright/mcp:latest \
  --port 8931 --host 0.0.0.0 --headless \
  --no-sandbox \
  --browser chromium \
  --viewport-size 1920x1080 \
  --user-agent "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36" \
  --ignore-https-errors \
  --init-script /stealth.js
```

Then set:

```bash
PLAYWRIGHT_MCP_URL=http://localhost:8931/mcp
```

Use `--browser chromium`, not `--browser chrome`; the official image ships
Chromium. The mounted stealth script reduces common headless-browser detection
signals before page JavaScript runs.

Run the real browser smoke test after the MCP server is up:

```bash
PLAYWRIGHT_MCP_URL=http://127.0.0.1:8931/mcp go test ./packages/toolkit/tools/playwright -run TestIntegration_PlaywrightMCPRealBrowserSmoke -count=1 -v
```


## Cost Tracking

Runs include LLM/tool steps, token usage, estimated cost, errors, Slack message
linkage, and quality feedback from emoji reactions.

Override rates to match your provider:

```bash
LLM_INPUT_COST_PER_MTOK=0
LLM_OUTPUT_COST_PER_MTOK=0
LLM_CACHE_READ_COST_PER_MTOK=0
LLM_CACHE_CREATION_COST_PER_MTOK=0
```
