# Documentation

Choose a task below. These guides describe the current source tree; deployment
commands and private configuration belong to `kepler-agent-deploy`.

## Use and operate

| I want to… | Read |
| --- | --- |
| Build, log in, or run a local coding session | [Local CLI](local-cli.md) |
| Set up Slack or request a PR review | [Hosted Slack](slack.md) |
| Enable browser conversations | [Hosted Web](web.md) |
| Select configuration and model settings | [Configuration](configuration.md), [models](models.md) |
| Diagnose readiness, stalled work, or shutdown | [Operations](operations.md) |
| Configure tracing and cost attribution | [Observability](observability.md) |
| Understand permissions and recovery limits | [Safety and limitations](safety.md) |
| Build images, migrate data, or restart services | [Deploy repository](https://github.com/noknov/kepler-agent-deploy) |

## Understand and extend

Read [architecture](architecture.md) first, then follow the area you change:

| Area | Guide |
| --- | --- |
| Turn lifecycle, context, events, and delegation | [Runtime](runtime.md) |
| Tools, discovery, integration setup | [Tools](tools.md) |
| Prompts, rules, skills, private overlays | [Prompts](prompts.md) |
| Source setup and verification | [Development](development.md) |
| Current design decisions and unfinished work | [Harness design status](harness-alignment.md) |
| Chinese walkthrough with diagrams and source links | [Architecture site](../architecture-site/README.md) |
| Generated app-server wire contract | [JSON Schema](app-server.schema.json) |

## Measure quality

[Evaluation](../evals/README.md) separates evaluator smoke tests, Harbor public
benchmarks, and product-specific regression tests. A dry-run is not a model
quality result; a CLI benchmark does not validate Slack/Web delivery or hosted
permissions.

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
