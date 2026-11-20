# 精读 Beaver 的 Agent 循环

这篇是生产源码导读。第一次接触 Tool Loop，请先完成 [6 小时从 0 到 1 手搓一个 Agent](00-build-an-agent-in-6-hours.md) 的前两小时；先在教学版看清骨架，再读这里的恢复、持久化和策略状态，会容易很多。

对应源码：

- `internal/agent/types.go`：核心协议；
- `internal/agent/runner.go`：循环与护栏；
- `internal/agent/runner_test.go`：行为规格；
- `internal/server/beaver_runtime.go`：应用层如何组装 Runner。

## 1. 阅读目标

读完后，你应该能回答：

1. 一次 Run、一个 Step 和一次 Attempt 有什么区别？
2. 哪些错误会在同一个 Step 重试，哪些会立即失败？
3. assistant Tool Call 和 tool 结果何时落库？
4. Loader、MCP 和普通 Tool 为什么不需要三套循环？
5. 最后一步、连续失败和来源未核验分别怎样改变下一次请求？

## 2. 先记住不变的骨架

```text
用户消息 ─► 选择本步可见工具 ─► 调用模型
                                  │
                  ┌───────────────┴───────────────┐
                  │                               │
             最终回答                         Tool Call
                  │                               │
                  ▼                               ▼
               返回结果          校验/审批/执行/保存 Tool Result
                                                  │
                                                  └──► 下一 Step
```

`Runner.Run` 的职责始终只有“选工具、问模型、执行工具、决定是否继续”。Skill 通过提示词指导模型；内置能力和 MCP 最终都注册为 `agent.Tool`；业务入口和 SQLite 在 `internal/server` 外层组装。

## 3. 核心类型怎样分层

### 3.1 Message 与 Tool Result

`Message` 支持 `system/user/assistant/tool` 四种角色。相比教学版，Beaver 还保留：

- `Parts`：协议无关的多模态内容块；
- `Attachments`：用户附件；
- `ToolResult`：结构化内容、错误和截断信息；
- `Reasoning` / `ReasoningDetails`：只用于同一轮协议续接，不写入公开 Trace。

`ToolCallID` 仍然是工具结果与调用配对的关键字段。不要把工具输出伪装成 user 文本。

### 3.2 ToolSpec 与 Tool

`ToolSpec` 中真正发送给 Provider 的标准字段只有名字、说明和参数 Schema。其他字段是 Beaver 本地策略元数据：

| 字段 | 用途 |
| --- | --- |
| `ActivityKind/Source/DisplayName` | Trace 和页面展示 |
| `Group/GroupDescription` | 渐进加载时组织工具目录 |
| `Loader` | 这个工具只负责加载真实能力 |
| `DiscoveryOnly` | 成功结果只是候选线索，不能单独支撑外部事实 |
| `Auxiliary` | 规划、时间、计算、记忆等辅助结果，不代表已读取外部来源 |

`Tool` 可以实现简单的 `Run → string`，也可以实现 `Execute → ToolResult` 保留结构化、多段或二进制结果。

### 3.3 Model、Request 与 Response

Runner 只依赖：

```go
type Model interface {
    Generate(context.Context, Request) (Response, error)
}
```

`Request.Messages` 是完整历史；`NewMessages + PreviousResponseID` 允许具备 continuation 能力的 Provider 只续接增量。是否支持流式、原生工具、结构化结果、多模态和服务端续接，由 Adapter 的 capability 显式声明。

协议 JSON 不进入核心类型：Chat Completions、Responses、Ollama 和 Anthropic 各自在 Adapter 边界编码。

## 4. 用一次两步任务精读 `Run`

假设用户要求读取文件并总结：

```text
Step 1：模型调用 read
Step 2：模型基于结果回答
```

### 4.1 初始化 `runState`

`runState` 保存一次 Run 内随步骤变化的状态：

- 完整 `messages` 与本步增量 `pending`；
- continuation 的 `previousResponseID`；
-累计 Usage；
- 用户显式选择的工具；
- 已失败调用和连续失败次数；
- Loader 后必须调用真实工具的约束；
- 搜索发现后是否仍待来源核验。

状态集中在一个结构体中，避免主循环散落十几个局部布尔值。

### 4.2 `chooseTools` 选择本步动作空间

规则按优先级叠加：

1. 默认暴露本轮已注册的全部工具，`tool_choice=auto`。
2. 用户用 `@tool` 指定时，只暴露所选工具，并验证首轮确实调用。
3. Loader 刚加载能力后，下一步隐藏 Loader，要求模型使用真实工具。
4. 连续失败需要收敛或已经是最后一步时，不再暴露工具。

最后一步不给工具不是为了省 Token，而是避免模型产生一个没有后续 Step 可读取结果的新调用。

### 4.3 `generateWithRecovery` 在同一步内有界恢复

一次 Step 可能有多个 Attempt：

| 失败 | 行为 | 为什么 |
| --- | --- | --- |
| 429、5xx、临时网络错误 | 等待后原样重试一次 | 典型瞬时失败 |
| 特定流式空响应/兼容错误 | 关闭流式再试 | 部分兼容 Provider 的传输问题 |
| Provider 报上下文超长 | 调 `PrepareRequest` 压缩后再试 | 请求形态改变但任务目标不变 |
| 认证、参数、模型不存在等确定性 4xx | 立即失败 | 重放只会浪费时间和 Token |
| 截断或不完整 Tool Call | 立即失败且绝不执行 | 参数可能只生成了一半 |

