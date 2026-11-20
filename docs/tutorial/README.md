# 从会调用模型，到真正理解 Agent

这套教程依托 Beaver 的真实代码，但不会一上来把读者丢进生产级 Runner。学习顺序是：

```text
先在几百行代码里亲手跑通协议和循环
                    ↓
再把每一个简化点映射到 Beaver 的生产实现
                    ↓
最后学习检索、记忆、持久化、权限、多入口和恢复
```

目标不是“会调用一个 SDK”，而是学完后能独立回答：模型、Agent、Tool 和应用服务各自负责什么；一次工具调用为什么需要两条配对消息；循环如何终止；真实系统为何还需要事务、Trace、权限和调度。

## 选择一条路线

### 路线 A：90 分钟看懂核心

适合先建立全貌，不急着写完整代码。

1. 阅读 [6 小时从 0 到 1 手搓一个 Agent](00-build-an-agent-in-6-hours.md) 的“开始之前”“第 0 小时”和“第 2 小时”。
2. 运行一次离线示例：

   ```bash
   go run ./examples/mini-agent -trace
   ```

3. 对照 [`examples/mini-agent/runner.go`](../../examples/mini-agent/runner.go) 画出循环。
4. 阅读 [01 Agent 循环](01-agent-loop.md)，理解 Beaver 为何比最小实现多出恢复和护栏。

验收标准：可以不用术语堆砌，向别人解释“模型提出工具调用，本地代码执行，再把结果回给模型”。

### 路线 B：6 小时从 0 到 1 手搓

适合第一次系统学习 Agent，推荐从这里开始。

[进入完整课程：6 小时从 0 到 1 手搓一个 Agent](00-build-an-agent-in-6-hours.md)

| 时间 | 本小时产物 | 核心问题 |
| --- | --- | --- |
| 00:00–00:30 | 跑通离线 Demo，画出消息轨迹 | Agent 和普通聊天调用差在哪里？ |
| 00:30–01:15 | `Message`、`ToolCall`、`Model` | 哪些是核心领域类型，哪些只是厂商协议？ |
| 01:15–02:15 | 第一个完整 Tool Loop | 何时执行工具，何时结束，结果怎样配对？ |
| 02:15–03:00 | OpenAI-compatible 适配器 | JSON 请求/响应怎样变成核心类型？ |
| 03:00–04:00 | Fake Model 与失败分支测试 | 不依赖真实模型，怎样证明循环是对的？ |
| 04:00–05:00 | Tool、RAG、Memory 的统一视角 | 为什么新增能力通常不需要改循环？ |
| 05:00–06:00 | 从 Demo 映射到 Beaver | 持久化、Trace、权限、压缩和队列解决什么？ |

每个阶段都有：目标、要改的文件、可复制命令、预期结果、原理解释、常见错误、检查点和答案。默认使用确定性的假模型，没有 API Key 也能完成；真实模型接入是可选实验。

### 路线 C：3 天读懂生产实现

适合已经理解 Tool Loop，准备系统阅读 Beaver。

#### 第 1 天：核心循环与 Provider

| 顺序 | 阅读 | 源码入口 | 动手验收 |
| --- | --- | --- | --- |
| 1 | [01 Agent 循环](01-agent-loop.md) | `internal/agent/types.go` | 标出 `Message`、`ToolCall`、`ToolSpec`、`Model` 的边界 |
| 2 | 同上 | `internal/agent/runner.go` | 跟踪一次“模型 → 工具 → 模型” |
| 3 | 6 小时课程第 3 小时 | `internal/agent/openai/chat.go` | 对照 wire JSON 和核心类型 |
| 4 | — | `internal/agent/runner_test.go` | 用假模型增加一个失败分支测试 |

#### 第 2 天：能力层

| 顺序 | 阅读 | 源码入口 | 动手验收 |
| --- | --- | --- | --- |
| 1 | [02 任务规划](02-todo.md) | `internal/builtin/tools/todo.go` | 解释规划为何是 Tool，不是 Runner 分支 |
| 2 | [03 RAG](03-rag.md) | `internal/search/search.go` | 用小语料解释切块、分词、BM25 排序 |
| 3 | [04 长期记忆](04-memory.md) | `internal/memory/` | 区分会话历史、压缩摘要和跨会话记忆 |
| 4 | [运行原理](../runtime.md) | `internal/builtin/tools/loader.go`、`internal/mcpclient/` | 解释渐进加载怎样控制工具数量 |

#### 第 3 天：产品运行时

| 顺序 | 阅读 | 源码入口 | 动手验收 |
| --- | --- | --- | --- |
| 1 | [设计说明](../design.md) | `internal/server/turn_execution.go` | 画出入口到 Runner 的接缝 |
| 2 | [05 多入口与团队](05-team-wechat.md) | `internal/server/turn_queue.go` | 区分持久化队列和进程内调度 |
| 3 | [运行原理](../runtime.md) | `context_compaction.go`、`trace_observer.go` | 区分消息历史、压缩检查点和 Trace |
| 4 | [工程复盘](../engineering-notes.md) | `internal/permissions/`、`tool_operations.go` | 解释 Prompt 为什么不能代替权限边界 |

