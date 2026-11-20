# 多入口、队列与微信：把 Agent 变成应用

前四篇关注 Agent 自己怎样思考和调用工具。这一篇看 Runner 外面的世界：用户从哪里提交任务、多个任务怎样排队、身份怎样绑定、重启后怎样恢复，以及结果怎样回到微信。

对应源码：

- `internal/server/turn_queue.go`：持久化排队与一轮执行；
- `internal/server/task_scheduler.go`：进程内并发和资源互斥；
- `internal/server/turn_execution.go`：选择 Runtime；
- `internal/server/weixin_*.go`：微信收件、命令、提交和投递；
- `internal/server/automation_scheduler.go`：定时触发；
- `internal/store/`：Session、Message、Event 和队列状态。

## 1. 阅读目标

读完后，你应该能解释：

1. Web、微信和 Automation 为什么能复用同一个 Runner。
2. SQLite 中的 `queued` 和 Scheduler 的“正在等待执行槽”为什么不是同一个概念。
3. 为什么同一工作区和同一 Ollama endpoint 默认串行。
4. 微信未知命令、普通路径和普通任务怎样区分。
5. 用户在旧任务结束前追问时，为什么结果不会被投递协程漏掉。
6. 为什么重启可以恢复 queued 任务，却不能盲目重放 running 工具链。

## 2. Runner 不应该知道入口

`internal/agent.Runner` 只处理一次 Run：给它消息、工具和回调，它返回答案。它不知道提问者是谁，也不知道任务来自网页、微信还是时钟。

```text
Web 页面 ─────► 创建/续写普通会话 ─┐
微信消息 ─────► 创建/续写微信会话 ─┼─► SQLite queued
Automation ──► 创建 automation 会话 ─┘        │
                                                  ▼
                                      Scheduler 取得执行槽
                                                  │
                                                  ▼
                                      executeSessionTurn
                                          ├─ Beaver Runtime
                                          └─ Codex Runtime
```

三种入口最终都创建 `Session`、追加 user 消息并调用同一条排队路径。差异留在入口边缘：

- Web 通过 API 和 SSE 展示状态与流式输出。
- 微信先发送接收确认，结束后额外投递最终文字。
- Automation 每次触发创建 `channel=automation` 的会话，完整结果在页面中查看；任务也可以勾选微信成员，在成功、失败或停止后主动推送结果。

## 3. 一条 Web 或微信任务怎样进入队列

核心入口是 `enqueueTurn`：

1. 拒绝同一会话仍在结束中的重复提交。
2. 校验 Beaver 模型配置已经验证可用。
3. 在 SQLite 中写入 user 消息、模型设置快照和 `queued` 状态。
4. 调 `startQueuedTurn` 启动当前进程中的等待协程。

先落库、再启动协程很重要。进程如果在两者之间退出，下一次启动仍能从 SQLite 找到 queued Session。

### 3.1 queued 状态表示什么

`queued` 是持久化事实：这轮已经提交，但尚未进入 `running`。它可能只持续几毫秒，因为任务协程刚启动、还没来得及更新状态。

### 3.2 Scheduler waiting 表示什么

`taskScheduler.waiting[sessionID]` 是进程内事实：该任务已经尝试获取执行槽，但因为以下任一条件不满足而真的在等待：

- 总活跃任务数达到并发上限；
- 某个互斥资源键正在被占用。

所以“数据库当前是 queued”不能直接翻译成“正在排队等待别人”。微信排队提示会短暂观察 `scheduler.isWaiting(sessionID)`；如果 Session 已经转为 running，便只发送普通接收确认。

## 4. Scheduler 的两个职责

### 4.1 总并发上限

默认最多同时运行 4 个任务，设置范围由 Store 常量约束。`acquireFor` 在获得名额时增加 `active`，结束后 `release` 并唤醒等待者。

这不是 goroutine 数上限。收件、SSE、轮询等后台 goroutine 不计入 Agent 任务并发；这里只限制真正占用模型与工作区的 Turn。

### 4.2 资源互斥

