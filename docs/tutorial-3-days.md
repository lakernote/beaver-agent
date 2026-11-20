# 三天从 0 到 1 写一个 Agent

目标：用 Beaver 的真实代码，三天弄懂一个可用 Agent 的全部组成。每天先读、再动手、最后跑测试。
前置：会读 Go；本地装好 Go，`go test ./...` 能通过。

## 第 1 天：循环 + 工具（Agent 的骨架）

**要弄懂**：Agent = 模型决定、代码执行、结果回喂的循环。

| 时间 | 做什么 | 代码位置 |
| --- | --- | --- |
| 上午 | 读 [learn-agent-loop.md](learn-agent-loop.md)，再读 `Runner.Run` 主体 | `internal/agent/runner.go`、`types.go` |
| 下午 | 写一个自己的 Tool（如返回随机数），注册后让模型调用 | `internal/builtin/tools/time.go`、`catalog.go` |
| 晚上 | 仿 `runner_test.go` 写一个“假模型”测试：第一步调工具，第二步答复 | `internal/agent/runner_test.go` |

**验收**：能解释“最后一步为什么不暴露工具”“同一个错误调用为什么会被拒绝”。

## 第 2 天：让 Agent 更聪明（规划、检索、记忆）

**要弄懂**：这些能力都只是“普通 Tool + 一条提示词规则”，循环一行不改。

| 时间 | 做什么 | 代码位置 |
| --- | --- | --- |
| 上午 | [learn-todo.md](learn-todo.md)：无状态规划工具 | `internal/builtin/tools/todo.go` |
| 下午 | [learn-rag.md](learn-rag.md)：切块 + BM25 检索 | `internal/search/search.go` |
| 晚上 | [learn-memory.md](learn-memory.md)：落盘 + 检索 + 按成员隔离 | `internal/memory/`、`tools/memory.go` |

**验收**：能回答“为什么 todo 不需要数据库”“记忆和 RAG 的区别是什么”。

## 第 3 天：接入真实入口，支持团队使用

**要弄懂**：Runner 只管“一轮对话”；入口、任务调度、安全都在它外面。

| 时间 | 做什么 | 代码位置 |
| --- | --- | --- |
| 上午 | 读 [design.md](design.md)：Runtime 不按用户文字做关键词路由，只按协议与运行状态分支 | `docs/design.md` |
| 下午 | 跟一条微信消息：收件 → 斜杠命令 → 任务排队 → Runner → 回复 | `internal/server/weixin_inbox.go`、`weixin_commands.go`、`weixin_tasks.go`、`weixin_delivery.go` |
| 晚上 | 读 [agent-runtime.md](agent-runtime.md) 与权限策略，理解审批、工作区、并发调度 | `internal/permissions/`、`internal/server/task_scheduler.go` |

**验收**：能画出“多个成员各自绑定微信、各自一个活动任务、全局并发受限”的流程图。

## 之后可以做的练习

- 把 `internal/search` 的 BM25 换成 Embedding 余弦相似度（[learn-rag.md](learn-rag.md) 第 4 节）。
- 给 WeChat 增加审批通道（目前没有，默认权限见 README 的安全说明）。
- 增加一个自己的 Skill 或 MCP 服务器。
