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
| Evaluation | Local runner/report/gate and Harbor adapter | Validate gate inputs, freeze experiment identity, connect completed results to release decisions |
| Tool safety | Descriptors, effects, profile policy and sandbox | Cross-tool conformance, dynamic MCP classification, failure-path tests |
| Delegation | Bounded leaf tasks, child transcripts and parent links | Measure review accuracy, duplication, cost, cancellation, and aggregate resource use |

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

## 2026-10-08: recovery, delivery, and long-session costs

The following primary references informed this iteration (retrieved on
2026-10-08; their default branches and hosted documentation can change):

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

### Measured local append cost

Baseline: `d47061c5453894cda823b53d984943469e937839`. Both versions ran the same
`BenchmarkJSONLAppend` on an Apple M3 Pro, macOS arm64, with 1 KiB message bodies,
20 measured appends per sample and three samples. History was loaded before
timing, matching the runtime's load-then-append flow. `fsync` remained enabled.
The baseline store was selected with a Go source overlay, excluding the new
index implementation and its implementation-specific tests.

| Existing events | Before, median ms/append | After, median ms/append | Before, median bytes allocated/append | After, median bytes allocated/append |
| ---: | ---: | ---: | ---: | ---: |
| 100 | 6.216 | 4.841 | 465,336 | 3,391 |
| 1,000 | 10.202 | 4.257 | 3,899,715 | 3,592 |
| 10,000 | 59.073 | 4.374 | 41,593,066 | 3,592 |

Raw [before](benchmarks/2026-10-08-jsonl-before.txt) and
[after](benchmarks/2026-10-08-jsonl-after.txt) samples are retained. Re-run the
current implementation with:

```sh
GOCACHE="$PWD/.cache/go-build" go test ./packages/profiles/local \
  -run '^$' -bench BenchmarkJSONLAppend -benchtime=20x -count=3
```

At 10,000 events this is approximately 13.5 times faster. Cold index building
and full history loads remain linear; atomic batch replacement still copies
existing bytes. The cache retains at most 64 session indexes, containing IDs and
file offsets rather than message bodies. Memory within one index grows with its
event count. This microbenchmark measures local storage, not model quality,
end-to-end provider latency, PostgreSQL throughput, or power-loss recovery.

### Verification and next measurements

This iteration was checked with `make check`, `make test-race`, `make test-ui`,
and the CLI protocol typecheck/bundle. UI tests execute the real owned submission
and transport code with deterministic rendering/DOM boundaries; they are not a
visual terminal or browser end-to-end test. Live PostgreSQL/Redis, multi-pod
routing, and real provider evaluations were not run locally. CI retains the
existing PostgreSQL/Redis/Linux sandbox checks and now runs the UI regressions
and local-store/delegation race tests.

Further work should be driven by production evidence: aggregate model/tool
admission across surfaces, Web multi-pod event routing/cancellation, incremental
run-summary projections, atomic session-history initialization, and narrowing
the CLI's imported UI surface. The broader vendor TypeScript scope still reports 1,949 pre-existing diagnostics
(all in `src/cc`, with none in owned source during this check); the protocol-only gate is not a substitute for removing
that debt. These are separate contracts and should not be hidden behind a
wholesale plugin-framework rewrite.


## 2026-10-09：硬编码、启发式、资源浪费与提示词审查

本轮以 `17a11604` 和当前工作区为基线，检索自有代码中的错误字符串分类、
模型/工具名称分支、固定预算、重复解析、脱离取消的任务和提示词注入，随后
检查 prompt 装配、provider/resilience、runtime/compaction、投影、工具发现、
Slack/Web/CLI 入口及部署配置的实际调用链。检索只是定位线索；下表依据实现
和可构造的反例。没有逐行复审 CLI 导入的第三方 UI，也没有用真实负载验证
多实例行为。代码审查和本地回归不能证明“所有问题已经消失”。

### 已处理

