# 学习：Agent 循环是怎么工作的

阅读对象：想弄懂“Agent 到底是什么”的人。对应源码：`internal/agent/runner.go` 的 `Runner.Run`。

## 1. 一句话

**Agent = 一个循环：让模型决定下一步，代码负责执行，结果再喂回模型，直到模型不再要求调用工具。**

```text
用户消息 ─► 模型 ─► 要调用工具？ ──否──► 最终回答（结束）
              ▲            │是
              │            ▼
              └──── 执行工具，把结果追加进对话
```

没有魔法：模型只是“输入对话 → 输出文本或工具调用请求”的函数；循环、工具执行、安全限制都在普通代码里。

## 2. 三个核心类型（`internal/agent/types.go`）

| 类型 | 作用 |
| --- | --- |
| `Message` | 对话中的一条消息，角色为 system / user / assistant / tool；assistant 消息可携带 `ToolCalls` |
| `Tool` | 一个工具 = `ToolSpec`（名字、描述、JSON Schema，告诉模型怎么用）+ `Run`（真正干活的 Go 函数，返回文本） |
| `Model` | 只有一个方法 `Generate(ctx, Request) (Response, error)`；OpenAI / Anthropic / Ollama 各写一个适配器 |

Skill 只是提示词，MCP 服务器只是“动态注册的一批 Tool”，所以循环本身不需要为它们加分支。

## 3. 读源码的顺序

`Run` 的主体现在只有四步，每一步对应一个小函数：

1. **选工具** `chooseTools`：默认给全部；用户 `@tool` 指定则只给这些；最后一步或连续失败则一个都不给，逼模型直接回答。
2. **问模型** `generateWithRecovery` → `callModel`：只对三类明确可恢复的错误重试（临时网络/429/5xx、流式兼容问题、上下文超长→压缩），其他错误直接返回。
3. **没有工具调用** → 取 `assistant.Content` 作为最终答案返回。
4. **有工具调用** `executeToolCall`：先拦截“已经失败过的相同调用”，再走审批 `OnToolApproval`，再真正执行 `runTool`（带超时），结果以 `tool` 消息追加到历史。`updateStrategy` 据本步结果调整下一步策略。

状态都收在 `runState` 里，字段旁有注释，可以对照阅读。

## 4. 为什么需要这些“护栏”

小模型和不稳定的网络会出各种问题，Runner 用少量通用规则兜底，而**不**根据用户文字内容做关键词路由（见 `design.md`）：

| 问题 | 护栏 |
| --- | --- |
| 无限循环 | `maxSteps` 上限，最后一步不再暴露工具 |
| 同一个错误调用反复重试 | `failedCalls` 记录，重复失败直接拒绝并提示换思路 |
| 连续两步全失败 | `forceConverge`：不再给工具，要求用已有信息收敛 |
| 只看了搜索摘要就作答 | `DiscoveryOnly` 工具（`web_search`）之后提醒一次去读原文 |
| 工具太多，Schema 吃光上下文 | `load_tools` 渐进披露：先给小核心，需要时再加载工具组 |
| 危险操作 | `OnToolApproval` 审批回调 + 权限策略（`internal/permissions`） |

## 5. 动手练习

1. 在 `internal/builtin/tools/time.go` 旁新增一个 `Tool`（例如返回随机数），在 `catalog.go` 注册，观察模型何时调用它。
2. 在 `runner_test.go` 里找一个“假模型”测试，仿写一个：第一步返回工具调用，第二步返回最终文本，验证 `Steps == 2`。
3. 把 `defaultMaxSteps` 改成 2，让任务需要 3 步，观察“最后一步不暴露工具”的行为。
4. 阅读 `internal/builtin/tools/web.go`：它是一个完整的、带注释的工具范例。

下一篇：[学习：任务规划 todo_write](02-todo.md)。
