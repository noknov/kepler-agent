# Documentation

Choose a task below. These guides describe the current source tree and its
public operating contracts. Environment-specific secrets and release procedures
are intentionally out of scope.

## Use and operate

| I want to… | Read |
| --- | --- |
| Set up Slack or request a PR review | [Hosted Slack](slack.md) |
| Select configuration and model settings | [Configuration](configuration.md), [models](models.md) |
| Diagnose readiness, stalled work, or shutdown | [Operations](operations.md) |
| Configure tracing and cost attribution | [Observability](observability.md) |
| Understand permissions and recovery limits | [Safety and limitations](safety.md) |

## Understand and extend

Read [architecture](architecture.md) first, then follow the area you change:

| Area | Guide |
| --- | --- |
| Detailed execution contracts, identity, failure windows, privacy, and capacity (中文) | [Architecture deep dive](architecture-deep-dive.md) |
| 2026 agent comparison, audit findings, and measurable next steps (中文) | [October audit](agent-audit-2026-10.md) |
| Turn lifecycle, context, events, and delegation | [Runtime](runtime.md) |
| 任务状态、上下文策略与原生续接 | [持续执行契约](agent-continuity.md) |
| Tools, discovery, integration setup | [Tools](tools.md) |
| 逐尝试记账、公平调度与子任务证据 | [执行资源](agent-execution-resources.md) |
| Prompts, rules, skills, private overlays | [Prompts](prompts.md) |
| Cross-session user memory design and evaluation | [Long-term memory](long-term-memory.md) |
| Memory 检索、时效与配额 | [记忆生命周期](memory-retrieval.md) |
| Agent maintenance, design cost, and review | [Maintenance workflow](maintaining.md), [execution plans](plans/README.md), [backlog](maintenance-backlog.md) |
| Source setup and verification | [Development](development.md) |
| Current design decisions and unfinished work | [Harness design status](harness-alignment.md) |
| Chinese walkthrough with diagrams and source links | [Architecture site](../architecture-site/README.md) |
| Generated app-server wire contract | [JSON Schema](app-server.schema.json) |

## Measure quality

[Evaluation](../evals/README.md) documents the evaluator code, Harbor and
AppWorld adapters, and wiring checks (`make eval-check`). Concrete benchmark
runs, packaging, deployment, and result records belong in the sibling
`kepler-agent-deploy` repository. A dry-run is not a model quality result; a
CLI benchmark does not validate Slack/Web delivery or hosted permissions.

## Maintain these documents

- Put each fact in one owning guide; link to it from other pages.
- Keep root READMEs short: product scope, entry points, and repository ownership.
- State the command's working directory, prerequisites, output, and verification.
- Use generic placeholders for credentials, domains, and organization details.
- Describe behavior supported by code separately from planned work and known limits.
- Update source links and examples with the code change that makes them stale.
- Keep generated protocol files generated; do not edit them to fix documentation.
- Keep the Chinese architecture site focused on explanations; link operational
  recipes to these guides rather than maintaining another set of commands.

The old [v2 entry](v2/README.md) remains a compatibility link. Version-named
folders should not be used as the main entry for current architecture.
