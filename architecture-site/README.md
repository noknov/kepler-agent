# 架构指南站点

这里是 Kepler Agent 当前主分支的中文架构说明。站点不依赖构建工具，直接打开 index.html 即可阅读。

项目文档总入口见 [docs/README.md](../docs/README.md)。本网站负责解释架构；
使用步骤、配置和排障分别由对应 Markdown 指南维护。

## 页面

- index.html：系统组成、完整请求路径、贯穿实现的规则和阅读入口。
- execution.html：一条请求从取得会话处理权到明确结束的控制流。
- models.html：提示内容、上下文投影、压缩、模型适配、重试和流式事件。
- tools.html：工具目录、参数、权限、安全边界、并发和结果处理。
- reliability.html：事件存储、并发所有权和中断恢复。
- delegation.html：子任务协议、批次调度、父子记录和代码审查工作流。
- surfaces.html：Slack 和 Web 的请求接收、身份、会话和结果呈现。
- cli.html：终端前端、应用服务、交互状态、滚动和批准流程。
- operations.html：入口队列、追加输入、运行投影、健康检查和关闭。
- reference.html：内部类型、事件、配置归属、数据库表、源码与测试索引。

## 维护

- assets/guide.css 保存文档布局、图表和响应式样式。
- assets/architecture.js 生成文档导航、本页目录和当前章节高亮。
- 概览建立全局结构，专题页沿真实执行路径解释行为，源码参考集中列出内部名称。
