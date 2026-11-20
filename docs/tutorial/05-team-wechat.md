# 学习：多入口与团队使用（Web + 微信）

对应源码：`internal/server/` 下的 `weixin_*.go`、`task_scheduler.go`、`turn_queue.go`、`automation_scheduler.go`。

## 1. 先划清边界

`Runner`（`internal/agent`）只负责**一轮对话**：给它消息和工具，它跑完循环返回答案。它不知道谁在提问、从哪里来、有几个任务在跑。这些都在 `internal/server` 里解决：

```text
Web 页面 ─┐
微信消息 ─┼─► 创建/续写会话 ─► 排队（全局并发 + 资源互斥）─► Runner ─► 结果回到来源入口
定时任务 ─┘
```

入口只有 Web 和微信；定时任务的输出也发往这两处。

## 2. 一条微信消息的旅程

| 步骤 | 做什么 | 代码 |
| --- | --- | --- |
| 1 收件 | 长轮询取消息，去重、过滤非本人消息 | `weixin_inbox.go` `poll` / `handleMessage` |
| 2 命令 | 以 `/` 开头才是控制命令（`/new` `/status` `/stop` `/projects` `/project`），其余全是任务 | `weixin_commands.go` |
| 3 提交 | 找到该成员的当前会话（没有就新建），写入消息，并把会话 owner 设为这个微信账号 | `weixin_tasks.go` `submit` |
| 4 排队 | 申请全局并发名额；同一工作区的任务串行 | `turn_queue.go`、`task_scheduler.go` |
| 5 回执 | 立刻回复“已接收”；并发满时额外提示在排队 | `weixin_inbox.go` |
| 6 回复 | 任务结束后把最终回答分段发回微信 | `weixin_delivery.go` |

**为什么命令只认斜杠？** 如果用关键词猜“用户想停止任务”，就会误吞“帮我写个停止服务的脚本”这种正常任务。把意图判断留给模型，把确定的控制动作留给显式命令，这是本项目的一贯边界（见 [design.md](../design.md)）。

`Runner` 是怎么被调用的？看 `internal/server/beaver_runtime.go` 的 `runBeaverTurn`：读设置 → `assembleBeaverTools` 组装工具 → 渲染提示词、按需压缩 → 创建 Runner 并挂好回调（落库、Trace、审批）→ 运行并保存。这是“循环”和“应用”的接缝，值得精读。

## 3. 多人各用各的

- **一人一个账号**：每位成员扫码绑定自己的微信账号，账号 ID 就是身份。
- **一人一个当前会话、同一时刻一个活动任务**：`/new` 才会开新会话；上一个没结束时不能开新的，要先 `/stop`。
- **个人记忆隔离**：会话在 `sessions.owner_id` 里记下 owner，运行时据此构造 `MemoryScope`，模型无法指定别人的身份（见 [04-memory.md](04-memory.md)）。
- **团队共享**：模型、项目、Skill、MCP 全队共用一套配置；团队记忆也是共享的。

> 当前是“单管理员 + 多个微信成员”的团队模式，没有 RBAC，也没有微信端的工具审批通道。这是已知的取舍，见 README 的安全说明。

## 4. 全局并发与资源互斥

`taskScheduler`（`task_scheduler.go`）只有两个职责：

1. **总并发上限**（默认 4）：满了就排队，排队状态落在 SQLite，重启后恢复。
2. **资源互斥**：任务声明自己要用的资源键（共享工作区、同一个本地模型端点），同键串行，不同键并行。

它被 Beaver 和 Codex 两种 Runtime 共用，所以“换 Runtime”不影响调度。

## 5. 定时任务

`automation_scheduler.go` 每 15 秒查一次到期任务，到期就和一条用户消息一样走同一条创建会话、排队、执行的路径，结果发往任务配置的 Web 或微信目标。**不需要为定时任务另写执行器**，这就是“入口只是创建会话的不同方式”的好处。

## 6. 练习

1. 读 `parseWeixinCommand` 的测试，给它加一个 `/help` 的别名。
2. 在 `handleMessage` 里数一数有多少个“提前返回”，各自防的是什么（重复消息、历史消息、非本人消息……）。
3. 把并发上限设为 1，用两个微信账号同时发任务，观察排队提示。
4. 思考：要加“微信端审批危险工具”，`OnToolApproval` 回调该怎么接到微信？需要新增哪些状态？

回到路线：[三天从 0 到 1](README.md)。
