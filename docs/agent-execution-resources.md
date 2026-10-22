# 执行资源与可验证子任务

本实现基于主线既有的 model client 与 transcript 契约，分开模型 admission、实际尝试、共享预算和子任务产物。

## 实际 provider attempt

`Govern` 对普通 client 调度一次；`ResilientClient` 实现 `AttemptManagedClient`，对每次 primary/retry/fallback
分别调用 `DispatchAttempt`。每次调度按顺序取得执行槽、持久化预算 reserve、调用 provider、settle、记录 receipt、释放槽。
退避不持有槽。包含 resilience 的自定义 decorator 必须保留 AttemptManagedClient 契约；不能再套一个隐藏的重试循环。

失败返回了明确 usage 时按实际值结算；usage 缺失时保守扣完整 reservation。重试重新申请剩余额度，不能用同一份
reservation 发三次请求。预算持久化失败不 dispatch；settle 持久化失败保留未决 reservation，恢复时保守计入 spent。

`ModelUsageSettled` 是实际尝试账本，含 provider/model、attempt/fallback、operation/class、估算 reservation、
known usage、charged allowance、排队/执行耗时与 error kind。它不含凭据，也不记录内部推理。
Parent/child/compactor 共用父 turn 的 usage recorder 和 Budget，防止子任务重置额度。
`ModelCompleted.usage` 仍是逻辑生成结果的用量；统计全部尝试成本应读取 receipt，不把两者相加重复收费。
Charged 单位是 token allowance，不是金额；未知用量也不是已验证账单。外部代理内部的隐藏重试无法由本进程证明。

## 有界轮转

Governor 有明确 concurrency 与 MaxQueued，默认 16 / 1024；embedding 可通过 GovernorConfig 构造。
按 authenticated team/user（无用户时按 session）轮转，在同一 owner 内轮转 interactive/compaction/background，
同一 owner/class 内 FIFO。子任务继承 owner，只能改变类别；不能通过声明新 owner 获得更多份额。
排队支持取消，满队列立即返回限额错误，空队列与取消等待者及时回收。Snapshot 暴露 active/queued/owners。

这是进程内公平 admission，不是集群租户配额、token/s 调度、优先级抢占或 provider QPS 限流。
多个副本仍须由 operator 配置总容量。类间等额轮转能避免长期饥饿，但没有宣称交互延迟的实际性能优势。

## 子任务交付与回读

只有 TerminationCompleted 且报告非空的子任务可发布 DelegatedTaskCompleted。达到 steps/output cap、暂停或取消都不能
因为“有一段文字”而冒充完成。失败仍返回 child audit identity，审计清理有独立 timeout。

ChildRun 新增 parent_sequence、EvidenceCount 与最多 32 条 evidence preview。引用由 runtime 从真实 child 工具结果组装，
含 sequence、tool/call ID、block count、failed 与 SHA-256。Digest 标识 canonical content，不证明内容正确或充分。
超过 preview 的结果可通过 deferred `agent_evidence` 分页列出，并按 sequence/block/offset 回读。

读取授权来自 caller 当前 session 中的 DelegatedTaskStarted event；模型不能通过填写 child session ID 读任意会话。
读取还校验 child turn，只返回工具证据，不开放任意 child 对话或推理。父任务需要主动加载 reader，核对原始内容、
失败标志、适用 workspace revision 与验证覆盖范围。父报告仍不自动获得 verified 身份。

Context reader 与 child reader 共用 transcript.ToolEvidence，统一字节限制、UTF-8 边界和 block 校验。
没有开放写入子任务、共享编辑工作区或自动 merge。写协作需要独立 workspace、版本依赖与冲突协议；
应在证据交付机制经过真实任务验证后增量加入。

## 回归范围

覆盖多 owner/class 排队、满队列、取消回收、退避释放槽、失败已知/未知 usage、重试预算耗尽、
父 owner 继承、durable receipt、child ancestry、跨会话拒绝和有限终态。
这些测试不等同生产负载或多 agent 效果评测。