不同任务即使没有达到总并发上限，也可能因为共享资源串行：

- 同一未隔离工作区不能同时修改。
- 项目包含共享源文件夹时，同项目任务串行。
- Beaver Runtime 使用同一 Ollama endpoint 时默认串行，避免单机模型显存和首 Token 延迟恶化。
- 远程 Provider 和 Codex 仍受总并发上限，但不会因为 Ollama endpoint 键而串行。

Worktree 只能隔离被复制的仓库；项目额外挂载的共享目录仍是真实共享资源，不能因为“开启了 worktree”就假设全部路径安全并行。

### 4.3 取消等待

`acquireFor` 同时监听 Scheduler 状态变化和 `ctx.Done()`。排队阶段使用的是 task context，因此用户停止或服务关闭会让等待中的任务退出，不会永久占据 waiting map。可配置的整轮超时在获得执行槽后才开始计算，排队时间不计入其中。

运行调度测试：

```bash
go test ./internal/server -run 'TestTaskScheduler' -v
go test ./internal/server -run 'TestTaskResourceKeys' -v
```

这些测试分别覆盖总并发、相同资源串行、多个共享资源和取消。

## 5. 一条微信消息的完整旅程

| 步骤 | 行为 | 源码 |
| --- | --- | --- |
| 1 拉取 | 长轮询获取更新，按 sequence 排序 | `weixin_inbox.go: poll` |
| 2 过滤 | 检查通道/账号启用、消息来源、重复 ID、历史时间 | `handleMessage` |
| 3 解码 | 处理文字、图片、PDF、文本文件和带微信文字的语音 | `decodeWeixinMessage` |
| 4 命令 | 明确命令由代码处理，不启动 Agent | `weixin_commands.go` |
| 5 提交 | 找到或创建当前 Session，写消息并入队 | `weixin_tasks.go: submit` |
| 6 确认 | 先回复“任务已接收”，确实等待时附加排队提示 | `weixin_inbox.go` |
| 7 执行 | 与 Web 共用 `startQueuedTurn → executeSessionTurn` | `turn_queue.go` |
| 8 投递 | 等任务结束，把最终文字分块发送 | `weixin_delivery.go` |

### 5.1 为什么先过滤再解码

重复、历史或非当前用户的消息不应下载附件、更不应启动 Agent。过滤越靠前，越少产生不必要的网络、存储与模型成本。

消息 ID 会与 `PendingMessageID`、`DeliveredMessageID` 比较，防止相同更新重复执行。启用通道时的 `IgnoreBefore` 还会阻止离线期间积压的旧消息突然变成新任务。

### 5.2 语音为什么不自行做识别

当前只使用微信已经提供的文字。语音没有文字时，系统提示用户补发说明，不下载音频，也不运行本地语音识别。这是明确的产品与隐私边界，不是解码失败后悄悄降级。

## 6. 命令边界要精确

已知命令：

```text
/help
/new
/status
/stop
/projects
/project
/project 项目名
```

解析规则不是“所有以 `/` 开头都吞掉”：

| 输入 | 结果 | 原因 |
| --- | --- | --- |
| `/status` | 命令 | 已知且不带多余参数 |
| `/project Beaver Web` | 命令 | `/project` 允许项目名 |
| `/stpo` | unknown | 形如 `/[a-z]+`，明显像拼错命令 |
| `/stop now` | unknown | 已知命令不接受参数，避免歧义 |
| `/usr/bin/env 怎么用` | 普通任务 | 路径不匹配单个英文斜杠命令 |
| `帮我停止服务` | 普通任务 | 不用关键词猜控制意图 |

unknown 会立即回复“无法识别这个命令”，不会提交给 Agent。这样既不会让拼错的 `/stop` 变成模型任务，也不会误吞用户询问文件路径的正常内容。

运行解析测试：

```bash
go test ./internal/server -run TestWeixinSlashCommands -v
```

## 7. 多成员身份和当前会话

每个微信绑定账号有自己的：

