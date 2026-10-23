# 持续执行：任务状态、上下文策略与原生续接

本实现直接基于主线既有的 runtime，与 plan、transcript、memory 分工如下。

| 数据 | 权威与用途 | 压缩后 |
| --- | --- | --- |
| transcript | 原始输入、工具调用和结果 | 不删除 |
| task_state | 模型维护、runtime 校验的任务投影 | 最新快照独立保留 |
| plan | 给用户呈现的执行计划 | 不自动等同任务账本 |
| checkpoint | 历史对话的有损续接 | 文本 fallback，可附原生窗口 |
| memory | 用户显式保存的跨会话信息 | 独立身份、generation、删除范围 |

## TaskState v1

`task_state` 是 deferred 的运行时工具，通过 `tool_search` 发现。读取 `{}` 返回 state 与最多 12 个近期来源，
每条含 sequence/type 和有界文本。更新必须提供 `expected_revision` 与 `upsert`；所有未列出的条目保留。
更新是当前会话执行权保护下的 CAS，使用 exclusive tool batch。模型更新的唯一写入入口是该内置工具；runtime 另可登记恢复中发现的未知执行结果。

条目包含稳定 ID、kind、text、status、sources、evidence、workspace_revision、verification。
支持 goal/constraint/work/question/artifact，另有 runtime 专属 effect；状态为 active/blocked/done/verified/superseded，effect 使用 unknown/reconciled。
最多 64 条、24 KiB，不随每个 token 增长。模型不能通过省略条目删除约束；无关的新任务应使用新会话。

Goal/constraint 必须引用真实 user/steering event 与逐字 quote；改变已有用户约束需要更晚的用户来源。
Verified 必须有成功工具结果及验证说明，证据在条目来源之后；跨 session、错误工具结果和不存在的 sequence 被拒绝。
Workspace revision 是声明，不是运行时自动探测的 Git revision。此协议检查来源与结构，**不证明测试命令充分、
工具返回内容正确或自然语言结论成立**。父任务仍须核验适用 revision 和验证范围。

`TaskStateUpdated` 事件保存完整快照。Memory/JSONL/PostgreSQL 的 continuation loader 保留最新状态，
即使其 sequence 落在 covers_through 之前。Runtime 的上下文缓存同样独立保留最新快照；投影为低信任 user data，
参与完整预算，不再经过摘要。旧事件仍可审计。Snapshot 与工具结果分两次 append；若间隙退出，
已提交状态保留，工具调用按既有 unknown-effect 规则恢复，不再次盲目应用。

## 未知工具结果

恢复到 `execution_interrupted` 时，runtime 把未知结果登记为 task_state 的 effect/unknown 条目。
它不由模型凭空创建，原始来源和 identity 不允许覆盖；摘要和省略条目不能把它清掉。
只有带更晚成功工具证据和说明的 reconciled 转移才被接受；这仍是有证据的模型声明，不是外部事务证明。
账本满时明确失败并保留原始 transcript 事实，不静默省略未知动作。恢复会补齐事件与快照之间退出造成的缺口。

## ContextPolicy v1

原先分散的默认策略集中为版本化 `ContextPolicy`：soft_limit_percent=85、mask_min_bytes=2400、
preview_bytes=400、recent_exchanges=2、image_tokens=1024。零值选择默认，非法组合在启动/投影时拒绝。
它们是可测的初始值，不是已证明的最优配置；没有按任务关键词决定丢弃内容。

`TokenCounter` 允许 embedding 注入 provider tokenizer；默认 EstimatedCounter 对中文、标点和图片估算。
同一 counter 用于投影与最终 dispatch；`ContextProjected` 保存 policy 与估算值，方便与 provider usage 对照。
每种 counter 必须覆盖消息、schema 和 continuation，不可只计算文本。此实现没有自动启动网络 token-count 请求。

单个巨大 exchange 在摘要阶段转换为低信任 JSON 文本片段，按预算切分、保持 UTF-8 边界，不丢字节；
片段不作为可执行的半个 tool-call 协议发送。摘要的缩减与轮次限制仍有效。

## 原生续接与跨 provider 降级

[官方 Compaction](https://developers.openai.com/api/docs/guides/compaction) 与
[Compact API](https://developers.openai.com/api/reference/resources/responses/methods/compact)
要求继续携带完整返回窗口；不能只拿 encrypted_content 或任意裁剪 output。

Canonical `ContentContinuation` 保存 version、provider、model、route 指纹、kind、完整 opaque items 和 token estimate。
Responses adapter 同时保留普通输出中的 reasoning/unknown items，下一轮按原窗口顺序重放。
精确 JSON 数值保留，不把 opaque 数据转换为普通文字或送到别的 provider。
Route 指纹包含 provider/protocol/endpoint/credential identity；ChatGPT 额外绑定登录账号、client 和 host identity，token 正常刷新不失效，同一路径换账号降级为文本，并在传输前再次验证身份。模型、路由或凭据变化时使用原有可移植消息。
不匹配的窗口不改变源 transcript。未知 schema version 在匹配路由上明确拒绝，避免默默损坏协议。

Native compact 显式启用：hosted `LLM_NATIVE_COMPACTION=true`；local TOML `native_compaction=true`，
本地还需要 hosted 路由允许此 operation。默认关闭。仅 Responses 路由/已路由到 Responses 的模型实现此 endpoint；
服务不支持或可选 native 调用失败时保留文本 checkpoint。取消继续传播。

为支持切换 provider，当前先产生可移植摘要，再请求 native window，因此 native 开启会增加一次压缩调用。
这是明确的兼容性成本，是否值得启用需要同任务评测；不声称必然省 token。当前 opaque token 数按编码大小保守估算，
不是精确 tokenizer；可注入 counter。Native payload 从外部 trace 内容递归省略，但仍在授权的持久 transcript 中。
没有自动启用 provider retention，也没有依赖 previous_response_id。

## 验证

回归覆盖 revision 冲突、伪造来源、失败测试、跨 session 引用、压缩后约束保留、JSONL 重开索引、
完整 native window round-trip、未知 item/大整数、模型/凭据切换 fallback、opaque trace omission、
策略图片估算与 masking，以及巨大输入分片。测试仅证明协议，不代替真实长任务效果对照。
