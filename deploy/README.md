# Kubernetes Deployment

Shared cluster resources live in `deploy/shared/k8s/`. Service-owned
Kubernetes manifests live beside the service:

| Service | Manifests |
|---|---|
| Gateway | `gateway/deploy/k8s/` |
| Worker | `worker/deploy/k8s/` |
| Observability | `observability/deploy/k8s/` |

## Apply

Build and push an image:

```bash
docker build -t ghcr.io/your-org/slack-copilot-agent:latest .
docker push ghcr.io/your-org/slack-copilot-agent:latest
```

Create service-specific secrets:

```bash
kubectl create namespace slack-copilot-agent
kubectl -n slack-copilot-agent create secret generic slack-copilot-gateway-secrets \
  --from-literal=SLACK_SIGNING_SECRET='...' \
  --from-literal=POSTGRES_DSN='postgres://slack_copilot:slack_copilot@slack-copilot-postgres:5432/slack_copilot?sslmode=disable' \
  --from-literal=REDIS_URL='redis://slack-copilot-redis:6379/0'

kubectl -n slack-copilot-agent create secret generic slack-copilot-worker-secrets \
  --from-literal=SLACK_BOT_TOKEN='xoxb-...' \
  --from-literal=SLACK_SIGNING_SECRET='...' \
  --from-literal=ALLOWED_SLACK_USERS='U11111111,U22222222' \
  --from-literal=POSTGRES_DSN='postgres://slack_copilot:slack_copilot@slack-copilot-postgres:5432/slack_copilot?sslmode=disable' \
  --from-literal=REDIS_URL='redis://slack-copilot-redis:6379/0' \
  --from-literal=MIMO_API_KEY='...'

kubectl -n slack-copilot-agent create secret generic slack-copilot-observability-secrets \
  --from-literal=POSTGRES_DSN='postgres://slack_copilot:slack_copilot@slack-copilot-postgres:5432/slack_copilot?sslmode=disable' \
  --from-literal=REDIS_URL='redis://slack-copilot-redis:6379/0' \
  --from-literal=OBSERVABILITY_TOKEN='...'
```

Apply shared resources and services:

```bash
kubectl apply -f deploy/shared/k8s/
kubectl apply -f gateway/deploy/k8s/
kubectl apply -f worker/deploy/k8s/
kubectl apply -f observability/deploy/k8s/
```

## Local Compose

Local-only dependency stacks live under `deploy/local/compose/`:

```bash
docker compose -f deploy/local/compose/search.yml up -d
```

## Notes

- Slack Events and Interactions route to `slack-copilot-gateway`.
- Operational dashboards and `/runs` route to `slack-copilot-observability`.
- Worker has no Service because it only consumes the durable inbox.
- Replace the starter PostgreSQL and Redis manifests with managed services for
  production.
- Keep non-sensitive environment config in reviewed ConfigMap, Helm values, or
  Kustomize overlay files. Keep credentials and connection strings containing
  passwords out of git and inject them through Kubernetes Secret or an external
  secret manager during CI/CD.
