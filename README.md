# Kepler Agent

Kepler is a Slack assistant with one shared model/tool runtime. It investigates
code and operational evidence, uses connected tools, and requests confirmation
for operator-enabled writes. Shared-channel requests require an explicit mention;
direct messages do not.

Slack is the only maintained conversational product. The noninteractive
`kepler-bench` executable exists for evaluation and automation of benchmark tasks;
there is no browser chat, terminal UI, app-server, or automatic review workflow.
Generic read-only delegation remains available when the agent needs independent
investigations. A PR URL does not select a separate loop or orchestration policy.

## Architecture

Authenticated Slack events enter a durable inbox. Admission persists session
input before acknowledging an event. A bounded dispatcher runs turns separately
from intake, so follow-ups and Stop remain serviceable during long work. The
runtime owns context, tools, approvals, budgets, cancellation and transcript
facts. PostgreSQL owns durable work; Redis wake-ups reduce latency.

Slack delivery receipts record intent and known message identity separately from
model completion and query-oriented run projections. Recovery repairs a known
message and does not repeat an uncertain message creation. Native session status
is reconciled under the same session ownership as execution.

- [Architecture](docs/architecture.md)
- [Development and checks](docs/development.md)
- [Slack behavior](docs/slack.md)
- [Models](docs/models.md), [tools](docs/tools.md), [memory](docs/long-term-memory.md)
- [Benchmark adapter](docs/benchmark.md) and [evaluators](evals/README.md)
- [Safety boundaries](docs/safety.md)

Deployment, images, private configuration, SOPS secrets, incremental migrations
and benchmark execution records belong to
[kepler-agent-deploy](https://github.com/noknov/kepler-agent-deploy).

## Verification

```sh
make doctor repo-check
make check
make test-race
```

`make integration-check` additionally requires isolated PostgreSQL and Redis.
Tests do not send Slack messages or establish live model quality. Use explicit
benchmark runs with pinned source, model and budgets for model-driven evidence.