## 学习前准备

你需要：

- 能读懂基本 Go：结构体、接口、切片、`context.Context` 和错误返回；
- 在仓库根目录执行命令；
- 安装根目录 [`go.mod`](../../go.mod) 声明的 Go 工具链；
- 愿意先用假模型理解确定性代码，再接真实模型。

先做一次环境检查：

```bash
go version
go test ./examples/mini-agent
```

预期第二条命令以 `ok` 结束。它不访问外部网络，也不需要启动 Beaver。

如果你还不熟悉 Go，先知道三件事也可以开始：接口表示“只依赖行为，不依赖具体实现”；`context` 用来取消和超时；Go 的错误通过最后一个返回值显式传递。课程遇到这些地方会再次解释。

## 课程中的四层代码

学习 Agent 最容易混乱，是因为四层经常被 SDK 包在一起：

| 层 | 负责什么 | 教学示例 | Beaver |
| --- | --- | --- | --- |
| 核心协议 | 消息、工具调用、模型接口 | `types.go` | `internal/agent/types.go` |
| Agent 循环 | 决定继续调用工具还是结束 | `runner.go` | `internal/agent/runner.go` |
| Provider 适配 | HTTP、厂商 JSON、流式解析 | `openai_compatible.go` | `internal/agent/openai/`、`ollama/`、`anthropic/` |
| 应用运行时 | 会话、数据库、权限、队列、入口 | 教学示例故意不实现 | `internal/server/`、`internal/store/` |

遇到一个功能时，先问“它属于哪一层”。例如 HTTP 401 是 Provider 层问题；模型一直调用工具是循环护栏问题；重启后任务是否重复执行是应用运行时问题。

## 每章怎么学

建议每一节都执行同一个循环：

1. 先读“本节完成后能运行什么”。
2. 不看源码，猜一次输入会产生哪些消息。
3. 执行命令，比较预期输出和真实输出。
4. 只阅读本节涉及的文件，不提前吞下整个仓库。
5. 故意触发一个失败分支。
6. 运行测试，确认自己的理解不是“碰巧成功”。
7. 最后再读“Beaver 对照”，理解生产复杂度解决的具体问题。

不要把“模型这一次表现正确”当成测试通过。模型输出有随机性；核心循环、协议转换和安全边界必须尽可能由确定性测试验证。

## 术语表

| 术语 | 在本教程中的精确定义 |
| --- | --- |
| Model | 接收消息与工具说明，返回文本或 Tool Call 的组件 |
| Agent | 围绕 Model 组织工具、循环、状态和护栏的程序 |
| Tool Spec | 发给模型的名称、说明和参数 JSON Schema |
| Tool Executor | 留在本地、真正产生结果或副作用的函数 |
| Tool Call | 模型生成的“请以这些参数调用这个工具” |
| Tool Result | 本地执行结果，以 `tool` 角色和 `tool_call_id` 回传 |
| Session | 一段可持续多轮的会话 |
| Turn | 一次用户输入以及由它触发的完整处理 |
| Step | Turn 内的一次模型决策 |
| Attempt | 同一 Step 因瞬时错误或协议降级而发生的一次真实请求 |
| Event | 用于展示和审计的一条运行事件 |

特别注意：Turn、Step 和 Attempt 不是同义词。把三者混在一起，会让重试统计、Trace 和最大步数全都失真。

## 文档事实边界

教程中的命令和示例以当前仓库代码为准。正在讨论但尚未合入的设计，不会写成既成事实。当前已经落地的共享 Memory Store、稳定微信成员 ID、追问投递循环和定时任务微信通知会按现状讲解；完整无碰撞的 owner 目录编码、跨 Store/跨进程锁与原子写、独立来源核验标记、SSRF 网段白名单和 MCP 敏感字段恢复仍会明确标成改进方向。

发现文档与代码冲突时，以测试和源码为事实来源，并提交一个同时包含实现、测试与文档的修正。

## 完成后的下一步

- 想写自己的能力：从一个无副作用 Tool 开始，再补参数错误和超时测试。
- 想理解检索：继续 [03 RAG](03-rag.md)，亲手构造一个能手算排名的小语料。
- 想理解长期状态：继续 [04 Memory](04-memory.md)，重点看身份、并发和明文存储边界。
- 想理解产品化：从 `executeSessionTurn → runBeaverTurn → Runner.Run` 跟一轮真实请求。
- 想理解安全：先区分 Prompt 建议和代码强制，再读权限、路径、网络和副作用账本。

学习路线不是结束于“跑通 Demo”，而是结束于你能指出 Demo 哪些地方不能直接用于生产，以及 Beaver 的每一层复杂度到底在解决什么。
