# Observability

[Documentation index](README.md) · [Operational troubleshooting](operations.md)

Runs and steps are projections of canonical events. Use them for diagnosis and
cost attribution; use the transcript for conversation replay.

## Local anomaly dashboard

The deployment repository serves the dashboard at `http://127.0.0.1:8082/`.
The HTML shell is public; every diagnostics API requires `OBSERVABILITY_TOKEN`.
Enter the token in the page: it stays only in memory, never in a URL or browser
storage. `/health/dashboard` remains an alias for the page.

`GET /overview?window=24h` aggregates PostgreSQL directly in a read-only,
repeatable-read transaction. Windows must be positive and at most `168h`.
Runs are grouped by **run start time** in `[start,end)`. The page avoids fetching full run
content or N+1 child lists. One run's existing `/runs/<id>` endpoint is read
only after its details are opened.

| Signal | Source and meaning | Limit |
| --- | --- | --- |
| Run statuses, termination, failure rate | PostgreSQL run projection; `error/(completed+error)` | Canceled, pending-user and incomplete are separate outcomes; empty denominator is unknown |
| Run P95 | Recorded durations in the selected run cohort | Full runtime duration; model/tool latency analysis belongs in Langfuse |
| Running records | Oldest 20 projections still marked `running`, across history | Does not prove the process or task is alive; inspect transcript and projection recovery |
| Slack inbox | Current queued/processing/completed/dead-letter counts, oldest queued age, expired claims | Across history, independent of selected run window; no automatic replay |
| Session inputs | Unacknowledged inputs grouped by kind and queued/claimed/expired state | Lease expiration uses stored claim deadline, no guessed stall threshold |
| Worker live metrics | Operator-configured `OBSERVABILITY_WORKER_URL`, `/readyz` and `/metrics` | Process lifetime; reset on restart; latency samples bounded to latest 4096 |
| Tool health | Redis snapshot published by worker, TTL 2 minutes | Code/workspace probes only, not complete provider/auth health; missing cache is unavailable |
| Trace exporter | Configuration, final successful/failed OTLP batches, latest delivery/failure times | An OTLP acknowledgment does not prove downstream Langfuse indexing |

The observability service never probes or publishes worker tool health. It
does not follow worker probe redirects or forward administrator credentials.
Gateway, worker and observer readiness check live PostgreSQL and Redis
connections with a two-second timeout; liveness merely confirms the process.

The deploy migration `010_observability_indexes.sql` adds global run-time
indexes; `011` removes the model-attempt index after moving model aggregation
to Langfuse. Apply through the deployment workflow; services
never execute DDL. Large installations should inspect query plans and use
normal online index procedures before applying an index migration in a
transaction.

## Coverage and remaining gaps

Worker `/metrics` and observer `/metrics` remain **JSON** diagnostics. Standard
Prometheus/OpenMetrics exports are available separately at `/metrics/prometheus`. The observer's legacy `/metrics` summarizes at most the latest 200
runs and must not be used as a time-window metric; use `/overview` instead.
The worker endpoint reports requests/denials, model and tool calls/errors,
agent events, usage/cache/reasoning fields, latency quantiles, inbox/job and
reaction counts, recent errors, and trace export status. Some providers do not
populate every usage field.

Model usage/cache, first text-delta time, model/tool latency and retry/fallback
analysis belong in Langfuse. The local dashboard does not duplicate those
charts or run model/step aggregation queries. Historical local step records
remain available for a failed run's diagnosis.

Services emit JSON logs and deployment limits new containers to three 10 MB
log files. Recorder errors carry ERROR severity. Legacy standard-library log
calls still carry INFO severity, so full log alerting requires migrating those
call sites to structured levels. There is no centralized log retention,
managed Prometheus time-series storage, SLO alert routing, host resource history, or
end-to-end external authorization probe in this deployment. Langfuse supplies
model/agent trace analysis; it does not replace queue and service diagnostics.

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
Langfuse can filter and aggregate child observations. Content capture is opt-in through a profile-injected recorder; it never changes
prompts or canonical transcript data.

Ingestion follows Langfuse's native OTel contract (HTTP/protobuf, Basic Auth,
`x-langfuse-ingestion-version: 4`). Environment and release from
`OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=local,service.version=<revision>`
are copied to every span, alongside session, turn, user and surface. Generation
spans report input/output tokens, finish reason and first text-delta time;
cache and reasoning token buckets are normalized to be mutually exclusive.
Missing usage is explicitly marked rather than recorded as zero. Actual
provider/retry/fallback facts are generation-span events and canonical transcript
events, with filterable request/turn counters and the final actual model route. Tool error results and panics mark tool spans failed even
when there is no returned Go error. Error exception messages contain only a
classification; full provider/tool diagnostics remain in local persistence.

Prefer Langfuse Cloud for a small deployment. Self-hosting is an explicit
optional deployment in `kepler-agent-deploy`, not a prerequisite for the local
anomaly dashboard. Validate three stages independently: configured worker,
OTLP successful delivery, and a trace visible in the target Langfuse project.