| 问题 | 修改及边界 | 验证入口 |
| --- | --- | --- |
| 提示词重复解释通用语法、强行规划或归一化输出 | core/rules 只保留任务、证据、权限和表达约束；Slack/Web 只声明渲染格式；删除 runtime 按 `update_plan` 名称追加规则和“3+ 步必规划”规则 | `prompts/catalog_test.go`、`agent/runtime/runner_test.go`、Slack 服务测试 |
| CLI 提示词与工具契约矛盾 | 删除 command-string 指导和无条件批量读取；不再假定所有本地模型都经 Kepler 托管 | `cli/headless.go`；工具仍使用 argv，执行策略没有放宽 |
| 环境事实夹带特定搜索策略 | 保留日期、时区、工作区；删除重复年份和强制年份搜索指令 | `agent/environment/environment_test.go`、`safety/guard_test.go` |
| 技能过度触发与重复协议文档 | 技能按意图/流程需要加载；瑞幸技能删除关键词触发、凭据读取/保存、shell fallback、复制的 MCP schema 和固定话术，保留订单预览、金额、优惠与授权边界；移除未注册的 `luckin-query_coupons` 描述 | catalog 加载及已有技能/工具测试；这些检查不代表真实订单端到端验证 |
| prompt cache 发布不原子 | 同一次加载组合完整 prompt，在同一把锁下发布 catalog 与缓存，避免重置 `sync.Once` 的并发竞态及混合版本 | `prompts/catalog_cache_test.go`，并发 reload/read + race |
| Web 用正则修补 Markdown | 移除双反引号“修复”；合法代码块内的双反引号不再被改为结束围栏；保留 parser、HTML 清理和无资源时的转义回退 | `surfaces/web/tests/markdown.test.mjs` 使用真实 vendored parser；清理边界使用 stub，不能替代安全或视觉测试 |
| Slack 静默修改文字及格式混用 | 保留代码中的 NBSP；原生 Markdown 与适配器 UI 的 mrkdwn 分开；原生格式不可用时按纯文本回退 | `surfaces/slack/agent/service_test.go`、`surfaces/slack/client/client_test.go` |
| 根据错误文案猜控制流 | Web 状态码按类型映射；工具重复注册和连接缺失按 `errors.Is` 判断，支持包装错误 | `surfaces/web/errors_test.go`；相关连接/注册器包测试 |
| JSON 输入校验只看前缀和截断后的 EOF | 按 MIME 类型解析 Content-Type，并读取额外一字节验证完整请求上限，拒绝界外空白/第二个对象 | `surfaces/web/errors_test.go` |
| 后续串行工具可能抢在此前的并行读取前执行 | 在派发 goroutine 前占有读锁；并行读取仍可重叠，串行操作等待此前读取完成 | `agent/runtime/audit_regression_test.go` |
| 取消后的落盘及异步投影缺少生命周期约束 | model/tool 清理落盘使用已有 `CleanupTimeout`；异步可重建投影继承服务取消 | runtime 审查回归与 `agent/transcript/async_test.go`；不保证能强制停止不响应 context 的工具 |
| 压缩可能耗费过量输出或不收敛 | 默认 4096 输出上限不再被较大的 target 覆盖；计入压缩指令预算；不可分消息组超限时拒绝派发；中间摘要不缩短时立即停止 | `agent/runtime/compactor_test.go`；token 数仍为估算 |
| 按供应商名称把所有 400 当临时故障 | 删除聚合商名称例外，HTTP 错误按状态分类；若以后需要特殊重试，应采用已验证的结构化错误契约 | `llm/errors_test.go`；供应商错误/恢复已有测试 |
| fallback 重复调用同一个模型路由 | 同时核对 provider、model、protocol、endpoint、credential 与 flavor，只抑制相同路由；`MaxAttempts` 仍控制显式重试 | `profiles/hosted/profile_test.go` 验证相同路由不追加调用、不同路由仍 fallback |
| 检索/文本处理重复分配 | query 分词移到描述符循环外；名称正确去重，中文描述按 rune 截断；网页空白正则只编译一次 | `agent/tool/search_test.go`、websearch 测试及下方基准 |
| 无效配置与虚假文档 | 移除 deploy 的 `PROGRESS_PROVIDER/MODEL`，修正 runtime 文档：应用没有进度模型请求；先前“进度模型已生效”的说法不成立 | deploy 脚本的 6 个模拟测试；未重新部署本轮改动 |

共享 system + general rules 从基线的 **1270 个空白分隔词降到 271 个**（约
78.7%）。这不是 tokenizer 测量，也没有包含工具 schema、按需技能或私有 overlay。
缩短 prompt 可以减少固定上下文，但“质量提高”仍需固定任务集和真实模型对照。
安全/权限/证据边界是必要策略，不能因为模型懂语法就删除；语法教程、重复
schema 和由程序负责的协议判断则不应占据通用 prompt。

### 仍需优先处理

