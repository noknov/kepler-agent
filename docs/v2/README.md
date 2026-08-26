# v2

v2 is the active architecture on `main`. The local CLI and hosted Slack agent
share the canonical model/tool contracts, provider adapters, runtime loop,
context projection, transcript events, compaction, and termination semantics.
Profiles retain product-specific policy, storage, tools, and presentation.

The hosted profile uses PostgreSQL for transcripts, session inputs, run
projections, and per-user connections; Redis carries wakeups and coordination.
It can compose user OAuth connections and MCP-backed tools without creating a
second execution loop. Its read-only `agent-explore` tool runs isolated child
turns from a filtered catalog. The local profile persists the same transcript
event model as JSONL and is also exposed through the stdio JSON-RPC app-server.

For the full bilingual comparison with the retired v1 source, see the
[architecture site](../../architecture-site/README.md).

Start with the [runtime](../runtime.md), [local CLI](../local-cli.md),
[configuration](../configuration.md), [tools](../tools.md), and
[operations](../operations.md) guides. The independent [evaluation
harness](../../evals/README.md) invokes the local product as a black box while
using the same provider path as hosted execution.
