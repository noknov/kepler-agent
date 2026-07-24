# Kubernetes deployment

These manifests run `slack-copilot-agent` as a single web/worker deployment first.
The application stores Slack inbox, sessions, runs, and reminders in
PostgreSQL. Redis backs cross-instance caching, event pub/sub, and active-run
coordination. The starter manifests include single-instance PostgreSQL and
Redis dependencies for local or small test clusters.

## Apply

1. Build and push an image:

   ```bash
   docker build -t ghcr.io/your-org/slack-copilot-agent:latest .
   docker push ghcr.io/your-org/slack-copilot-agent:latest
   ```

2. Create the secret from your environment. `REDIS_URL` should point at the
   Redis service in this namespace, unless you use an external Redis:

   ```bash
   kubectl create namespace slack-copilot-agent
   kubectl -n slack-copilot-agent create secret generic slack-copilot-agent-secrets \
     --from-literal=SLACK_BOT_TOKEN='xoxb-...' \
     --from-literal=SLACK_SIGNING_SECRET='...' \
     --from-literal=POSTGRES_DSN='postgres://...' \
     --from-literal=REDIS_URL='redis://slack-copilot-redis:6379/0' \
     --from-literal=MIMO_API_KEY='...'
   ```

3. Update `deployment.yaml` with your image and provider-specific variables,
   then apply:

   ```bash
   kubectl apply -f deploy/k8s/
   ```

## Operational notes

- `/livez` is a liveness probe. `/readyz` fails during drain so Kubernetes
  stops routing traffic before SIGTERM finishes.
- `preStop` calls local `/drain`, making `/readyz` fail, then sleeps briefly so
  endpoints can remove this pod before process shutdown begins.
- `terminationGracePeriodSeconds` should be longer than ordinary agent turns.
  Long tool runs are retried through the durable Slack inbox after the inbox
  lease expires.
- For multiple replicas, keep `POSTGRES_MAX_CONNS` low enough that
  `replicas * per-store pools` stays below the database limit.
- For production, replace the starter Redis deployment with managed Redis or
  another durable Redis-compatible service if you need persistence guarantees.
- Workspace auto-fetch should stay disabled in the main deployment until its
  network and freshness trade-offs are understood for the cluster.
