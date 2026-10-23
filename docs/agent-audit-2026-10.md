# 2026-10-22 Agent 深度审查

审查基线：`d79c25c8cbc407dc22b658508eac464d42d98e4a`。本文件将本轮可复现修复、
残余设计差距和实验目标分开。它不是安全认证，也不声称 Kepler 已超过任何竞品。
当前执行契约见[架构详解](architecture-deep-dive.md)。

本轮按独立 PR 交付：[审查修复 #12](https://github.com/noknov/kepler-agent/pull/12)、[上下文与任务连续性 #13](https://github.com/noknov/kepler-agent/pull/13)、[执行资源与子任务证据 #14](https://github.com/noknov/kepler-agent/pull/14)、[记忆生命周期 #15](https://github.com/noknov/kepler-agent/pull/15)。下文保留审查基线，具体可用性以对应 PR 是否合并为准；首版实现不代表效果评测完成。

## 本轮发现并修复

P1 表示可能破坏身份/隐私或长任务正确性；P2 表示能力、恢复或效率缺口；P3 表示不必要信息与维护问题。

| 级别 | 基线问题与触发条件 | 本轮处理 | 回归证据 |
| --- | --- | --- | --- |
| P1 | Context projector 检查 durable text 后才追加 environment 和当前图片；多图/长环境导致实际请求超预算 | 将 ephemeral input/environment 纳入同一次 projection，dispatch 前校验完整请求（归属 #13） | `runtime/environment_test.go` |
| P1 | 本地 JSONL append 有文件锁，但 CLI/app-server 的整个 turn 只有 runtime 实例内锁；两个进程 resume 同一 session 可同时执行 | 独立 `.turn.lock` 覆盖 turn 生命周期，与 `.events.lock` 分离 | `local/session_lease_test.go` |
| P1 | Git committed-tree 排除规则漏掉普通读文件已禁止的 credential 文件，且大小写语义不一致 | 用同一组 basename/suffix/directory 生成两种策略；实际 Git grep 验证 | `safety/git_paths_test.go` |
| P1 | MCP remote tool 缓存仅绑定 session/user，连接 token 变化后沿用旧握手状态 | 绑定实际 credential/config fingerprint、team scope 和 TTL；旧失败不驱逐新会话 | `tools/mcp/cache_regression_test.go` |
| P1 | 遥测 text 中带引号的 JSON 凭据名、部分 token/连接串可能绕过文本过滤；嵌套结果未递归省略 reasoning/artifact URI | 修复过滤覆盖、递归复制后过滤；保留 canonical evidence，RawMessage 数值不转 float | `infra/telemetry/content_test.go`、`runtime/telemetry_test.go` |
| P2 | 本地大 tool output 落在 state directory，普通 read_file 不可访问，且旧命名可能跨 turn 覆盖 | 增加 scoped artifact reader；opaque content/turn-bound ID；UTF-8 分页与受限文件根 | `local/artifacts_test.go` |
| P2 | 等待 runtime session mutex 的 turn 无法响应取消，可能拖延停止/关闭 | 取消感知的 admission 与引用回收 | `runtime/recovery_regression_test.go` |
| P3 | fixtures 含真实组织/项目标识，vendored UI 注释残留内部讨论 URL | 替换通用样例；移除私有引用并在 sync 时清理 | fixture 测试与源码扫描 |

表中 `runtime/` 位于 `packages/agent/`，`local/` 位于 `packages/profiles/`，其余路径位于 `packages/`。
这些修复不需要数据库迁移。新 local reader 只接收新 `artifact://` 引用；旧 transcript 中的裸磁盘路径没有被自动迁移。

## 主流 agent 的可验证机制，和 Kepler 的差距

以下来源在 2026-10-22 核对，描述公开机制，不能据此推断闭源系统全部内部实现。

| 机制 | 官方来源说明的方向 | Kepler 当前状态 | 下一步判断标准 |
| --- | --- | --- | --- |
| 原生 compaction | OpenAI 支持返回 opaque compaction item；完整返回窗口需要继续传回 | canonical schema 以 text/tool blocks 为主，当前 compactor 产生文本 checkpoint | provider-native state 可无损 round-trip；换 provider 有明确降级，而非把 opaque 内容当文本 |
| 按需上下文 | Cursor 把大输出/历史变为可重新读取的证据，按需装载工具 | masking、context_read、spill 已有；local spill 读取本轮补齐 | 多次压缩后的约束与证据召回、重复读取量、上下文成本 |
| 稳定 cache prefix | Cursor 把动态信息移出稳定层，并按 provider 能力设置 cache 边界 | prompt 层分隔和 Anthropic breakpoints 已有，跨 provider 能力不一致 | 固定任务下 cache hit、冷请求比例、质量无下降 |
| 长任务连续性 | Anthropic 强调可检查的进度/环境产物，压缩本身不够 | 有 transcript、plan、summary 和 child result；缺少统一 typed task state | 中断后恢复目标、约束、验证状态；不能因摘要说“完成”就结束 |
| session/harness/sandbox 分离 | Anthropic 将持久 session 与可替换执行环境分离 | core 接口分层较好，hosted 执行环境生命周期仍非独立通用控制面 | executor 重启时任务状态不丢，凭据与工作区不随意扩散 |
| 安全自主执行 | 官方文档区分授权、执行限制与模型行为 | exact allowlist、scope、sandbox、append-only 恢复基础已具备 | 跨工具、跨进程、跨入口的一致性；提示文本不能授予权限 |

来源：

- [OpenAI Compaction](https://developers.openai.com/api/docs/guides/compaction)
- [Cursor Dynamic context discovery](https://cursor.com/blog/dynamic-context-discovery)
- [Cursor Improved token efficiency for longer agent runs](https://cursor.com/blog/improved-token-efficiency)
- [Anthropic Effective harnesses for long-running agents](https://www.anthropic.com/research/effective-harnesses-for-long-running-agents)
- [Anthropic Scaling Managed Agents](https://www.anthropic.com/research/managed-agents)
- [Claude Code execution model](https://code.claude.com/docs/en/how-claude-code-works)
- [Cursor Agent Security](https://cursor.com/docs/agent/security)

## 还值得做的设计改进

### 1. 把任务状态从摘要中分离，优先级最高

`ModelCompactor` 的 checkpoint 仍是一段自然语言。任务目标、用户纠正、未完成项、验证证据、工作区 revision、
未知副作用如果只靠这段文本承载，连续摘要会改变含义。`plan` 是一个工具/呈现协议，并不是全部任务状态的权威账本。

建议增量引入一个可版本化的 TaskState：目标、当前约束、明确排除项、工作产物引用、验证结果、pending action、
unknown-effect 列表。每项有来源 event ID 与 revision。模型可以提出更新，runtime 检查 schema、来源与状态转移，
不能用一段模型输出把所有待办改成 verified。Transcript 仍是事实来源，TaskState 是可重建投影。

验收：同一任务经过多次压缩、进程退出与用户纠正后，保留全部关键约束；验证失败不被摘要“抹平”；
无需重读全部历史即可定位待办和相关证据。先用确定性故障用例，再用真实长任务测能力。

### 2. Provider-native state 应该有明确的扩展槽

`model.Content` 没有专门的 opaque provider continuation 类型；Responses adapter 主要转换 message/function call，
不能据此声称具备原生 compaction 或完整 reasoning state round-trip。
建议为 provider continuation 定义版本、provider/model binding、opaque payload、覆盖窗口和失效规则。
不能把加密块送给另一个 provider，也不能为了可观测性解码/导出隐私内容。

验收：同一 provider 的 native 与文本 fallback 各自有 round-trip tests；provider 切换明确回退到可移植 checkpoint；
未知 block 不被默默丢弃。先验证协议再做效果对照，不仅把 endpoint 接通就称为能力提升。

### 3. 让上下文策略可测，而不是继续堆固定阈值

`context.go` 的 2,400-byte masking、400-byte preview、最近两个 exchange、图片 1,024-token 估算，以及
`projectContextOnce` 的 85% soft threshold 都是有界默认策略，不是普遍最优值。
单个巨大 tool group 超过 compactor 输入上限仍可能无法缩减；错误日志、代码、图片、多语言输入的最佳策略不同。

下一步应有集中且有版本的 context policy，支持 provider token counter/usage calibration、固定的安全余量、
超大证据分页与 coverage metadata。保留原始引用，再评估局部摘要或 provider-native compaction。
不建议把每个常数都变成用户设置，也不建议按英文关键词决定丢弃哪类证据。

验收：预算估计误差、provider context rejection、summary 次数/成本、关键证据回读成功率与任务成功率共同改善。
仅 token 下降但漏掉任务约束，不算优化。

### 4. 记忆需要相关性、时效和删除生命周期

最新 `memory/query.go` 已改为全 namespace PostgreSQL 检索，解决了先截断候选集的问题。
但 `word_similarity > 0` 仍是词法重叠，不代表相关性；偏好无文本匹配也可参与，8 条/6,000 bytes 是注入上限，
不是经过任务评测的 recall policy。中文、语义改写、相互冲突的旧事实需要单独验证。

建议先建真实查询/期望命中的小样本集，比较 lexical、hybrid、rerank；以 provenance、更新时间、用户纠正和
scope 约束处理冲突，再决定是否引入 embeddings。自动抽取记忆是独立产品选择，不应作为“主流都有”的默认开关。
还需要 namespace 总量/配额、过期策略、删除审计以及 transcript/spill/trace 的独立保留政策。

### 5. 从并发限流走向可解释的资源调度

`Governor` 现在限制并发并维护共享 token reservation；它在 resilient client 外层，不是逐 provider attempt 的收费计量器。
内部重试、fallback、退避持有 slot 与 compactor/interactive/worker 竞争，都可能影响成本和交互尾延迟。
多副本还有每进程限额相乘问题。

建议分开三个契约：模型请求 admission、逐 attempt usage ledger、租户/任务调度。
按 provider quota 与实际 usage 记账，对未知花费保守标记；交互任务、compaction 和后台 worker 采用明确公平策略，
防止一个批次占满全部容量。不要用加大 concurrency 常数来掩盖排队。

### 6. 子任务要有可验证产物与冲突协议，再扩展写入协作

`agent-explore` 当前是有界只读 leaf task，这一边界值得保留。它没有共享编辑锁、独立 worktree 合并、
产物依赖图、动态写入协作或跨进程持久 worker 调度。把 read-only 改成 read-write 不能自动获得 agent team。

建议先增加 child report 的结构化证据/验证结果，按任务净收益决定是否分工；需要写入时使用独立 workspace revision，
由父任务验证产物并处理冲突。验收应同时统计重复研究比例、总成本、完成时间与真实错误发现率。

### 7. 安全与隐私仍需用能力边界约束

本轮修复了具体读取/遥测漏洞，但敏感文件名和 regex redaction 不可能识别所有个人信息或业务秘密。
`readOnlyHint` 不能证明远端工具无副作用；tool schema 也不等于执行前已完成全部校验。
默认 trace 关联标识、日志错误文本、人工下载的 transcript 与外部系统 retention 需要独立政策。

建议以 credential scope、executor sandbox、网络出口、工具级资源权限为硬边界；
对 telemetry metadata 做可配置假名化，对内容采集明确告知用途/保留期。
优先复现真实 data flow，避免不断增加无法证明覆盖率的秘密正则或 prompt 禁令。

### 8. 可读性与维护性：减少双重定义，建立契约测试

当前 runtime、provider adapter、产品 prompt overlay 的分层方向正确；但能力目录、工具 schema、wire conversion、
图文档与两种 artifact reader 仍有重复契约。`runner.go` 很大，测试也分散在多个 regression 文件。
建议先按 admission、projection、recovery、execution、terminal persistence 拆出明确边界，
使用 protocol/conformance tests 保护语义，再做文件拆分。仅把大文件分小而不明确不变量，收益有限。

静态 model catalog 应保留可审计的能力声明和来源版本，但不能永远靠模型名字猜测能力。
配置默认模型与 catalog 更新应有同一归属；不要因为对齐“2026”就在代码中硬替换成未经配置验证的最新型号。

## 如何证明“超越”

目标应是选定任务分布上的质量、稳定性与成本优势，而不是功能名更多。
建议先选择 Kepler 有实际优势的数据与工具链：代码审查、事故诊断、跨服务证据整合、可恢复长任务。

| 试验 | 控制条件 | 核心指标 | 失败必须计入 |
| --- | --- | --- | --- |
| 长任务连续性 | 同模型、相同初始仓库/工具与预算 | 完成率、关键约束保留、恢复耗时 | 压缩后遗漏、假完成、重复工作 |
| 上下文策略对照 | 同任务，text/native/masking 策略分组 | 准确率、input/cache tokens、压缩成本 | 证据丢失、读取不可达、context overflow |
| 可靠性故障注入 | 固定 append/timeout/取消/凭据轮换点 | 错误副作用次数、恢复一致性、p95 延迟 | 未知结果被当成功、重复写入 |
| 检索质量 | 人工核对的多语言 query-record 集 | recall、precision、冲突事实错误率 | 无关 memory 污染与跨 scope 命中 |
| 并发资源效率 | 同总预算和并发负载 | 完成时间、吞吐、每用户公平性 | 背景任务挤占、取消后仍收费 |
| 多 agent 净收益 | 单 agent 与 bounded leaf 分工对照 | 总成本、真问题发现率、验证耗时 | 重复发现、互相引用未经验证的报告 |

先做小而可信的真实任务对照，记录模型/工具/源码 revision、预算和故障点。
现有 deterministic tests 证明实现契约，不能代替上述效果证据。发布门禁仍可后置，运行能力评估应先进行。