## Content and delivery observability

Set `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true` to opt in to
sanitized question/answer, model request/response and tool argument/result
capture. Default is disabled. `OTEL_INSTRUMENTATION_GENAI_CONTENT_MAX_BYTES`
defaults to 32768 bytes per input/output JSON attribute (512–262144 accepted).
Capture bounds traversal and scanning work, preserves valid JSON/UTF-8, marks
truncation, and excludes images, binary, artifact URLs and internal reasoning.
Canonical transcript persistence is unchanged. Recognizable credentials,
sensitive JSON keys and exact secrets from the process environment are masked;
arbitrary confidential prose may still appear. The profile owns this policy,
so agent core does not import deployment config or read secrets.

`run_status` distinguishes completion, cancellation, limits, waiting and failure.
Release comes from explicit `service.version`, build revision or Go VCS metadata;
`langfuse.version`/`prompt_hash` use the exact composed prompt content hash.
Slack adds a parent `slack.reply` chain covering processing and final delivery.
Its user latency metrics start at the original Slack message timestamp when
available and count only acknowledged answer writes, not tool-progress updates.
Delivery outcomes preserve uncertainty, suppression and idempotent skips rather
than treating nil errors as confirmed success. Slack acknowledgment is not proof
that the user read the message. See the [deployment metric guide](https://github.com/noknov/kepler-agent-deploy/blob/main/docs/observability.md)
for fields and dashboard denominators.

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

### Content disclosure boundaries

Content capture remains opt-in. Nested tool results are copied before reasoning,
image payloads, and artifact locations are omitted; canonical evidence is not
modified. The content recorder redacts structured secret fields and common text
credential forms, including quoted JSON fields, and preserves exact JSON numbers.
These controls do not identify every personal or business-sensitive fact. User
and session correlation IDs can still be present in trace attributes even when
message capture is disabled. Set exporter access and retention accordingly;
clearing user memory does not erase previously exported traces.

## Standard operational metrics

Worker `/metrics/prometheus` uses a private official Prometheus client registry.
Existing recorder counters remain authoritative: request/denial, model/tool
calls and errors, reported token fields, canonical events, in-process inbox jobs,
queue depth/capacity, Go/process resource metrics and recorder start time.
Labels contain configured tool/event names and fixed operation/kind/outcome
values, never session/user IDs, tool arguments, prompts or error strings.
Cache/reasoning token fields can overlap input/output; do not sum every `kind`
into billing usage. Counters reset on process restart.

`kepler_agent_operation_duration_seconds` records request/model/tool duration in
seconds with classic and native histograms. Classic buckets support existing
scrapers and include long agent operations through 1,800 seconds. Native histograms use a
160-bucket memory budget per operation. Histograms can be aggregated
across workers; the JSON P95 over the latest 4,096 samples cannot. JSON summaries
retain their existing recent-sample/lifetime-count meaning. Sampling now uses
constant-time ring writes and computes percentiles outside the recorder lock.

Observer `/metrics/prometheus` requires the same admin authorization as
`/overview`. It reads queue-only state in a bounded, read-only repeatable-read
transaction; it does not fetch conversation metadata, recent errors, traces or
run windows. Exports are gauges:

| Metric | Meaning |
| --- | --- |
| `kepler_agent_durable_inbox_events{state}` | Current inbox rows by state |
| `kepler_agent_durable_inbox_expired_claims` | Processing claims whose deadline expired |
| `kepler_agent_durable_inbox_oldest_queued_seconds` | Age of the oldest currently queued event |
| `kepler_agent_durable_pending_inputs{queue}` | Unacknowledged inputs by kind and claim state |

Known queue states emit zero when empty. A failed/unconfigured database read
returns HTTP 503; it never turns into a zero-valued healthy scrape. Replicated
observers describe the **same** database: select one or take a maximum for the
same store; never sum duplicated backlog gauges. Select your deployment/store
labels before aggregation. Worker in-process queue depth is a different signal.

Configure a Prometheus-compatible scraper explicitly; this repository does not
start one or choose a retention/alert/SLO policy. Use `metrics_path:
/metrics/prometheus`. Worker health traffic retains its existing network boundary;
observer scraping uses a bearer `credentials_file` or the existing admin header.
Keep credentials out of scrape URLs and versioned configuration.

Useful queries after selecting the relevant deployment labels:

```promql
sum(rate(kepler_agent_model_errors_total[5m]))
  / sum(rate(kepler_agent_model_calls_total[5m]))

histogram_quantile(0.95,
  sum by (le, operation) (rate(kepler_agent_operation_duration_seconds_bucket[5m])))

max by (state) (kepler_agent_durable_inbox_events)
```

No model traffic leaves the error ratio undefined, not evidence of a perfect
success rate. These are instrumented-operation signals, not task-quality or
end-user-delivery SLOs. Real Slack latency and delivery outcomes still come from
`slack.reply` traces; benchmark pass rates still require their native graders.
References: [histograms](https://prometheus.io/docs/practices/histograms/),
[official Go client](https://github.com/prometheus/client_golang).