- `UserID` 与通道凭据；
- 当前 `SessionID`；
- 默认项目；
- 待投递/已投递消息位置；
- 忽略旧消息的时间边界。

成员 ID 由微信用户 ID 哈希得到（`weixinMemberID`），所以同一个人重新扫码绑定，ID 不变，个人记忆和定时任务的通知对象都不会失效。提交任务时，会话 owner 在应用层绑定；Memory Tool 通过闭包取得 owner，模型参数里没有“切换成另一个成员”的字段。

同一成员的追问通常继续当前 Session；`/new` 清空当前会话指针，让下一条消息创建独立 Session。当前任务仍活跃时不允许 `/new`，必须先结束或 `/stop`，避免“当前会话”在投递期间被悄悄替换。

当前产品边界仍是单管理员、多个微信成员共享一套服务配置，不提供 RBAC 或多租户隔离；微信端也没有工具审批通道。

## 8. 为什么投递要循环

`resumeDelivery(accountID)` 保证同一账号只有一个投递协程。`delivery[accountID]` 的 bool 还是一个“再跑一轮”的唤醒位：活跃协程存在时的新请求不会只返回，而会把它设为 true。投递逻辑可概括为：

```go
for {
    for manager.deliverPending(accountID) {
    }
    // 与 resumeDelivery 共用同一把锁：有唤醒就清零并继续，
    // 没有唤醒才删除所有权并退出。
    if consumeWakeupOrRelease(accountID) == released {
        return
    }
}
```

考虑时间线：

```text
T1  用户提交问题 A
T2  投递协程等待 A
T3  A 刚结束，用户又提交追问 B
T4  协程发送 A
T5  协程重新读取账号，发现 B 仍待投递
T6  等待并发送 B
T7  没有更多待发结果且没有新唤醒，原子释放所有权后退出
```

只循环 `deliverPending` 仍不够：最后一次检查返回“没有待发结果”到协程删除所有权之间，还有一个很窄的退出窗口。B 若恰好在这时到达，只看“协程存在”就返回，旧协程随后退出，仍会漏发。唤醒位与所有权释放共用 `manager.mu`，保证新请求要么被旧协程再处理一轮，要么在所有权删除后启动新协程。

等待任务结束时用的是 `waitWithProgress`：每 2 分钟检查一次 `taskManager.progress`，进度和上次推送不同才发一条“仍在执行”，最多 3 条。条数设上限是因为 iLink 对同一条入站消息的 `context_token` 只允许约 10 次回复：接收确认 1 条、进度最多 3 条，剩下的额度留给分段的最终结果。

`deliverPending` 在真正发送前会重新读取账号和 Session，避免使用过期状态；分段发送失败会有限重试，成功后才更新 `DeliveredMessageID`。

微信只投递最终可见文字，不把完整 Trace 发到聊天中。详细模型、工具和错误事件仍留在 Web 会话页面。

运行相关测试：

```bash
go test ./internal/server -run 'TestWeixin(FinalText|Acknowledgement|MessageFiltering|APIShowsCurrentTask|DeliveryWakeup)|TestLongTaskPushesProgress' -v
```

## 9. Automation 也是创建会话

`automationLoop` 启动后会立即检查一次到期任务，之后每 15 秒检查。到期时：

1. 计算下一次运行时间并原子标记任务已触发。
2. 读取项目、工作区和模型 Profile。
3. 创建 `channel=automation` 的普通 Session。
4. 在启动 goroutine 前先把 LastSessionID 与任务记录关联。
5. 调用同一个 `enqueueTurn`。

每次运行都生成新 Session，因此历史、Trace、Usage、停止和失败语义与手动任务一致。Automation 列表只保存最近一次运行状态和 Session ID，不替代完整会话记录。

### 9.1 结果怎样通知到微信

任务记录里有 `NotifyWeixin`（成员 ID 列表，在任务编辑框里勾选）。会话结束时 `recordAutomationSessionResult` 会调用 `notifyAutomationResult`：按会话 ID 反查任务，只在 completed / failed / canceled 这三个终态推送，并复用微信投递的 `sendChunks` 做分段和重试。任务连会话都没能创建（例如模型配置失效）时，`notifyAutomationFailure` 也会通知。