| 优先级 | 证据及影响 | 建议与验收条件 |
| --- | --- | --- |
| P1 | `providers/catalog.go` 只有少量静态能力声明，没有 ChatGPT；`worker/service.go` 依其结果判断图像输入，未知路由会被按不支持处理 | 能力来自经过验证的 provider 元数据或操作员声明，区分 unknown/unsupported；对当前 ChatGPT 路由测试真实图像派发，不靠模型名猜能力 |
| P1 | Web 的 active map 按会话限制，app-server 和 Slack 有各自上限，缺少覆盖主模型、子任务、路由器和压缩的 worker 总准入预算 | 在组合层建立共享模型请求与工具资源预算；混合表面/子任务压测峰值并发、排队、取消和拒绝；避免父任务占满槽位后等待子任务造成死锁 |
| P1 | Web 固定 30 分钟 turn、35 分钟输入 claim；Slack run 可配置而 claim/Redis 标记固定 35 分钟；OAuth continuation 使用 `WithoutCancel`，若清理/查询阻塞可超出服务生命周期 | 统一任务 deadline、claim 租期及续租协议，保留 owner fencing；区分服务生命周期与有限清理 context；测试长任务、关闭、claim 过期和节点失联，不能只替换一个常数 |
| P2 | `Runtime.turnEvents` 每步复制历史，`completedToolResults` 和 `nextModelRequestID` 再扫描；Web terminal 查询加载整段历史；`RunSink.applyTrace` 可能反复读取含步骤的 run | 为当前 turn 维护增量索引，存储提供 terminal/trace 等小查询；以长会话的总分配、SQL 次数和 P95 为验收，保留跨进程恢复一致性 |
| P2 | `agent/runtime/context.go` 使用字符启发式和固定图像 token 估算；compactor 的 MaxInput 与主模型配置绑定，独立压缩模型和自定义 buffer 可能有不同窗口 | 注入 provider/model 的预算计量能力，分离输入窗口与输出上限；用真实 usage 校准中文、schema、图像和压缩误差，未知值保守但可观测，不能将估算称为协议硬保证 |
| P2 | `appserver/cmd/app-server/main.go` 只有一句 core；headless CLI 另行组装本地策略、项目指令与技能 | 共享本地 profile 的 prompt 装配契约，允许表面添加渲染事实；测试相同仓库下 interactive/headless 实际收到的项目约束，避免只对其中一个入口做评测 |
| P2 | `agent/tool/search.go` 的中文别名基于子串，`云` 会命中“云南”；排名权重没有检索评测支撑 | 建立中英任务→正确工具的小型评测，测 recall、误激活、上下文开销和调用成功率后决定算法；它只用于发现，不能决定权限 |
| P2 | hosted 的多个 resilience 实例仍各有 circuit 状态；工作流路由器可先用模型分类，再调用相同主模型；默认空回复重试 3 次 | 按真实调用轨迹测量重复调用与收益；按路由共享需要共享的健康状态，必要时让工作流显式进入；空回复重试应有总时间/成本预算，不用新的关键词路由替代 |
| P2 | 瑞幸的 preview→create、优惠券及金额校验目前依赖技能，工具端只是直接转发 MCP；模型偏离流程仍可能绕过业务校验（操作员写工具 allowlist 仍有效） | 对真实外部写入引入可核验的预览凭证、参数绑定与幂等策略；测试篡改价格/商品、过期预览和结果未知，prompt 不能充当业务授权机制 |
| P3 | `health.SummaryPrompt`、`prompts.ToolStatus`、`DynamicBoundaryMarker` 当前没有生产调用；导入的 CLI UI 面积大；`RunSink.saveRun` 无类型判断地重试且最后失败仍 Sleep | 移除/接通死接口，缩小自有 UI 依赖面；仅重试明确临时故障并使等待可取消。避免用新的功能开关维持无人使用的概念 |

允许保留的常数包括协议标识、合法枚举、schema 约束和有明确资源目的的默认
上限。需要质疑的是“这几个模型名一定如何”“错误文案含某词就是某类故障”
“出现某关键词就启用某流程”等无法作为可靠契约的判断。敏感路径/命令正则
仍只是辅助检查，不是完整的秘密识别器或执行沙箱；不能靠增加黑名单宣称
安全问题已解决。

### 局部性能证据

同一台 Apple M3 Pro，Go darwin/arm64；两版运行同一 benchmark，200 次/样本，
各 3 个样本，比较中位数。Before 是分词/去重/正则修改前的实现，不是整次
审查的完整旧版本；After 仅测对应帮助函数所在路径。

| 场景 | Before | After | 分配字节 Before→After | 分配次数 Before→After |
| --- | ---: | ---: | ---: | ---: |
| 200 个工具，混合中英 query | 0.581 ms | 0.206 ms | 475806→219744 | 6620→2643 |
| 100 行网页文本空白处理 | 0.206 ms | 0.111 ms | 238576→44732 | 3127→804 |

原始样本：[before](benchmarks/2026-10-09-audit-before.txt)、
[after](benchmarks/2026-10-09-audit-after.txt)。可重跑：

```sh
GOCACHE="$PWD/.cache/go-build" go test ./packages/agent/tool ./packages/tools/websearch \
  -run '^$' -bench 'Benchmark(DeferredSearch|CleanWhitespace)$' -benchtime=200x -count=3
```

这些结果不代表网络/模型延迟、系统吞吐或回答质量。静态检索结果排序未通过
新的真实任务评测；本轮没有宣称更换排序启发式后获得质量提升。

### 验证范围

本轮运行 `make check`、`make test-ui`、相关包 race 和部署脚本模拟测试。
最初的 sandbox 全量/race 运行因 `httptest` 不能绑定 loopback 端口失败，随后在
允许本地端口的环境重跑通过。Web 测试验证真实 Markdown parser 的输出和
应用回退路径，没有人工浏览器视觉验证。本轮没有发送 Slack 消息、执行
真实订单、做在线完整 prompt 评测，或部署本轮审查改动。
