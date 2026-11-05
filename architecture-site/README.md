# Agent Core 架构站点

这里是当前主分支共享执行核心的中文架构说明。站点不依赖构建工具，直接打开 index.html 即可阅读。

项目文档总入口见 [docs/README.md](../docs/README.md)。本网站负责解释架构；
使用步骤、配置和排障分别由对应 Markdown 指南维护。

## 页面

- index.html：系统组成、完整请求路径、贯穿实现的规则和阅读入口。
- execution.html：一条请求从取得会话处理权到明确结束的控制流。
- models.html：提示内容、上下文投影、压缩、模型适配、重试和流式事件。
- tools.html：工具目录、参数、权限、安全边界、并发和结果处理。
- reliability.html：事件存储、并发所有权和中断恢复。
- delegation.html：子任务协议、批次调度、父子记录和汇合边界。
- slack.html：Slack 请求接收、会话映射、附件与结果呈现。
- operations.html：入口队列、追加输入、运行投影、健康检查和关闭。
- production.html：把超时、租约、容量、发布和故障排查放在同一条生产链路上推演。
- reference.html：内部类型、事件、配置归属、数据库表、源码与测试索引。

## 维护

- assets/guide.css 保存文档布局、图表和响应式样式。
- assets/architecture.js 生成文档导航、本页目录和当前章节高亮。
- 概览建立全局结构，专题页沿真实执行路径解释行为，源码参考集中列出内部名称。
- 阅读生产专题时，先看可靠性中的权威事件，再看运维中的队列和投影；生产页只做跨模块的故障推演，不重新定义各模块的契约。

## 深入阅读

- [执行架构详解](../docs/architecture-deep-dive.md)：身份与信任边界、完整 turn、失败窗口、请求预算、取证与存储生命周期。

## Reading experience

The guide uses a chapter sidebar, per-page contents, section permalinks, previous/next
links, a mobile chapter menu, and a light/dark reading theme. Theme preference is
stored locally when browser storage is available. Search runs entirely in the
browser, across section titles and body text. It needs no remote service or font.
The static chapter content remains readable without JavaScript.

`reading-guide.html` owns reading paths, the shared glossary, and writing rules.
The Chinese prose borrows ASD-STE100 principles: consistent terms, explicit actors,
one main action per sentence, and conditions beside the relevant action. This is
an adaptation of writing principles, not an STE compliance or certification claim.
Keep code identifiers unchanged and link behavior claims to the owning source or
guide. Keep architecture contracts distinct from review history.

After editing any HTML content, regenerate and verify the offline search index:

```sh
python3 architecture-site/scripts/build-search-index.py
python3 architecture-site/scripts/build-search-index.py --check
```

Run these commands from the source repository root. The generator uses only the
Python standard library; no site build or package installation is required.
For browser checks, serve `architecture-site` with a local static server. Verify
search results across chapters, keyboard dismissal and focus return, mobile
navigation, both themes, section anchors, and wide-table scrolling. Check that
pages remain readable with JavaScript disabled. Run `make repo-check` for the
repository documentation checks.

The visual reading reference is [御舆](https://lintsinghua.github.io/).
[ASD-STE100](https://www.asd-ste100.org/) is the source of the technical writing
principles. Kepler source and owning documentation remain authoritative for the
architecture described here.

## Publish with CI

The `architecture-pages` GitHub Actions workflow deploys this directory to GitHub
Pages. Run it manually for the selected ref, or push an explicit release tag
matching `architecture-site-*`. A release tag publishes the site from that exact
commit, including commits on a feature branch. Ordinary branch pushes do not
publish the site. Check the workflow result before treating a release as live.
