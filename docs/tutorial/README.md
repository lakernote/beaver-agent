# 三天从 0 到 1 写一个 Agent

用 Beaver 的真实代码，三天弄懂一个可用 Agent 的全部组成。每天的节奏：**先读文档 → 再读代码 → 动手改一点 → 跑测试验收**。

前置：会读 Go；本机装好 Go；`go test ./...` 能通过。建议开两个窗口：一边读文档，一边在编辑器里跳转对应源码。

## 总览

```text
第 1 天  骨架     循环 + 工具 + 护栏        （Agent 是什么）
第 2 天  能力     规划 + 检索 + 记忆        （怎么变聪明，且不改循环）
第 3 天  落地     多入口 + 团队 + 调度      （怎么真正给人用）
```

贯穿三天的一句话：**循环保持简单，能力靠“普通 Tool + 一条提示词规则”扩展。**

## 第 1 天：骨架

目标：能讲清楚“模型决定、代码执行、结果回喂”的循环，以及为什么要有护栏。

| 顺序 | 阅读 | 代码 | 动手 |
| --- | --- | --- | --- |
| 1 | [01-agent-loop.md](01-agent-loop.md) | `internal/agent/types.go` 三个核心类型 | 画出循环流程图 |
| 2 | 同上第 3 节 | `internal/agent/runner.go` 的 `Run`、`executeToolCall` | 把 `defaultMaxSteps` 改成 2，观察最后一步不暴露工具 |
| 3 | 同上第 5 节 | `internal/builtin/tools/time.go`、`catalog.go` | 新增一个返回随机数的 Tool 并注册 |
| 4 | — | `internal/agent/runner_test.go` | 仿“假模型”写一个测试：第一步调工具，第二步答复 |

验收：能回答——最后一步为什么不给工具？同一个错误调用为什么会被拒绝？Skill 和 MCP 为什么不需要改循环？

## 第 2 天：能力

目标：理解规划、检索、记忆都只是 Tool，循环一行不改。

| 顺序 | 阅读 | 代码 | 动手 |
| --- | --- | --- | --- |
| 1 | [02-todo.md](02-todo.md) | `internal/builtin/tools/todo.go` | 给校验加一条新规则并补测试 |
| 2 | [03-rag.md](03-rag.md) | `internal/search/search.go` | 给 `Search` 加最低分阈值 |
| 3 | [04-memory.md](04-memory.md) | `internal/memory/`、`internal/builtin/tools/memory.go` | 阅读 `MemoryScope`，说明为什么身份不由模型传入 |

验收：能回答——todo 为什么不需要数据库？记忆和 RAG 的区别？为什么检索用 BM25 而不是向量？

## 第 3 天：落地

目标：看懂 Runner 之外的世界：入口、排队、隔离、定时。

| 顺序 | 阅读 | 代码 | 动手 |
| --- | --- | --- | --- |
| 1 | [design.md](../design.md) 第 3 节 | — | 说出哪些判断可以写进代码、哪些必须交给模型 |
| 2 | [05-team-wechat.md](05-team-wechat.md) | `internal/server/weixin_*.go` | 跟一条微信消息走完全程 |
| 3 | 同上第 4 节 | `task_scheduler.go`、`turn_queue.go` | 并发设为 1，两个账号同时发任务 |
| 4 | [runtime.md](../runtime.md) | `internal/permissions/` | 理解权限策略与审批 |

验收：能画出“多个成员各自绑定微信、各自一个活动任务、全局并发受限”的流程图。

## 之后可以继续

- 把 BM25 换成 Embedding 余弦相似度（[03-rag.md](03-rag.md) 第 4 节）。
- 给微信增加工具审批通道。
- 写一个自己的 Skill，或接一个 MCP 服务器。
- 读 [engineering-notes.md](../engineering-notes.md)：每个“弯路”都是一课。
