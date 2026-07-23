# Kubernetes deployment

These manifests run `slack-copilot-agent` as a single web/worker deployment first.
The application stores Slack inbox, sessions, runs, and reminders in
PostgreSQL, so it can be scaled horizontally after the database connection
limits and Slack event retry behavior are validated.

## Apply

1. Build and push an image:

   ```bash
   docker build -t ghcr.io/your-org/slack-copilot-agent:latest .
   docker push ghcr.io/your-org/slack-copilot-agent:latest
   ```

2. Create the secret from your environment:

   ```bash
   kubectl create namespace slack-copilot-agent
   kubectl -n slack-copilot-agent create secret generic slack-copilot-agent-secrets \
     --from-literal=SLACK_BOT_TOKEN='xoxb-...' \
     --from-literal=SLACK_SIGNING_SECRET='...' \
     --from-literal=POSTGRES_DSN='postgres://...' \
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
- RAG indexing and workspace auto-fetch should stay disabled in the main
  deployment until they are split into a separate worker or CronJob.
