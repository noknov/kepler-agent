# v2

v2 is the active architecture on `main`. The local CLI and hosted Slack agent
share the canonical model/tool contracts, provider adapters, runtime loop,
context projection, transcript events, retries, compaction, and termination
semantics. Profiles retain only product-specific policy, storage, tools, and
presentation.

Start with the [runtime](../runtime.md), [local CLI](../local-cli.md),
[configuration](../configuration.md), [tools](../tools.md), and
[operations](../operations.md) guides. The independent [evaluation
harness](../../evals/README.md) invokes the local product as a black box while
using the same provider path as hosted execution.