每个真实请求都会发出 model start/end Event；重试不会伪装成新的 Step。

### 4.4 模型没有 Tool Call

通常这意味着最终回答，但 Runner 还会检查：

- 用户显式指定工具时，首步是否真的调用；
- Loader 加载后，是否调用了真实能力；
- 搜索发现阶段之后，是否需要提醒读取原始来源；
- 正文是否为空。

通过所有检查后，才返回 `RunResult`。

### 4.5 模型有 Tool Call

Runner 会先为缺失的调用 ID 补值，并附加页面展示元数据，然后把每个调用交给 `executeToolCall`：

1. 拦截已失败过的完全相同调用，防止原样死循环。
2. 查找已注册 Tool；未知名字形成结构化失败。
3. 运行 `OnToolApproval`；拒绝时工具不会开始。
4. `runTool` 用带 deadline 的上下文执行，并校验结果大小。
5. `OnToolLifecycle` 在副作用前后记录操作状态。
6. 生成与调用 ID 配对的 `role=tool` 消息。

一个 Step 的 assistant 消息和全部 tool 结果收齐后，通过 `OnTurnMessages` 一批交给应用层保存，避免数据库留下半条工具链。

### 4.6 `updateStrategy` 决定下一步

它根据本 Step 的结果更新：

- 是否只有 Loader、下一步必须调用真实工具；
- 是否发生发现阶段结果、需要来源核验；
- 是否成功执行过真实工具；
- 是否连续所有调用都失败；
- 是否需要停止暴露工具并收敛。

当前代码使用 `DiscoveryOnly` 与 `Auxiliary` 推导来源核验状态。计划中的更精确标记在真正合入前不应写成现状。

## 5. Runner 与应用层的边界

`RunRequest` 的回调是关键接缝：

| 回调 | 应用层用途 |
| --- | --- |
| `OnTextDelta` / `OnTextReset` | 流式页面展示与纠偏时清空临时正文 |
| `OnToolApproval` | 等待用户审批危险工具 |
| `OnToolLifecycle` | 工具副作用开始/结束账本 |
| `OnTurnMessages` | 原子保存完整 assistant/tool 链 |
| `PrepareRequest` | 正常或强制上下文压缩 |
| `IsContextError` | 识别 Provider 的上下文超限错误 |

Runner 不直接 import Store、HTTP Server 或 UI。`internal/agent/architecture_test.go` 会检查这个依赖方向。

## 6. 按行为读测试，而不是从上到下扫文件

建议分四组运行：

### 第一组：最小循环

```bash
go test ./internal/agent -run 'TestRunner(FeedsToolResultBackAsToolMessage|DisablesToolsOnLastStep)$' -v
```

### 第二组：模型恢复

```bash
go test ./internal/agent -run 'TestRunner(RetriesTransientProviderErrorInSameStep|DoesNotRetryDeterministicProviderError|RetriesContextErrorAfterPreparation)$' -v
```

### 第三组：失败与收敛

```bash
go test ./internal/agent -run 'TestRunner(ReturnsToolErrorsToModelForRecovery|ConvergesAfterRepeatedFailedToolSteps|RejectsTruncatedToolCallWithoutExecutingIt)$' -v
```

### 第四组：持久化和副作用边界

```bash
go test ./internal/agent -run 'TestRunner(PersistsCompletedToolStepAsOneBatch|PersistsToolStartBeforeSideEffect|RejectsOversizedToolResultWithoutPersistingPayload)$' -v
```

每跑一组，先读测试名猜行为，再看 Fake Model 怎样安排响应，最后才回到实现。

## 7. 常见误读

| 误读 | 正确理解 |
| --- | --- |
| `Steps` 是 HTTP 请求数 | Step 是模型决策；重试属于同一步的 Attempt |
| Tool 错误应该直接让 Run 崩溃 | 可恢复的调用失败通常回给模型；状态一致性失败才终止 Run |
| Prompt 已经写了规则，所以无需审批 | Prompt 是建议，权限和审批是代码边界 |
| Loader 成功就说明任务完成 | Loader 只改变下一步可见工具，必须再调用真实能力 |
| Trace 等于保存模型思维过程 | Trace 记录可审计操作，不展示私有推理 |
| 压缩会删除旧消息 | Beaver 保存原始消息，压缩只建立运行时检查点 |

## 8. 动手练习

1. 在教学版 `examples/mini-agent` 写一个空响应测试，再在生产版找到对应的 `TestRunnerMarksEmptyNonStreamingResponseAsError`。
2. 用纸画出“Step 1 有两个 Tool Call，其中一个成功、一个失败”后 `messages` 的顺序。
3. 跟踪 `runBeaverTurn` 中 `OnTurnMessages` 到 Store 的调用，确认完整工具链如何进入一个事务。
4. 阅读 `TestRunnerPersistsToolStartBeforeSideEffect`，解释为什么 tool start 的持久化必须发生在执行器之前。
5. 把 `Step/Attempt/Event` 分别映射到页面 Trace 中可见的字段。

完成后继续：[任务规划 `todo_write`](02-todo.md)，或回到 [课程主页](README.md)。
