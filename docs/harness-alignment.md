# Harness design status

This document tracks design direction. It is not a feature checklist or a
claim that another product's implementation has been reproduced. Current
behavior belongs in [architecture](architecture.md) and [runtime](runtime.md).

## Decisions to retain

- Keep one transport-neutral loop and canonical append-only transcript.
- Compose product policy, storage, credentials, and delivery in profiles/surfaces.
- Derive client views and observability from events rather than adding competing
  execution-state stores.
- Extend tool, skill, provider, and MCP interfaces without handing ownership of
  persistence or authorization to arbitrary extensions.
- Keep hosted authorization operator-controlled and local execution sandboxed.
- Keep public benchmark grading in Harbor and product-specific tests separate.

## Implementation and remaining validation

| Track | Implementation entry | Remaining work |
| --- | --- | --- |
| Trace and trajectory | Runtime events, run projection, `thread/trajectory` | Verify trace joins, redaction and replay; record enough versions to interpret historical facts |
| App-server admission | Active-turn limit and retryable `-32001` | Verify client retries, reconnects and overload behavior; add transport bounds before network exposure |
| Protocol compatibility | Initialize handshake, Go registry, generated JSON Schema/TypeScript | Verify compatibility and reconnect behavior, not just generated-file equality |
| Evaluation | Local runner/report/gate, Harbor adapter, and AppWorld MCP runner | Validate gate inputs, freeze experiment identity, connect completed results to release decisions |
| Tool safety | Descriptors, effects, profile policy and sandbox | Cross-tool conformance, dynamic MCP classification, failure-path tests |
| Delegation | Bounded leaf tasks, child transcripts, circuit breaker, and parent usage rollup | Measure review accuracy, duplication, cost, cancellation, and aggregate resource use |

An implementation entry means code exists, not that the feature is complete
or proven under every failure condition. In particular, a gate script is not
a CI release gate until the release workflow requires a compatible completed
result. See [evaluation](../evals/README.md).

## How to adopt an external idea

Record the problem, the concrete mechanism worth adopting, Kepler's boundary
conditions, and a testable success criterion. Compare behavior and evidence;
do not infer stronger durability or safety from architecture labels alone.
The [Chinese architecture site](../architecture-site/README.md) explains the
current mechanisms; dated comparisons and audits should retain their own
source revisions rather than becoming timeless product claims.

## 2026-10-02: recovery, delivery, and long-session costs

The following primary references informed this iteration (retrieved on
2026-10-02; their default branches and hosted documentation can change):

