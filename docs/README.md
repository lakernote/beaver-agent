# 文档索引

## 学习路线（从这里开始）

[三天从 0 到 1 写一个 Agent](tutorial/README.md)

| 篇 | 内容 | 对应源码 |
| --- | --- | --- |
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

入门请先阅读仓库根目录的 [README](../README.md)，参与开发请阅读 [CONTRIBUTING](../CONTRIBUTING.md)。
