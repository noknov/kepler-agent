# Memory 检索、时效与生命周期

此实现针对 [2026 审查](agent-audit-2026-10.md) 中的相关性、冲突、重排和保留问题。
用户规则、skills、transcript 和 local 文件记忆不属于 hosted 显式 memory 的同一份数据。

## 运行链路

1. Surface 完成身份校验，runtime 取得 session ownership，恢复原始 scope 和共享 token budget。
2. 提交 TurnStarted 与 UserInput 后，调用 profile 提供的 PrepareContext；回调不是模型可配置参数。
3. 从完整 `(team_id,user_id,generation)` namespace 查询非 preference 候选，默认混合排名后取 32 条。
4. 独立读取最多 2 条 preference。Query 也支持 exact key filter 与 kind filter，不开放模型填写 identity。
5. 可选 ModelRanker 在当前 turn 的 Governor/Budget 内排序或拒绝候选；它没有工具，也不能更新 memory。
6. 按 8 条 / 6,000 JSON bytes 限制 disclosure；其中 preference 最多 1,500 bytes，避免抢占全部证据预算。
   先按剩余字节预算选择能完整放入的事实，再计算条数；过大的记录不占用本可交给后续证据的名额，不截断原始记录。
7. 再核对 generation 与选中条目的 version，发现 reset/更新后有界重试；持续变更时返回空 memory。
8. 生成低信任 prompt fragment，并持久化 TurnContextPrepared 的 scope values。重放保留原 generation，
   不会因为重新检索而让旧审批取得 reset 后的新写权限。

模型披露使用统一 ContextRecord，仅携带 key/kind/content/version/时间；内部 source session/turn ID 留在鉴权存储接口，
不传给重排模型或注入 prompt。JSON 字节预算仍按完整 Record 保守计算，不会低估此披露。

PrepareContext 是可复用的 profile 扩展点，运行在锁与预算内。输入 scope 的 map 被复制；回调提供的 values
不能覆盖原身份、授权或恢复值。原始输入先持久化，即使 enrichment 取消或失败，也不丢掉用户请求。
已完成 turn 的 replay 不重新检索。中断 turn 恢复可能重新排序，因此可选模型调用会再次消耗预算。

## 排名机制与证据

Query 默认保留 lexical 模式供 list/search 和对照；自动 recall 默认使用 hybrid。
Hybrid 在数据库完整 namespace 内计算两条排名：Unicode trigram word_similarity 与 PostgreSQL simple 全文搜索，
用 reciprocal rank fusion 合并，默认 K=60，可在 Query 中调整；exact semantic_key 命中优先。
同分按更新时间和稳定 key 排序。没有先截取最近 N 条，也没有英文关键词表决定相关性。

这些是标准词法机制，不是 embedding semantic retrieval。
全文解析、trigram 与 rank fusion 的取舍需要使用实际任务样本验证，不能把任意非零 overlap 当成相关性证明。
`packages/memory/testdata/retrieval.json` 和 PostgreSQL integration test 提供中英文正例、负例、lexical/hybrid 对照；
它是小型确定性回归集，不是生产质量基准。新增业务样本时应记录期望 key、无关/冲突项与可接受时效。

可选 `MEMORY_RERANK=true` 使用已配置主模型判断候选相关性、冲突和语义改写，输出只允许已提供 key 的 JSON 数组，
可以返回空数组。不存在的 key、重复 key、额外字段、未完成输出都拒绝；失败回退数据库排序。
默认不开启，以免未经评测增加延迟和费用。默认输入最多 32 KiB、输出 512 tokens、最多 8 个 key，
候选超预算时从尾部收缩；因此它也可能漏掉排名靠后的事实。**重排无法召回未进入候选池的零词法重叠记忆。**
大规模语义召回需要独立的 embedding/index 生命周期、删除与权限协议，应在真实样本证明收益后实现。

参考：[PostgreSQL trigram](https://www.postgresql.org/docs/current/pgtrgm.html)、
[全文搜索控制](https://www.postgresql.org/docs/current/textsearch-controls.html)、
[RRF 公式与常数](https://www.elastic.co/docs/reference/elasticsearch/rest-apis/reciprocal-rank-fusion)。

## 更新、冲突与过期

- Version=0 仅用于创建；已有 key 更新必须使用 list/search 返回的 expected_version。未知/旧版本不能覆盖。
- 修订号由数据库序列分配，创建和更新都会获得新值；删除、过期或重建不复用旧值。它是 opaque CAS token，不是单条记录的修改次数。
- 同一 semantic key 是明确的冲突域；不同 key 之间的语义冲突不会被代码自动合并或删除。
- `expires_at` 是用户显式选择的时间，没有自动套用一个通用 TTL。过期条目不参与任何查询。
- 更新未提供 expiry 时保留原期限；`clear_expiry=true` 需要用户明确要求移除期限。
- 成功写入事务在同一 identity 内清理明确过期记录，并检查 namespace quota；generation 锁防止并发超额。
- `MEMORY_MAX_RECORDS` 默认 1,000；满额时允许带版本更新，拒绝新增并要求清理过时记录。
- Reset 仍提升 generation、删除当前 memory，并保留既有重试 receipt 语义。旧 generation 不能写回。

最终 version/generation 检查是检索期间的 snapshot fence，不是对用户 reset 的全局同步屏障。
已经送入 provider 的内容无法撤回；同一活跃 turn 先前装配的上下文也不会因此从远端消失。
Transcript、spills、备份和 traces 的保留策略仍独立；本变更没有重写 Git 历史或自动清理这些系统。

## 配置与升级

| 配置 | 默认 | 作用 |
| --- | --- | --- |
| MEMORY_MAX_RECORDS | 1000 | 每个 team/user 的显式记录上限 |
| MEMORY_RERANK | false | 启用额外模型重排；与主 turn 共用额度 |
| MEMORY_RECALL_TIMEOUT | 30s | DB + 可选重排的整体时间预算，失败时可继续无 memory 的任务 |

部署此 PR 前应用 `schema/postgres.sql`，为已有 user_memories 增加 nullable expires_at、scope/expiry 索引以及 user_memory_versions 修订序列。
表结构变更只在 schema 文件中，runtime 不执行 DDL。现有记录 expiry 为 NULL，不会被自动过期。
这是新增字段迁移，按现有流程先升级数据库再升级服务；只发布二进制会使旧 schema 上的 memory 查询失败。

## 验证边界

回归检查 generation/version fence、无关 key 注入、偏好预算、序列化大小、ranker abstention、共享 turn 预算、
scope map 不可覆盖、版本 CAS、expiry 保留/清除、quota 与 PostgreSQL 混合查询。
真实模型的相关性、延迟和成本未在这些确定性测试中证明；保持语义重排默认关闭，按业务样本评估后再启用。
