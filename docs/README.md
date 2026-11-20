# 文档索引

## 学习路线（从这里开始）

第一次学习，建议先完成 [6 小时从 0 到 1 手搓一个 Agent](tutorial/00-build-an-agent-in-6-hours.md)：课程配有只依赖 Go 标准库、无需 API Key 的可运行示例，并把每一步映射回 Beaver 的生产代码。想快速浏览或继续三天源码路线，进入 [教程主页](tutorial/README.md)。

| 篇 | 内容 | 对应源码 |
| --- | --- | --- |
| [00 六小时手搓 Agent](tutorial/00-build-an-agent-in-6-hours.md) | 从消息协议、Tool Loop 到真实模型和生产化 | `examples/mini-agent`、`internal/agent` |
| [01 Agent 循环](tutorial/01-agent-loop.md) | 循环、工具、护栏 | `internal/agent` |
| [02 任务规划](tutorial/02-todo.md) | `todo_write`，无状态规划 | `internal/builtin/tools/todo.go` |
| [03 RAG](tutorial/03-rag.md) | 切块、BM25、向量升级思路 | `internal/search` |
| [04 长期记忆](tutorial/04-memory.md) | 落盘 + 检索 + 成员隔离 | `internal/memory` |
| [05 多入口与团队](tutorial/05-team-wechat.md) | Web/微信、排队、定时 | `internal/server` |

## 参考

| 文档 | 内容 |
| --- | --- |
| [design.md](design.md) | 设计目标、核心概念、“代码 vs 模型”的边界 |
| [runtime.md](runtime.md) | 消息、上下文压缩、Skill、MCP、Trace 的运行原理 |
| [engineering-notes.md](engineering-notes.md) | 工程复盘：走过的弯路与修正原则 |

教程中的命令、默认值和运行路径以当前代码与测试为事实来源；尚未合入的设计不会提前写成现状。

入门请先阅读仓库根目录的 [README](../README.md)，参与开发请阅读 [CONTRIBUTING](../CONTRIBUTING.md)。