注意主动推送用的是成员最近一条消息的 `context_token`，成员很久没和机器人互动时会被微信拒收，日志里会有一条失败记录。

关于限制，需要说明来源：腾讯没有公开文档写明具体数字，下面是 [Tencent/openclaw-weixin](https://github.com/Tencent/openclaw-weixin) 仓库里开发者的实测反馈（issue #81、#225），不是官方承诺，行为可能变化：

- 同一条入站消息的 `context_token` 连续回复约 10 条后，服务端返回 `ret=-2`（rate limited）。
- 没有新的入站消息时，`context_token` 约 24–48 小时后过期，同样返回 `ret=-2`，且错误信息为空，只有成员再发一条消息才能恢复。
- 官方客户端也是按“账号 + 用户”保存最近一条 token 并复用于主动发送，Beaver 的做法与它一致。

所以“定时任务推送”只适合成员每天至少和机器人互动一次的场景；这是协议限制，不是 Beaver 的缺陷。

验证稳定成员 ID 和通知选择：

```bash
go test ./internal/server -run 'Test(WeixinMemberIDIsStableAcrossRebinding|AutomationResult)' -v
```

Cron 解析测试：

```bash
go test ./internal/server -run 'Test(NextAutomationRunWithCron|ParseAutomationCronRejectsInvalidExpression)' -v
```

## 10. 重启与副作用边界

服务启动时可以扫描并恢复 queued Session，因为它们还没获得执行槽。running Session 不能直接从头重放：某个写工具可能已经产生副作用，只是结果没来得及落库。

异常结束时，Beaver 会把未完成的 `tool_operations` 标记为 unknown，并提示为避免重复副作用不会自动重放。用户可以检查真实世界状态后再决定是否重新发送。

这也是为什么“持久化队列”与“内存 Scheduler”要分开：

- Store 决定重启后知道什么。
- Scheduler 决定当前进程现在允许谁运行。
- 副作用账本决定异常后哪些动作不能盲目重试。

## 11. 推荐的源码阅读顺序

不要从 `weixin_manager.go` 开始把所有文件扫一遍。沿一条真实消息走：

1. `weixin_inbox.go: handleMessage`
2. `weixin_commands.go: handleCommand`
3. `weixin_tasks.go: submit`
4. `turn_queue.go: enqueueTurn`
5. `task_scheduler.go: acquireFor`
6. `turn_execution.go: executeSessionTurn`
7. `beaver_runtime.go: runBeaverTurn`
8. `weixin_delivery.go: resumeDelivery`

在每一步记下：当前有哪些持久化事实、哪些只是进程内状态、失败后谁负责回复用户。

## 12. 练习与答案方向

### 练习 1：为什么确认消息要先于投递协程

答案方向：极短任务可能瞬间结束。如果先启动结果等待，最终答案可能先发出，随后才出现“任务已接收”，用户看到的时间顺序会反过来。

### 练习 2：数据库是 queued，为什么不一定提示排队

答案方向：任务协程刚创建、尚未把状态改成 running 的瞬间也是 queued；只有 Scheduler 明确记录它因名额或资源冲突等待，才是真正需要提示的排队。

### 练习 3：两个任务使用不同 worktree，是否一定能并行

答案方向：不一定。项目的额外共享源文件夹仍产生同一个资源键；同一 Ollama endpoint 也会串行；总并发上限仍然适用。

### 练习 4：怎样增加微信危险工具审批

至少需要持久化待审批状态、把 Tool Call 以可理解方式发送给正确成员、处理批准/拒绝/超时、在重启后恢复，并把结果接回 `OnToolApproval`。不能只在微信回复文本里写“请确认”而让原 Run 无状态等待。

完成后回到 [课程主页](README.md)，或阅读 [运行原理](../runtime.md) 继续理解上下文、Skill、MCP 和 Trace。