- [Codex App Server](https://learn.chatgpt.com/docs/app-server): explicit
  thread/turn/item lifecycles and authoritative completed items. Kepler's CLI
  now prefers durable final content, reserves a turn before its start request
  resolves, and rejects pending requests immediately on transport closure.
- [DeepSeek Harness core](https://github.com/deepseek-ai/deepseek-harness/blob/master/docs/subsystems/core.md):
  event-derived history and explicit ownership of agent lifetimes. Kepler keeps
  its existing Go dependency injection; private leaf sessions inherit the
  parent's cancellation context without acquiring a second session lease.
- [pi agent core](https://github.com/badlogic/pi-mono/blob/main/packages/agent/README.md):
  separation between model context and presentation, with distinct steering and
  follow-up delivery. Kepler keeps its existing routing choice, allows typing
  while busy, and serializes follow-ups across the start-acknowledgement window.

These are mechanisms adapted to Kepler's contracts, not a migration to another
harness or a claim of feature parity.

### Implemented invariants

| Boundary | Behavior | Regression evidence |
| --- | --- | --- |
| Model resilience | An open primary circuit can still use a healthy fallback; committed output remains non-retryable | `agent/model/recovery_regression_test.go` and existing committed-stream tests |
| Turn admission/replay | Start and initial input recover independently using stable IDs; retry after an ambiguous input commit does not duplicate the user message | `agent/runtime/recovery_regression_test.go` |
| Turn termination | Cancellation is terminal for queue acknowledgement; cancellation and completion update app-server phase under the same lock | Web/app-server recovery regressions and race suite |
| Context and cleanup | Every profile reserves configured output tokens; impossible budgets fail before model dispatch; terminal persistence has a default 10-second deadline | Runtime recovery regressions |
| Child ownership | A fresh private child does not consume its parent's bounded distributed-lease pool | Eight saturated parents complete eight child model calls in `agent/delegation/lease_test.go` |
| Local transcript | Offset/ID indexes avoid re-parsing historical messages per append; file locks preserve cross-process sequencing, and each write still syncs | Local store tests cover external append, replacement, truncation, duplicate IDs, cancellation, corruption, and concurrent writers |
| PostgreSQL JSON | One encoder replaces actual NUL escapes while preserving literal escapes and full integer precision | `infra/postgresjson` unit tests and fuzz seeds |
| Slack delivery | One scheduled/in-flight flush coalesces deltas; model callbacks do no network I/O; completion drains before final delivery | Blocked-network test asserts all 100 additional deltas arrive once, with at most one append |
| CLI interaction | Failed submissions preserve the draft; queued start failures pause without dropping later input; stale completions do not release another turn | Seven transport/component/hook regressions in `apps/cli/tests` |
| Web selection | Snapshots are bound to request and selection identity, and cannot replace newer live events | Three selection/recovery regressions in `surfaces/web/tests` |

Paths in the evidence column are relative to `packages/` unless otherwise
specified. No schema migration or new production dependency is required.

### Local append and search microbenchmarks

The local JSONL store uses offset/ID indexes to avoid re-parsing historical
messages per append while keeping file locks and `fsync` enabled. The cache
retains at most 64 session indexes, containing IDs and file offsets rather than
message bodies. Shared prompt layers (`system.md` + `general.md`) are kept
compact, prompt catalog publication is atomic under a single lock, and query
tokenization and regex compilation stay out of inner loops.

Run the Go storage and tool microbenchmarks from the source root with:

```sh
make bench
```

Raw benchmark result records, public dataset executions, packaging, and
deployment artifacts belong in the sibling `kepler-agent-deploy` repository;
this source repository retains only the benchmark code and harness contracts.

## 2026-10-07: context masking, lexical tool ranking, and surface parity

This iteration closed the remaining architectural gaps across the shared
runtime, provider catalog, tool discovery, MCP routing, and local/hosted
surfaces:

| Area | Implemented contract | Verification entry |
| --- | --- | --- |
| Two-stage observation masking | `BoundedProjector` folds historical `ToolResult` text and JSON blocks outside the most recent two complete exchanges into UTF-8 head/tail previews with original-evidence references before dropping whole turn groups or invoking LLM compaction | `agent/runtime/context_test.go` |
| Reasoning retention and prompt caching | Assistant prose accompanying tool calls is retained as text with a commentary phase, distinct from native reasoning and final-answer text; `prompt.Build` injects `---DYNAMIC_CONTEXT_BELOW---` between static and dynamic layers, enabling Anthropic incremental cache breakpoints | `agent/prompt/prompt_test.go`, `providers/providers_test.go` |
| Sub-agent circuit breaking and usage rollup | `agent-explore` enables `CircuitBreaker` (`MaxIdenticalFailedAttempts: 3`) for child turns and rolls child `model.Usage` up into the parent turn's `TurnResult.Usage`; `CircuitWarning` appends pre-trip self-correction hints to repeated tool errors | `agent/delegation/explore_test.go`, `agent/runtime/runner_test.go` |
| Provider catalog and shared circuit state | `providers/catalog.go` declares capabilities for ChatGPT plan slugs (`gpt-5.4`, `gpt-5.6-luna`, `gpt-6-luna`, `gpt-6.1-sol`, etc.) and API providers; `hosted.NewProfile` shares one `model.CircuitBreakers` registry across primary, router, compactor, and explorer clients | `providers/providers_test.go`, `profiles/hosted/profile_test.go` |
| Identifier-aware BM25 tool search | `agent/tool/search.go` replaces static bilingual synonym tables with field-weighted BM25 ranking and camelCase/snake_case sub-token indexing; `diagnostics` pseudo-tool is replaced by the `incident-diagnostics` skill | `agent/tool/search_test.go` |
| MCP router session caching and tool contracts | `tools/mcp` caches initialized MCP router sessions and tool definitions (5-minute TTL, invalidated on transport error), reducing per-call round trips from three to one; `luckin-create_order` enforces turn-scoped preview fingerprint verification; `git-search_ref` and `repo-search` default to literal `-F` matching | `tools/luckin/tools_test.go`, `tools/git` and `tools/mcp` tests |
| Local harness unification and fuzzy edit | `tools/local/bootstrap.go` unifies skill, MCP, and prompt assembly across `app-server` and headless CLI, registers the durable `memory` tool, persists `agent-explore` events to the session store, and adds whitespace-tolerant fuzzy matching to `edit` | `tools/local/tools_test.go`, `tools/edit/tools_test.go` |
| Web and Slack lifecycle alignment | Web enforces process-wide `MaxActiveTurns` (32), maps `TurnFailed` through `failure.PublicMessage`, collapses duplicate `plan` events, and renders live tool/plan progress in activity cards; Slack bypasses the secondary LLM router when `workflows.Registry.HasPromptCandidate` finds no workflow trigger | `surfaces/web` and `surfaces/slack/agent/service_test.go` |

## 2026-10-22: evidence-backed architecture review

The [October audit](agent-audit-2026-10.md) records the reviewed source revision,
reproducible context/ownership/credential/privacy fixes, official comparison
sources, and remaining design gaps. The [architecture deep dive](architecture-deep-dive.md)
explains the current ownership, recovery, context, artifact, memory, and disclosure
contracts. Native provider compaction, typed task state, semantic recall, and
tenant-fair scheduling remain proposals rather than completed capabilities.

## Slack quality criteria

Reviewed against the main runtime on 2026-10-23. Slack is the primary product
surface; a coding benchmark alone cannot measure its quality. These criteria
compare observable behavior, not feature counts or inferred implementation
quality. This review does not establish parity with, or superiority over, another
agent.

| Dimension | Existing mechanism | Required evidence / remaining gap |
| --- | --- | --- |
| Interaction | Explicit channel mentions, direct messages, typed plan events, native session lifecycle and Stop | Slow/erroring Slack must not stall the model; final state must not be lost; measure Stop-to-idle latency under load |
| Execution reliability | Durable inbox/session inputs, turn IDs, owner-checked claims, uncertain-effect handling | Fault-inject worker restarts, lost replies and concurrent controls; never turn an unknown side effect into an automatic repeat |
| Context and tools | Canonical transcript, checkpoint coverage, task-state revisions, deferred tool activation and evidence reads | Test long Slack threads with corrections, attachments and large tool results; measure constraint retention and evidence recall as well as tokens |
| Performance | Shared model governor, attempt receipts, prompt/context versioning | Compare queue wait, time to first delivered answer and total latency at fixed concurrency/model; a faster single CLI task is insufficient |
| Observability | `slack.reply` spans link user timestamps, delivery results and runtime work | Report p50/p95 by model/surface and separate provider failure, tool failure, delivery uncertainty and failed grading; do not count unknown delivery as success |
| Quality evaluation | Runtime/product regressions plus official Terminal Bench and AppWorld graders | Require the Slack regression gate; separately run versioned, representative live-model tasks with fixed budgets and independent expected outcomes |

`make eval-slack` provides the deterministic product-contract layer, including
real PostgreSQL/Redis control paths. It does not contact Slack or prove task
quality. Before claiming an experience improvement, run a fixed Slack scenario
set against the deployed version: multi-turn investigation with a correction,
image/file interpretation, a permission failure followed by reconnection,
concurrent users, cancellation during tool work, and resumption after a worker
restart. Record scenario/model/prompt-policy versions, outcome evidence,
attempts/tokens and delivery latency. Expected outcomes must be independent of
the agent answer; do not accept an LLM's own declaration of success as a grade.

Start with these failure paths before adding new orchestration or prompt rules.
Keep operational facts and user content separate: the UI projects lifecycle and
plan events, while canonical user/tool evidence drives the model. Keep a
one-to-one relationship between a confirmed side effect and its execution
receipt. These invariants are testable without guessing intent from text.

Primary references consulted for this review:

- [Slack agent sessions](https://docs.slack.dev/ai/agent-sessions/): lifecycle
  states and native stopping are platform contracts; loading text is not a
  substitute for a task plan.
- [Slack rate limits](https://docs.slack.dev/apis/web-api/rate-limits/): the
  Retry-After window applies per method/workspace, rather than per conversation.
- [pi agent core](https://github.com/earendil-works/pi/blob/main/packages/agent/README.md):
  typed streaming events, separate steering/follow-up queues, and explicit
  turn-finalization hooks are useful boundaries. They do not establish durable
  cross-worker delivery by themselves.

For Codex's thread/turn/item contract, retain the App Server reference above;
Kepler's Slack adapter should preserve the same explicit lifecycle boundaries
without importing CLI-specific interaction into Slack.

## 2026-10-26：本轮能力复核与验证边界

以本轮 PR #23–#33 的独立工程变更为复核范围。结论是核心 harness 的分层、
执行和恢复机制已覆盖所讨论的大部分能力；现有证据不能证明任务效果、成本或
生产可靠性已追平或超过 Codex / Claude Code。协议实现、可选配置和实测效果须分开判断。

| 方面 | 当前实现与本轮交付 | 验证边界 / 有意义的后续条件 |
| --- | --- | --- |
| ReAct / Function Calling | [统一 runtime](runtime.md) 运行模型→工具→观察循环，保留类型化调用、工具效果/策略与错误恢复 | 各工具仍负责业务参数和权限校验；工具 schema 不授予能力 |
| Plan-and-Execute | [plan 与 task_state](agent-continuity.md) 分开呈现和持久任务事实，来源/修订/证据检查，压缩保留约束 | 自适应执行循环，不伪装为独立的通用工作流 DAG 引擎 |
| Structured Outputs | 原生 OpenAI Chat/Responses、Anthropic 映射；完整本地 schema 校验，显式 native/validated 模式；远程 native receipt（#23/#30） | 提供方支持范围不同；native 不支持时明确失败，无隐式降级；真实生成质量未测 |
| MCP | [工具边界](tools.md) 的版本协商、精确 RPC ID、保持连接的 SSE、发现容量/循环限制（#24） | 已实现 2025 年三版工具传输子集；不宣称覆盖当前所有 MCP 扩展/新版本 |
| A2A | [通用远程适配器](a2a.md) 及官方 SDK executor，共用 TaskSpec/Runner；send/get/cancel/continuation、调用归属、认证/有界结果（#25） | SDK 0.3 binding；不是完整 A2A1.0 联邦平台，未自动开放公网 inbound 服务 |
| Multi-Agent | 通用本地 delegation、子任务证据回读与父预算；[code review](../packages/workflows/codereview/prompt.go) 是其上的业务编排 | 原有多 reviewer 是内部委托，不等于 A2A。配置了 remote peer 才有跨系统 A2A；code review 仍使用只读本地 leaf，未自动外发私有 PR |
| RAG / Hybrid / Reranker | [显式 memory 检索](memory-retrieval.md) 融合全文/trigram/可选精确 pgvector 候选；可选模型 reranker（#28/#29） | 针对有配额的用户 facts，不是任意文档摄取平台。受控向量证明零词法重叠链路，真实模型相关性尚未测；默认关闭 embedding/rerank |
| Context Engineering | [continuity](agent-continuity.md) 包含版本策略、完整请求预算、按需证据、独立任务状态与可选 native continuation | 精确 tokenizer 可注入；默认估算、摘要/native 组合的实际质量与费用仍需同任务实验 |
| Memory | 来源版本、reset generation、显式过期/配额、模型 space/claim CAS 与删除生命周期；配置/启用时 schema 检查（#29/#32） | 不自动抽取/合并/删除用户事实；模型升级要显式运维切换，真实隐私/保留政策覆盖更多外部系统 |
| Checkpoint / Resume | append-only transcript、独立输入提交、完整 turn 会计恢复、任务事实在 compaction 后保留 | 本地/hosted 恢复有回归；任意外部副作用不具备端到端 exactly-once |
| Human-in-the-Loop | [审批](safety.md) 绑定 actor、原调用/descriptor 和 server policy；暂停后恢复 | 人工批准不能扩大 host 能力；远端或已发生的副作用仍要核对事实 |
| 幂等 / 故障恢复 | durable queue owner/lease、稳定提交 ID、未知效果不盲重放；Web 临时存储失败保留输入、满载不消耗重试（#31），有界 drain/finalization（#33） | 真实 PG/Redis + 故障回归不是整个集群故障演练；跨系统未知结果需要 reconciliation |
| 资源 / 性能 | [逐次 usage receipts](agent-execution-resources.md)、父预算、公平 admission、embedding 独立记账；指标 ring O(1)、精确向量查询（#27/#29） | Governor 是进程内，非集群配额；本地微基准不能推出生产 P95 或任务成功率 |
| PostgreSQL / Redis / OpenTelemetry | durable 数据与唤醒职责分开；[标准 metrics](observability.md)、真实队列 gauges、OTLP；deploy018 schema parity | 没有托管 failover、集群压测或现成 SLO 告警后端；没有生产 HA 认证 |
| Docker / K8s / 框架 | Docker 构建与本地运维；保留 canonical Go runtime 的状态/恢复接口 | 当前部署是本地替换，有中断窗口；K8s legacy 不是经验证生产平台。不为清单再引入 SpringAI/LangGraph/Temporal 形成第二执行状态源 |
| Evaluation | 完整非 dry-run artifacts / identity / raw records gate（#26），deploy4 修正 Harbor gateway 启动契约 | wiring、单元回归和真实模型分数分开。缺当前登录、Docker/数据集时，没有可发布的同模型竞品对照成绩 |

本轮本地证据包括 `make check`、相关 race suites、真实 PostgreSQL/Redis 的
`make integration-check`、受控向量/缓存生命周期回归、增量迁移的新装/升级/重放/schema parity，
以及部署脚本 16 个回归。没有执行生产部署。源码/部署 PR 是独立的可审查交付，不代表已应用 migration018。
本地 SQL 与 recorder 微基准的范围见对应 owning guides；原始 SQL 观察值保存在
[部署仓库](https://github.com/noknov/kepler-agent-deploy/blob/ce83b963c3524a98b4d3a77bf2f2c0224201895b/docs/measurements/memory-exact-2026-10-26.json)，
其中未冻结的数组原型明确仅作探索证据。

对公开设计的取舍：

- [Codex App Server](https://openai.com/index/unlocking-the-codex-harness/)：借鉴同一 harness 支撑多个 surface 和明确 thread/turn/item 生命周期；保留 Kepler 的 canonical events。
- [Claude Code subagents](https://code.claude.com/docs/en/sub-agents)：借鉴独立上下文、受限能力和任务结果返回；不把多 agent 数量当质量指标。
- [DeepSeek Harness architecture](https://github.com/deepseek-ai/deepseek-harness/blob/master/docs/architecture.md)：借鉴 profile composition 与事件/生命周期归属；保留 Go 的显式依赖边界，不复制整个插件框架。

下一轮应先在固定模型、配置、任务镜像和 grader 下测任务成功率/费用/延迟，再决定
写入子代理的独立 workspace/冲突协议、更大文档 corpus 或分布式 admission 是否有净收益。
生产 HA 需明确目标集群、外部数据服务 failover、恢复目标和故障演练证据；新增一组 YAML 不能替代这些条件。

## 2026-10-27：本轮深化与 Slack 功能方向

延续上一节的完整能力矩阵，本轮聚焦能复现的边界错误和可组合的新能力。
PR35–43 已通过各自最新提交的 CI 后合并；Slack RTS 搜索已完成本地验证。没有增加执行框架、数据库表或生产依赖。

| 方面 | 本轮增强 / 保留的边界 | 仍需证明的结果 |
| --- | --- | --- |
| ReAct、Function Calling、Plan-and-Execute | #41 在 assistant 消息进入可执行 transcript 前校验整批调用 ID；同一 ID 绑定原工具/精确 JSON 参数；复用结果不重复用量和计划/任务状态 | 真实模型是否更少陷入无效调用，需要固定任务集验证；不修补或猜测缺失 ID |
| Structured Outputs、MCP | 保留上一轮原生输出/完整校验、RPC identity 和有界 SSE；本轮没有复制第二套协议实现 | 提供方/协议版本兼容范围和真实效果仍以各 owning guide 为准 |
| A2A、Multi-Agent | #38 把实际凭据身份纳入 peer/task/审批绑定，运行中轮换在出网前失败；保留通用 delegation，code review 是其上编排 | 旧凭据未绑定的任务先核对；远程 peer 不是默认 code-review 数据出口，暂无跨产品质量对照 |
| Context Engineering、RAG/Hybrid/Reranker、Memory | #35 传递消息时的 Slack viewing 引用；#43 通过用户 RTS 检索相关讨论及来源；沿用已有任务状态、混合 memory、可选 reranker | Slack 语义检索由工作区能力决定；未用真实模型证明相关性、召回率、费用或长期约束保持率 |
| Checkpoint/Resume、HITL、幂等恢复 | #37 容量包含持久收尾；#40 完整有界 JSON 回执；#41 拒绝改绑旧调用；真实 JSONB 恢复测试覆盖数字归一化、精度和不重复写/计费 | 跨系统未知副作用仍需 reconciliation；不能宣称 exactly-once 或任意任务自动恢复 |
| 性能、PostgreSQL/Redis、OpenTelemetry | #39/#42 统一 cursor 进展检查，减少超量分页并拒绝循环；保留 governor、队列指标和独立 embedding 记账 | 进程内限流不是集群配额；真实 PG/Redis race 测试不能替代生产 P95、HA/SLO 或故障演练 |
| Slack 产品体验 | #36 原生建议入口，复用同一 runtime；RTS 搜索只按需取一页，保留 source/context，不复制整个 workspace | 需安装 scopes、重新连接和真实工作区验收；没有发送真实 Slack 消息 |
| Spring AI/LangGraph、Temporal、Docker/K8s | 保留 canonical Go runtime 与现有部署；本轮无框架/集群模板堆叠 | 仍未提供经验证的生产 HA；引入外部 workflow engine 必须先解决明确的独立调度/持久化需求 |

合并后的 `make check`、runtime/delegation/A2A/Web/worker/memory race suites、
隔离 PostgreSQL/Redis 的 `make integration-check` 和 JSONB 调用恢复回归提供工程证据。
没有真实 Slack/模型质量实验或生产部署。本轮无需补数据库迁移；上一轮 deploy018
的应用顺序和兼容条件仍须遵守。

### 值得组合的 Slack 能力

以下是基于官方 API 和现有边界的产品设计判断，不是已验证的用户效果：

| 场景 | 可复用的能力 | 验收与维护条件 |
| --- | --- | --- |
| “总结我正在看的频道的决策、分歧和原话来源” | 消息时 viewing 引用 + 已授权 conversation read + 原生建议入口 | 切换频道不串上下文；引用逐条可追溯；没有用户连接时不扩大 bot 读取范围 |
| “当时为什么这么改？找讨论并对照这次 PR” | RTS + 选定 thread read + GitHub 只读工具/现有通用 delegation | 同时保留 Slack permalink 与不可变 PR head；区分事实、推断和未决事项；比较调用数、来源准确率及耗时 |
| “把调查结论整理成 Canvas” | 搜索证据 + 已有 `slack-create_canvas`，需用户明确要求和 operator write allowlist | 明确可见范围；带来源；未知创建结果不盲重试。不伪装成已经实现的持续同步文档 |
| 将确认后的行动项写入 Slack Lists | 官方 Lists API；当前尚未实现 | 先定义字段映射、目标/actor、重复提交与更新冲突，再接已有写策略和回执；不从模糊发言自动分配任务 |
| 在自有任务链接旁展示进度与证据 | Work Objects / link unfurl；当前尚未实现 | 仅 app 拥有的对象和授权读者；从 canonical events 投影；不再建任务状态库，不因贴链接自动触发昂贵调查 |

优先用前两种只读组合验证真实收益，再决定是否增加 Lists 或 Work Objects 写入面。
这避免为每一种需求单独硬编码一个 workflow，也避免把更多 agent 数量当作质量提升。

参考：[Codex as a platform](https://developers.openai.com/blog/codex-as-a-platform)、
[Slack context management](https://docs.slack.dev/ai/agent-context-management/)、
[RTS usage](https://docs.slack.dev/apis/web-api/real-time-search-api/)、
[Canvas](https://docs.slack.dev/surfaces/canvases/)、[Lists](https://docs.slack.dev/surfaces/lists/)、
[Work Objects / unfurl](https://docs.slack.dev/messaging/unfurling-links-in-messages/)。
