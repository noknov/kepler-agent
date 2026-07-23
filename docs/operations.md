# Operations

## Health and Shutdown

| Endpoint | Description |
|---|---|
| `GET /livez` | Liveness check |
| `GET /readyz` | Readiness check; fails during drain |
| `POST /drain` | Local-only drain switch used by Kubernetes `preStop` |
| `GET /health/dashboard` | Interactive health dashboard |
| `GET /health/tools` | Tool health status JSON |
| `GET /health/tools/rag` | RAG health status JSON |
| `GET /metrics` | Run and cost metrics |
| `GET /runs?limit=20` | Recent run list |
| `GET /runs/<run_id>` | Run detail with LLM/tool steps and cost |

`/metrics`, `/runs`, and the health dashboard require
`Authorization: Bearer <token>` or
`X-Slack-Copilot-Agent-Admin-Token: <token>` matching `OBSERVABILITY_TOKEN`.
The older `X-Oncall-Agent-Admin-Token` header is still accepted for
compatibility. Set `OBSERVABILITY_ALLOW_UNAUTHENTICATED=true` only for direct
loopback development access.

Slack events are first written to a durable PostgreSQL inbox. Workers claim
events with `claim_owner` and `claim_until`; abandoned events become retryable
after the lease expires.

## Docker

```bash
docker build -t ghcr.io/your-org/slack-copilot-agent:latest .
```

The runtime image includes `git`, `ripgrep`, `curl`, CA certificates, and
`openssh-client` because the code, repo, and preStop workflows need them.

## Kubernetes

Starter manifests live in `deploy/k8s/`.

```bash
kubectl create namespace slack-copilot-agent
kubectl -n slack-copilot-agent create secret generic slack-copilot-agent-secrets \
  --from-literal=SLACK_BOT_TOKEN='xoxb-...' \
  --from-literal=SLACK_SIGNING_SECRET='...' \
  --from-literal=POSTGRES_DSN='postgres://...' \
  --from-literal=LONGCAT_API_KEY='Bearer lc-...'

kubectl apply -f deploy/k8s/
```

Start with one replica. Scale after PostgreSQL capacity, Slack retries, and
workspace/RAG background jobs are understood for your environment.

Important knobs:

```bash
SLACK_EVENT_TIMEOUT=15m
SLACK_EVENT_INBOX_LEASE=16m
HTTP_SHUTDOWN_TIMEOUT=90s
POSTGRES_MAX_CONNS=4
RAG_BACKGROUND_INDEX=false
WORKSPACE_AUTO_FETCH=false
```

`preStop` calls local `/drain`, making `/readyz` fail before SIGTERM so
Kubernetes can remove the pod from endpoints before shutdown completes.

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

For self-hosted search:

```bash
docker compose -f docker-compose.search.yml up -d

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
PLAYWRIGHT_MCP_URL=http://127.0.0.1:8931/mcp go test ./internal/toolkit/tools/playwright -run TestIntegration_PlaywrightMCPRealBrowserSmoke -count=1 -v
```

## Optional RAG

Code questions use agentic search by default: grep, immutable repo snapshots,
file reads, and LSP tools. RAG adds semantic recall for architectural or fuzzy
natural-language queries.

```bash
docker compose -f docker-compose.rag.yml up -d
```

```bash
RAG_ENABLED=false
RAG_POSTGRES_DSN=
RAG_EMBEDDING_BASE_URL=http://localhost:11434/v1
RAG_EMBEDDING_API_KEY=ollama
RAG_EMBEDDING_MODEL=nomic-embed-text
RAG_EMBEDDING_DIMS=768
RAG_BACKGROUND_INDEX=false
RAG_INDEX_INTERVAL=5m
```

Any OpenAI-compatible `/v1/embeddings` endpoint works. Examples:

| Provider | Base URL | Model | Dims |
|---|---|---|---:|
| Ollama | `http://localhost:11434/v1` | `nomic-embed-text` | 768 |
| SiliconFlow | `https://api.siliconflow.cn/v1` | `BAAI/bge-m3` | 1024 |
| OpenAI | `https://api.openai.com/v1` | `text-embedding-3-small` | 1536 |

Indexing is incremental: changed files are re-chunked, and only changed chunks
are re-embedded. `rag-search` also queues per-repo indexing on demand.

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
