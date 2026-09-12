# Observability

[Documentation index](README.md) · [Operational troubleshooting](operations.md)

Runs and steps are projections of canonical events. Use them for diagnosis and
cost attribution; use the transcript for conversation replay.

## OpenTelemetry

The gateway, worker, observability service, and local CLI support standard
OTLP/HTTP trace export through the OpenTelemetry environment contract:

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318
OTEL_SERVICE_NAME=kepler-agent-worker
```

The shared runtime emits nested `agent.turn`, `model.generate`, and
`tool.execute` spans. Session/turn IDs and tool/model names are attributes;
prompt text, tool arguments, model output, credentials, and Slack message text
are never attached. With no OTLP endpoint configured, tracing is a no-op.

### Langfuse

Langfuse is an optional OTLP backend for all Kepler surfaces, not a Slack-only
integration. When a standard `OTEL_EXPORTER_OTLP_ENDPOINT` is present it takes
precedence, so an OpenTelemetry Collector can fan traces out to Langfuse and
other backends. For direct Langfuse export, configure the same variables for
each process that should report traces (normally the worker, local CLI, and
interactive app server):

```bash
LANGFUSE_BASE_URL=https://cloud.langfuse.com # or https://<self-hosted-langfuse>
LANGFUSE_PUBLIC_KEY=pk-lf-...
LANGFUSE_SECRET_KEY=sk-lf-...
```

The runtime labels the root turn as a Langfuse `agent`, model calls as
`generation`, and tools as `tool`. Every span receives the session ID, user ID
when known, and the ingress surface (`slack`, `web`, `cli`, or `appserver`) so
Langfuse can filter and aggregate child observations. It deliberately does not
send prompt or result content.


## Cost tracking

Set provider rates explicitly. Unset rates are recorded as zero; a zero estimate
is not evidence that inference was free.

```sh
LLM_INPUT_COST_PER_MTOK=0
LLM_OUTPUT_COST_PER_MTOK=0
LLM_CACHE_READ_COST_PER_MTOK=0
LLM_CACHE_CREATION_COST_PER_MTOK=0
```

Compare estimates with provider billing, including retries and child agents.
For HTTP authentication and endpoint ownership, see [operations](operations.md).
