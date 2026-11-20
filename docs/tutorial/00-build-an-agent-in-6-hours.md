# 6 小时从 0 到 1 手搓一个 Agent

这是一门“边写边验证”的课程。你会先完成一个只有核心协议、工具和循环的 Mini Agent，再把它逐层映射到 Beaver 的生产实现。

课程的最终产物位于 [`examples/mini-agent/`](../../examples/mini-agent/)。它只使用 Go 标准库，默认不联网、不需要 API Key，并有确定性测试。你可以直接运行最终版本，也可以新建一个练习分支，按本文顺序逐文件手写。

> 6 小时是聚焦学习的参考时间，不是考试倒计时。第一次接触 Go 接口、JSON Schema 或 HTTP 协议，分两天完成反而更容易消化。

## 学完以后，你应该真的会什么

你应该能独立完成下面六件事：

1. 不依赖 Agent 框架，定义消息、工具和模型接口。
2. 写出“模型 → 工具 → 模型”的循环，并说明每个终止条件。
3. 解释 `assistant.tool_calls` 和 `tool.tool_call_id` 为什么必须配对。
4. 把 OpenAI-compatible 的 HTTP JSON 转换成自己的核心类型。
5. 用 Fake Model 测试未知工具、非法参数、超时和最大步数，而不是靠真实模型碰运气。
6. 说清楚最小 Demo 距离生产系统还缺哪些层，以及 Beaver 中对应代码在哪里。

你不会在这 6 小时里实现一个完整的 Beaver。流式、多模态、MCP、数据库恢复、权限审批等会在最后映射，但不会塞进最小循环里。先看清骨架，再理解工程复杂度，这是本课程的主线。

## 最终会得到什么

目录结构：

```text
examples/mini-agent/
├── README.md
├── main.go
├── types.go
├── demo_model.go
├── tools.go
├── runner.go
├── runner_test.go
├── openai_compatible.go
└── openai_compatible_test.go
```

一次完整运行会产生这条消息链：

```text
system("需要精确统计时必须调用 text_stats")
  ↓
user("请统计：Agent = 模型 + 工具 + 循环")
  ↓
assistant(tool_call id=call_demo_1, name=text_stats, arguments={...})
  ↓
tool(tool_call_id=call_demo_1, result={"ok":true,...})
  ↓
assistant("统计完成：……")
```

请先记住一句话：**模型只能提出调用；真正执行工具的是你的程序。**

---

## 开始之前：10 分钟环境检查

以下命令都在仓库根目录执行：

```bash
go version
go test ./examples/mini-agent
```

预期测试以类似下面的内容结束：

```text
ok  github.com/lakernote/beaver-agent/examples/mini-agent
```

如果 Go 尝试下载工具链，原因是根目录 [`go.mod`](../../go.mod) 声明了项目使用的版本。先让这条测试通过，再继续课程；否则后续错误会混入环境问题。

### 推荐的学习方式

有两种都可以：

- **跟读模式**：保留仓库里的完整代码，按课程顺序只打开当前文件；文中的测试命令可直接执行。
- **手写模式**：创建练习分支和空目录 `examples/mini-agent-lab`，依次重写 `types.go → demo_model.go → tools.go → runner.go → runner_test.go → openai_compatible.go → openai_compatible_test.go → main.go`。把文中命令的 `./examples/mini-agent` 换成 `./examples/mini-agent-lab`；某个测试只有在对应文件写完后才运行，最终版本始终留在 `examples/mini-agent` 供比较。

手写模式的第 0 小时仍先运行仓库自带 Demo，明确目标行为；第 1 小时只写 `types.go` 和 `demo_model.go`，用 `go test ./examples/mini-agent-lab` 检查能否编译。第 2 小时写完 Tool、Runner 和测试后，再开始执行带 `-run TestRunner...` 的命令。这样每一步依赖的文件都已经存在。

不要一开始就复制 Beaver 的 `internal/agent/runner.go`。生产 Runner 同时处理重试、压缩、渐进加载、来源核验和持久化回调；这些代码是“升级答案”，不是最适合第一次理解循环的起点。

---

## 第 0 小时：先看见一次完整循环（00:00–00:30）

### 本节目标

- 能运行一个完全离线的 Agent。
- 能从输出中认出四种消息角色。
- 能说出普通聊天调用和 Agent 的最小差异。

### 0.1 运行离线 Demo

```bash
go run ./examples/mini-agent -trace "请统计：Agent = 模型 + 工具 + 循环"
```

关键输出如下，耗时可能不同：

```text
消息轨迹：
1. system
   content: 你是一个教学 Agent。需要精确统计文本时必须调用 text_stats。
2. user
   content: 请统计：Agent = 模型 + 工具 + 循环
3. assistant
   tool_call: id=call_demo_1 name=text_stats arguments={"text":"Agent = 模型 + 工具 + 循环"}
4. tool name=text_stats tool_call_id=call_demo_1
   content: {"bytes":32,"non_space_runes":14,"ok":true,"runes":20}
5. assistant
   content: 统计完成：32 个 UTF-8 字节，20 个 Unicode 字符，其中 14 个不是空白。

最终回答：统计完成：32 个 UTF-8 字节，20 个 Unicode 字符，其中 14 个不是空白。
```

逐条解释：

1. `system` 给模型长期有效的行为规则。
2. `user` 是本轮用户输入。
3. 第一条 `assistant` 没有正文，而是提出一个结构化 Tool Call。
4. `tool` 是本地 Go 函数的执行结果，它用同一个 `call_demo_1` 回答第 3 条调用。
5. 第二条 `assistant` 看见工具结果后，组织最终自然语言回答。

第 3 条不是“模型执行了函数”。模型只生成了名字和 JSON 参数。`Runner` 检查注册表，找到 `text_stats` 的 Go 函数并执行。这个边界决定了模型不能凭空获得本地权限。

### 0.2 Agent 到底比聊天多了什么

普通聊天通常只有一次调用：

```text
messages ─► model ─► assistant text
```

Agent 至少多了两个东西：

```text
                         ┌──────────────────────────┐
                         │                          │
messages + tool schemas ─► model ─► tool calls ─► executor
          ▲              │                          │
          └──────────── tool results ◄─────────────┘
```

- **工具执行器**：真正访问时间、文件、数据库或外部服务。
- **循环**：模型请求工具时继续；模型给最终正文时结束。

“会多轮聊天”不等于 Agent；“模型能输出一段 JSON”也不等于 Agent。关键是代码能识别原生 Tool Call、受控执行，并把结果按协议送回模型。

### 0.3 为什么先用假模型

当前运行使用 [`DemoModel`](../../examples/mini-agent/demo_model.go)，不是神经网络。它第一次固定请求 `text_stats`，第二次固定读取工具结果并回答。

这样做有三个好处：

- 没有网络和费用，任何人都能复现。
- 输出确定，失败时一定是代码问题，不是模型随机性。
- 可以先学循环，再学 Provider 协议，认知负担更小。

Fake Model 不是“假装测试”。Agent 的核心职责就是正确编排模型与工具；用一个可控的 Model 实现测试编排，和数据库测试使用内存替身是同一个思路。

### 本节检查点

先不看答案，口头回答：

1. 哪一条消息真正包含工具结果？
2. 如果把工具结果伪装成 `user` 消息，会丢失什么？
3. 模型生成 `{"name":"text_stats"}` 是否意味着工具已经执行？

答案：

1. `role=tool` 的第 4 条消息。
2. 会丢失与原 Tool Call 的结构化配对，模型协议和审计也会失真。
3. 不意味着；本地代码完成注册表查找、参数校验和执行后才算运行过。

---

## 第 1 小时：定义自己的核心协议（00:30–01:15）

### 本节目标

- 理解 [`types.go`](../../examples/mini-agent/types.go) 的每一个类型。
- 分清核心领域类型和厂商 HTTP JSON。
- 理解为什么 `Model` 应该是接口。

### 1.1 从四种消息角色开始

最小角色定义：

```go
type Role string

const (
    RoleSystem    Role = "system"
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
    RoleTool      Role = "tool"
)
```

| Role | 谁产生 | 用途 | 常见错误 |
| --- | --- | --- | --- |
| `system` | 应用 | 规则和运行环境 | 把权限只写在这里，以为模型一定服从 |
| `user` | 用户 | 目标、问题、附件说明 | 把工具结果伪装成用户补充 |
| `assistant` | 模型 | 最终文本或 Tool Call | 只保留文本，丢掉结构化调用 |
| `tool` | 本地执行器 | 回答某个 Tool Call | 忘记 `tool_call_id` 或配错 ID |

消息类型是：

```go
type Message struct {
    Role       Role
    Content    string
    ToolCalls  []ToolCall
    ToolCallID string
    Name       string
}
```

同一个结构体承载四类消息，但字段含义随角色不同：

- 普通 `system/user/assistant` 主要使用 `Content`。
- 发起调用的 `assistant` 使用 `ToolCalls`。
- 返回结果的 `tool` 使用 `Content`、`ToolCallID` 和 `Name`。

真实 Beaver 的 [`agent.Message`](../../internal/agent/types.go) 还支持多模态 `Parts`、附件、结构化 `ToolResult` 和 Provider 推理续接信息。教学版先不引入，因为它们不改变循环的核心判断。

### 1.2 Tool Call 为什么保留原始 JSON

```go
type ToolCall struct {
    ID        string
    Name      string
    Arguments json.RawMessage
}
```

- `ID`：这次调用的关联 ID，不是工具名，也不是数据库主键。
- `Name`：模型选择的工具名，必须在本地注册表中查找。
- `Arguments`：模型生成的原始 JSON 参数。

参数先保留为 `json.RawMessage`，到真正执行具体工具时才反序列化。原因是不同工具参数结构不同，核心循环不应该知道所有工具的字段；同时保留原始 JSON 有利于 Trace 和错误定位。

不要把参数直接定义成 `map[string]string`。数字、布尔、数组、嵌套对象都会丢失类型，复杂工具很快就无法表达。

### 1.3 Tool Spec 和执行函数必须分开

```go
type ToolSpec struct {
    Name        string
    Description string
    Parameters  map[string]any
}

type Tool struct {
    Spec ToolSpec
    Run  func(context.Context, json.RawMessage) (string, error)
}
```

`ToolSpec` 会发给模型，让模型知道“有哪些能力、参数怎样写”。`Run` 只留在本地，是真正执行能力的代码。

这个分离非常重要：

```text
发给模型：name + description + JSON Schema
留在本地：网络凭据 + 文件权限 + 执行函数
```

模型永远不应该拿到一个 Go 函数或服务器凭据。它只看到契约。

### 1.4 用一个接口隔离模型厂商

```go
type ModelRequest struct {
    Messages []Message
    Tools    []ToolSpec
}

type Model interface {
    Generate(context.Context, ModelRequest) (Message, error)
}
```

Runner 只依赖 `Generate`。因此下面两者对 Runner 来说没有区别：

- `DemoModel`：确定性假模型；
- `OpenAICompatibleModel`：真正发 HTTP 请求的适配器。

这叫依赖倒置，但不用背名词。直觉是：**循环依赖“能生成下一条模型消息”这个能力，而不是依赖某家 SDK。**

Beaver 的接口返回 `Response`，额外包含 Usage、停止原因、原始交换信息和 continuation ID；原理完全相同。

### 1.5 本小时动手

按顺序阅读：

```text
examples/mini-agent/types.go
examples/mini-agent/demo_model.go
```

跟读模式随后执行：

```bash
go test ./examples/mini-agent -run TestRunnerCompletesToolLoop -v
```

手写模式此时 Runner 还没有实现，只检查本小时的两个文件可以编译：

```bash
go test ./examples/mini-agent-lab
```

预期：命令通过，并且没有网络请求。`TestRunnerCompletesToolLoop` 留到第 2 小时写完 `runner_test.go` 后再运行。

试着在 `Message` 中临时删掉 `ToolCallID`，观察哪些测试或代码无法再表达配对关系；理解后恢复，不要保留这个实验改动。

### 常见误区

| 误区 | 为什么错 |
| --- | --- |
| 直接在 Runner 里调用某家 SDK | 核心循环被厂商协议绑死，Fake Model 也难写 |
| Tool Spec 里放秘密 | Spec 会发送到远端模型服务 |
| 用文本正则解析“模型想调用工具” | 正文不等于原生 Tool Call，容易误执行示例代码或提示词注入内容 |
| 所有参数先转字符串 | 丢失 JSON 类型和原始输入，难以验证与审计 |

### 本节检查点

- `ToolSpec` 是给谁看的？`Run` 是给谁执行的？
- 为什么 Model 接口不应该暴露 HTTP Header？
- `ToolCall.ID` 和 `ToolCall.Name` 各自解决什么问题？

答案：Spec 给模型，Run 由本地程序执行；Header 属于 Provider 适配层，不应污染核心循环；ID 关联一次调用与结果，Name 选择注册表中的能力。

---

## 第 2 小时：写出第一个完整 Tool Loop（01:15–02:15）

### 本节目标

- 写出工具注册、参数校验、执行和回传。
- 精确说出循环的继续条件与结束条件。
- 理解最大步数、超时和结构化失败的作用。

### 2.1 先实现一个无副作用工具

课程选择 [`text_stats`](../../examples/mini-agent/tools.go)，因为它能展示完整协议，又不会引入计算器解析、文件权限或网络安全等旁支。

它发给模型的 JSON Schema 等价于：

```json
{
  "name": "text_stats",
  "description": "统计文本的 UTF-8 字节数、Unicode 字符数和非空白字符数",
  "parameters": {
    "type": "object",
    "properties": {
      "text": {
        "type": "string",
        "description": "需要统计的原始文本"
      }
    },
    "required": ["text"],
    "additionalProperties": false
  }
}
```

几个细节：

- `required` 告诉模型必须给 `text`。
- `additionalProperties: false` 减少模型编造无关字段。
- Schema 只帮助模型生成参数，不替代执行端校验。
- 工具返回 JSON 字符串，便于模型和测试稳定解析。

执行器仍会自己检查 `text` 非空。因为模型可能忽略 Schema，攻击者也可以绕过模型直接构造请求。

### 2.2 建立工具注册表

`NewRunner` 把 `[]Tool` 建成 `map[string]Tool`：

```text
text_stats ─► Tool{Spec: ..., Run: runTextStats}
```

初始化阶段拒绝空名字、空执行器和重复名称。越早失败越好；如果等模型调用时才发现两个同名工具，程序已经无法确定该运行哪一个。

发给模型时只从注册表提取 `ToolSpec`，不会发送 `Run`。教学版还对名字排序，让测试和请求顺序保持确定。

### 2.3 核心循环只有四个判断

打开 [`runner.go`](../../examples/mini-agent/runner.go)，先只看 `Run`。把细节折叠后，它等价于：

```go
history := copy(input)

for step := 1; step <= maxSteps; step++ {
    specs := registeredToolSpecs()
    if step == maxSteps {
        specs = nil // 最后一步要求收敛
    }

    assistant := model.Generate(ctx, ModelRequest{
        Messages: history,
        Tools:    specs,
    })
    history = append(history, assistant)

    if len(assistant.ToolCalls) == 0 {
        if strings.TrimSpace(assistant.Content) == "" {
            return error("模型既没有回答，也没有调用工具")
        }
        return assistant.Content
    }

    for _, call := range assistant.ToolCalls {
        result := executeRegisteredTool(call)
        history = append(history, Message{
            Role:       RoleTool,
            Content:    result,
            ToolCallID: call.ID,
            Name:       call.Name,
        })
    }
}
```

真正的代码还处理 `context`、默认 ID、JSON 合法性、工具超时和错误包装，但循环骨架就是这些。

四个核心判断：

1. 外层 Context 是否已经取消？取消就立即退出。
2. 模型有没有 Tool Call？没有就把非空正文当最终回答。
3. Tool Call 是否可安全执行？查注册表、验 JSON、加超时。
4. 是否到最后一步？最后一步不再暴露工具，要求模型基于已有结果回答。

### 2.4 为什么最后一步不再给工具

假设 `MaxSteps=3`，模型每次都调用工具：

```text
Step 1：有工具 → 执行
Step 2：有工具 → 执行
Step 3：不给工具 → 要么回答，要么明确失败
```

如果第三步仍把工具发给模型，模型可能再发起调用；执行后却没有剩余 Step 让它阅读结果并形成最终答案。更糟的是，最后一次副作用已经发生，用户却只收到“达到最大步数”。

Beaver 也在最后一步使用 `tool_choice=none`。生产版还会在连续失败、来源未核验和 Loader 加载后调整下一步策略。

### 2.5 工具失败为什么通常回给模型

未知工具、非法参数、执行错误和超时会形成这样的 Tool Result：

```json
{
  "ok": false,
  "error": {
    "code": "unknown_tool",
    "message": "工具 \"delete_world\" 未注册"
  }
}
```

它仍然是 `role=tool`，仍与原调用 ID 配对。模型看见失败后可以换参数、换工具或向用户解释。

这不代表所有错误都应该吞掉：模型 HTTP 失败、数据库无法持久化、外层取消等系统错误应让 Run 失败。简单判断：

- **某一次工具调用的业务失败**：优先形成 Tool Result。
- **Agent 无法保证状态一致性**：终止本轮并向上返回错误。

### 2.6 超时和取消不是同一层

教学 Runner 有两种取消：

- 外层 `ctx`：用户停止任务或进程关闭，整个 Run 取消。
- 每个工具的 `context.WithTimeout`：到达上限时取消工具上下文；配合取消的工具会立即返回 `timeout`，模型仍可决定下一步。

这就是为什么工具函数第一个参数是 `context.Context`。Go 不能强行抢占一个普通函数；如果工具忽略取消，Runner 只能等它返回，再把已经越过 deadline 的结果标成 `timeout`，底层工作和副作用仍可能已经发生。真正的 I/O 工具必须把 `ctx` 继续传给 HTTP、数据库或子进程 API。

### 2.7 跑四个失败实验

```bash
go test ./examples/mini-agent -run TestRunnerReturnsUnknownToolFailureToModel -v
go test ./examples/mini-agent -run TestRunnerRejectsInvalidArgumentsBeforeExecution -v
go test ./examples/mini-agent -run TestRunnerCancelsSlowTool -v
go test ./examples/mini-agent -run TestRunnerStopsToolCallsAtLastStep -v
```

四条都应通过。它们分别证明：

1. 模型不能调用注册表之外的能力。
2. 非法 JSON 不会进入执行函数。
3. 慢工具能收到取消。
4. 模型不能无限调用工具。

### 常见错误

| 现象 | 最可能原因 | 怎么检查 |
| --- | --- | --- |
| 模型第二步说“没看到结果” | Tool Result 没带原 `tool_call_id` | 打印完整消息轨迹 |
| 工具明明注册却显示未知 | Spec 名称和注册表 key 不一致 | 初始化时统一 trim 并拒绝重复 |
| 参数解析偶尔失败 | 把 `arguments` 当普通文本拼接 | 保留并校验原始 JSON |
| Ctrl+C 后工具仍在跑 | 执行器没监听 `ctx.Done()` | 写一个超时测试 |
| Agent 一直循环 | 没有最大步数或最终收敛步 | 用永远调用工具的 Fake Model 测试 |

### 本节检查点

请在纸上画出一次含两个 Tool Call 的 Step。一个 assistant 消息可以包含两个调用，之后必须有两个分别配对的 tool 结果，下一次模型调用才能看到完整历史。

---

## 第 3 小时：接入真实模型协议（02:15–03:00）

### 本节目标

- 看懂非流式 Chat Completions 的工具调用 JSON。
- 理解 Adapter 为什么不应该决定循环策略。
- 能接入一个支持原生 Function Calling 的 OpenAI-compatible 服务。

### 3.1 Adapter 的唯一职责

[`openai_compatible.go`](../../examples/mini-agent/openai_compatible.go) 做两次转换：

```text
核心 ModelRequest ──编码──► HTTP JSON
核心 Message      ◄─解码── HTTP JSON
```

它负责 URL、Header、HTTP 状态码和 wire format；不负责工具执行、最大步数或业务权限。这些仍属于 Runner 和应用层。

### 3.2 第一次请求长什么样

省略 Schema 细节后，请求类似：

```json
{
  "model": "your-model",
  "messages": [
    {"role": "system", "content": "需要精确统计时必须调用 text_stats"},
    {"role": "user", "content": "请统计：Agent"}
  ],
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "text_stats",
        "description": "统计文本……",
        "parameters": {"type": "object", "properties": {}}
      }
    }
  ],
  "tool_choice": "auto",
  "stream": false
}
```

模型决定使用工具时，响应类似：

```json
{
  "choices": [
    {
      "finish_reason": "tool_calls",
      "message": {
        "role": "assistant",
        "content": "",
        "tool_calls": [
          {
            "id": "call_abc",
            "type": "function",
            "function": {
              "name": "text_stats",
              "arguments": "{\"text\":\"Agent\"}"
            }
          }
        ]
      }
    }
  ]
}
```

注意 `arguments` 在 Chat Completions wire format 中通常是“装着 JSON 的字符串”。Adapter 解开外层字符串后，核心层仍保存 `json.RawMessage`。不要把 wire format 直接扩散到 Runner。

### 3.3 第二次请求必须带完整工具链

教学 Adapter 使用完整历史，因此第二次请求至少包含：

```json
[
  {"role": "user", "content": "请统计：Agent"},
  {
    "role": "assistant",
    "tool_calls": [
      {
        "id": "call_abc",
        "type": "function",
        "function": {
          "name": "text_stats",
          "arguments": "{\"text\":\"Agent\"}"
        }
      }
    ]
  },
  {
    "role": "tool",
    "tool_call_id": "call_abc",
    "content": "{\"ok\":true,\"runes\":5}"
  }
]
```

只发送 tool 结果、忘记前面的 assistant Tool Call，很多 Provider 会返回 400，因为结果没有可关联的调用。

### 3.4 不联网验证 Adapter

先执行：

```bash
go test ./examples/mini-agent -run TestOpenAICompatible -v
```

测试用 `httptest.Server` 在本机模拟 Provider，验证：

- Base URL 为测试服务器的 `/v1` 时，请求路径是 `/v1/chat/completions`；
- Authorization Header 正确；
- 工具 Schema 和 `tool_choice=auto` 被编码；
- 返回的 Tool Call 被解码成核心类型；
- Tool Result 的 `tool_call_id` 没有丢失。

这比直接请求真实 API 更适合做协议单元测试：快、免费、不会因模型行为变化而随机失败。

### 3.5 可选：接真实 OpenAI-compatible 服务

准备一个支持原生 Function Calling 的服务，然后运行：

```bash
MINI_AGENT_MODE=openai \
MINI_AGENT_BASE_URL=http://127.0.0.1:11434/v1 \
MINI_AGENT_MODEL='<支持工具调用的模型名>' \
MINI_AGENT_TRACE=1 \
go run ./examples/mini-agent "请统计：工具结果必须回传给模型"
```

如果服务需要鉴权，再设置：

```bash
export MINI_AGENT_API_KEY='<你的 API Key>'
```

不要把真实 Key 写入仓库、教程截图或命令示例。示例程序不会打印 Key，但环境变量本身仍需由你的运行环境保护。

教学 Adapter 只允许本机回环地址使用明文 HTTP；连接其他主机必须使用 HTTPS，避免 Prompt、工具结果和 Bearer Token 在网络中明文传输。Base URL 也不能附带 query、fragment 或用户信息，HTTP 客户端不会自动跟随重定向，避免跳转绕过这些检查。

### 3.6 常见 HTTP 问题

| 现象 | 常见原因 | 检查顺序 |
| --- | --- | --- |
| 401/403 | Key 无效或权限不足 | Header、Key 所属项目、服务端日志 |
| 404 | Base URL 层级错误 | 确认最终地址是否为 `.../v1/chat/completions` |
| 400 tools unsupported | 模型或兼容层不支持原生工具 | 换支持 Function Calling 的模型/端点 |
| `choices` 为空 | 兼容层响应不是标准格式 | 保存脱敏响应，核对 Provider 文档 |
| arguments 非法 JSON | 模型输出或兼容层编码异常 | 查看原始响应，不要直接执行 |
| 第一步有调用，第二步 400 | assistant 调用与 tool 结果没有完整配对 | 打印发出的完整 messages |

### Beaver 对照

教学 Adapter 故意只有非流式 Chat Completions。Beaver 的 Provider 层还处理：

- 流式 SSE 聚合和实时文本增量；
- Usage、停止原因和上下文错误归一化；
- Chat Completions、Responses、Ollama、Anthropic 的差异；
- 某些 Provider 的服务端 continuation；
- 多模态输入和结构化 Tool Result。

入口分别在 `internal/agent/openai/`、`internal/agent/ollama/`、`internal/agent/anthropic/`。尽管协议更多，`Runner` 仍只依赖 `Model.Generate`。

---

## 第 4 小时：让行为可验证，而不是“看起来能跑”（03:00–04:00）

### 本节目标

- 会用脚本化 Fake Model 精确安排每一步响应。
- 理解正常路径测试为什么远远不够。
- 区分 Step 和同一步内的 Attempt。

### 4.1 一个 Tool Loop 测试怎样工作

打开 [`runner_test.go`](../../examples/mini-agent/runner_test.go) 的 `TestRunnerCompletesToolLoop`。测试模型第一次返回 Tool Call，第二次检查历史末尾是否为正确配对的 Tool Result，再返回最终回答。

逻辑可以概括成：

```go
modelCalls := 0

model := modelFunc(func(ctx context.Context, req ModelRequest) (Message, error) {
    modelCalls++
    switch modelCalls {
    case 1:
        return Message{ToolCalls: []ToolCall{{
            ID: "call_test", Name: "text_stats",
            Arguments: json.RawMessage(`{"text":"Agent"}`),
        }}}, nil
    case 2:
        assertLastMessageIsToolResultFor("call_test")
        return Message{Content: "Agent 有 5 个 Unicode 字符。"}, nil
    default:
        return Message{}, errors.New("unexpected extra model call")
    }
})
```

这个测试同时证明：

- 第一次请求携带工具 Schema；
- 工具确实被执行；
- 结果 ID 和调用 ID 相同；
- Runner 进行了第二次模型调用；
- 最终正文正确结束循环；
- 没有多余的第三次调用。

真实模型测试很难稳定证明这些事情。它可能今天选工具、明天直接心算；即使最终文本正确，也可能根本没走你想验证的路径。

### 4.2 最少要覆盖的失败矩阵

| 场景 | 期望 | 示例测试 |
| --- | --- | --- |
| 模型直接回答 | 一步结束 | 可自行补充 |
| 正常 Tool Call | 执行、配对、再次问模型 | `TestRunnerCompletesToolLoop` |
| 未注册工具 | 不执行，返回结构化失败 | `TestRunnerReturnsUnknownToolFailureToModel` |
| 非法 JSON | 不进入执行器 | `TestRunnerRejectsInvalidArgumentsBeforeExecution` |
| 工具超时 | 取消上下文，拒绝使用越过 deadline 的结果 | `TestRunnerCancelsSlowTool`、`TestRunnerRejectsLateSuccessAfterToolDeadline` |
| 一直调用工具 | 最后一步拒绝继续 | `TestRunnerStopsToolCallsAtLastStep` |
| 模型空响应 | Run 明确失败 | 建议练习 |
| 模型 HTTP 错误 | 原样向上返回并带上下文 | Adapter 测试可补充 |

### 4.3 Step 和 Attempt 不要混淆

在教学版里，每个 Step 只调用模型一次，所以两者看起来相同。生产系统会出现：

```text
Turn 7
└── Step 2：模型决定下一步
    ├── Attempt 1：HTTP 429
    └── Attempt 2：重试成功，返回 Tool Call
```

`MaxSteps` 应限制模型决策链长度，不应该因为一次网络重试就多算一个 Step。否则网络抖动会改变 Agent 可完成任务的深度。

Beaver 的 `generateWithRecovery` 只对明确可恢复的情况进行有界补救，例如 429/5xx、特定流式兼容问题和上下文超限压缩。确定性 4xx 不会盲目重放。

### 4.4 工具错误和副作用

教学工具没有副作用，重试风险很低。真实工具可能已经发邮件、提交代码或修改数据库，但响应在返回前丢失。此时你不知道副作用是否发生，不能简单重试。

因此生产系统需要区分：

- 读取类工具：通常可以安全重试；
- 幂等写入：有稳定幂等键时可以有界重试；
- 非幂等副作用：结果未知时必须停止、审计或让人确认。

Beaver 的 Trace 用于展示和审计，`tool_operations` 副作用账本用于记录工具开始与结束；两者不是一回事。

### 4.5 本小时实验

运行全部样例测试和竞态检查：

```bash
go test ./examples/mini-agent -v
go test -race ./examples/mini-agent
go vet ./examples/mini-agent
```

然后自行补一个“模型返回空消息”的测试，期望错误包含：

```text
模型既没有回答，也没有调用工具
```

参考思路：Fake Model 永远返回 `Message{}`，Runner 不应把空字符串当成功答案。

### 本节检查点

- 为什么“最终答案正确”不能证明工具链正确？
- 哪些错误应该回给模型，哪些错误应该终止整个 Run？
- 模型调用次数为什么不一定等于 Step 数？

如果能用具体例子回答这三个问题，就已经跨过了“只会调 API”和“理解运行时”的分界线。

---

## 第 5 小时：新增能力时，先问它是不是一个 Tool（04:00–05:00）

### 本节目标

- 理解规划、检索和长期记忆为何都能建立在同一个循环上。
- 学会判断能力属于 Tool、Prompt、Provider 还是应用运行时。
- 知道何时需要渐进加载。

### 5.1 新增一个普通 Tool 不应修改 Runner

要新增 `weather`、`calculator` 或 `search`，通常只需要：

1. 写 Tool Spec。
2. 写执行函数。
3. 注册 Tool。
4. 补参数、成功、失败和超时测试。

如果每加一个能力都要在 `Runner.Run` 中写 `if toolName == ...`，循环会变成无法维护的业务控制器。核心循环应该只认识统一的 Tool 契约。

### 5.2 任务规划也是 Tool

Beaver 的 `todo_write` 不负责执行任务，它让模型把复杂目标写成一个可见计划并持续更新。Runner 不需要理解“待办事项”，只需执行普通 Tool Call。

阅读顺序：

```text
docs/tutorial/02-todo.md
internal/builtin/tools/todo.go
```

关键问题：为什么计划状态可以跟随当前 Run，而不一定要成为独立数据库？答案取决于产品需求；不要因为“规划很重要”就自动新增持久化层。

### 5.3 RAG 是“查询 → 候选片段”的 Tool

一个最小检索工具可以抽象成：

```text
输入：query
执行：切词、打分、选 Top K
输出：片段 + 来源
```

模型不需要知道 BM25 的实现细节。它只知道何时检索，并用结果回答。Beaver 的本地实现位于 `internal/search/search.go`，包含切块、分词、索引和 BM25 搜索。

但检索结果只是候选证据。对于联网搜索，摘要和原始来源还要分开：搜索发现候选，读取原文才算核验。这个策略属于 Tool 元数据与 Runner 护栏的结合，不应靠模型正文关键词猜测。

继续学习：[03 RAG](03-rag.md)。

### 5.4 Memory 是“写入 + 查询 + 删除”的 Tool 集

长期记忆不是把所有历史永远塞进 Prompt。它通常是：

```text
memory_save   写入一条值得跨会话保留的事实
memory_search 按当前问题检索相关事实
memory_forget 删除错误或过期事实
```

这三个 Tool 属于 Beaver Runtime。Codex Runtime 每轮会按用户消息检索最多 5 条相关记忆，作为只读背景注入；它能使用已有记忆，但不能直接保存或删除。

身份不能由模型随意传入。应用层先确定当前会话属于谁，再通过闭包把 owner 绑定进工具；模型只提供内容、查询和作用域。

同时要问四个生产问题：

- 同一身份的并发读写是否一致？
- 文件或数据库写入是否原子？
- 只读权限下是否还暴露写入/删除工具？
- owner 到存储路径的编码是否无碰撞且防目录穿越？

当前专题见 [04 Memory](04-memory.md)。这些实现细节会随正在进行的重构变化，课程不会把尚未合入的方案写成事实。

### 5.5 工具多了以后为什么要渐进加载

把上百个 Tool Schema 每次都发给模型会带来：

- 输入 Token 增长；
- 相似工具互相干扰；
- 小模型选择错误增加；
- 每一步请求更慢。

Beaver 默认只预加载一个小核心，再通过 `load_tools` 或 MCP Loader 把需要的能力加入下一步。Loader 成功不等于任务完成；下一步必须调用真实工具。这仍然没有改变最底层的“模型 → 工具 → 模型”循环，只是改变每一步可见的工具集合。

### 5.6 能力应该放在哪一层

| 需求 | 首选位置 | 原因 |
| --- | --- | --- |
| 增加计算、检索、业务 API | Tool | 有明确输入、执行和结果 |
| 告诉模型何时使用能力 | Prompt / Tool Description | 属于决策指导，不是强制权限 |
| 强制路径、网络、审批边界 | 应用与执行器代码 | 不能依赖模型自觉 |
| 适配某家模型 JSON | Provider Adapter | 不污染核心循环 |
| 跨重启保存会话 | Store / 应用运行时 | 不属于单次模型决策 |
| 多任务排队和资源互斥 | Scheduler | 不属于 Runner 的单轮职责 |

### 本小时练习

设计一个 `document_search` Tool，只写契约，不写执行代码：

- 参数至少包含什么？
- 输出如何保留原始来源？
- 无命中是正常空结果还是错误？
- 它是否能证明来源已经核验？
- 需要哪些超时和大小限制？

参考答案方向：参数至少有 `query`，可以有受控的 `limit`；结果包含片段、文件或 URL、定位信息和分数；无命中通常是成功的空集合；搜索摘要不能自动证明原文已读；需要查询超时、Top K 和单条/总结果大小上限。

---

## 第 6 小时：从 Mini Agent 走到 Beaver（05:00–06:00）

### 本节目标

- 能沿真实源码跟踪一轮请求。
- 理解生产复杂度各自解决的具体失败模式。
- 能判断 Mini Agent 哪些地方绝不能直接部署。

### 6.1 一轮 Beaver 请求的主路径

先只跟这条主线，不要同时打开所有文件：

```text
Web / 微信 / Automation
        │
        ▼
会话与用户消息写入 SQLite
        │
        ▼
turn_queue：持久化排队
        │
        ▼
task_scheduler：并发名额与资源互斥
        │
        ▼
executeSessionTurn
        │
        ├─ Codex Runtime
        │
        └─ runBeaverTurn
              │
              ├─ 组装 Prompt / Skill / Tool / MCP
              ├─ 恢复或压缩上下文
              ├─ 创建 Provider Adapter
              ├─ Runner.Run
              └─ 消息、Trace、Usage、状态落库
```

源码入口：

1. `internal/server/turn_execution.go` 的 `executeSessionTurn`；
2. `internal/server/beaver_runtime.go` 的 `runBeaverTurn`；
3. `internal/agent/runner.go` 的 `Run`；
4. `internal/agent/types.go` 的 `RunRequest` 回调。

`runBeaverTurn` 是应用层与核心循环的接缝，最值得精读。它读取会话与设置、组装工具、创建模型适配器、挂载落库/Trace/审批/压缩回调，最后调用 Runner。

### 6.2 Session、Turn、Step、Attempt、Event

用一个具体例子：用户在已有会话里问“查资料并总结”，模型第一次请求搜索，第二次读取原文，期间第一次 HTTP 请求遇到 429 后重试。

```text
Session：整个持续对话
└── Turn：这一次“查资料并总结”
    ├── Step 1：模型决定搜索
    │   ├── Attempt 1：HTTP 429
    │   └── Attempt 2：成功返回 search Tool Call
    ├── Step 2：模型决定读取原文
    └── Step 3：模型形成最终回答

Event：model_start、model_end、tool_start、tool_end 等审计记录
```

最大步数限制 Step，不是 Attempt；会话轮数统计 Turn，不是模型请求次数。

### 6.3 持久化为何不只是“保存聊天记录”

Mini Agent 的 `history` 只在内存里。生产系统至少需要保存：

- Session 状态和运行设置；
- user、assistant、tool 消息；
- Tool Call 与 Tool Result 的配对；
- Usage 和最终状态；
- Trace 事件；
- 上下文压缩检查点；
- 可能产生副作用的工具操作状态。

Beaver 的 assistant Tool Call 与本 Step 的全部 tool 结果通过 `OnTurnMessages` 一批保存，避免数据库只留下半条工具链。原始消息不会因为压缩而删除；压缩建立检查点，运行时使用摘要加最近消息重建上下文。

### 6.4 Trace 不等于模型思维过程

Trace 应记录可审计事实：

- 第几 Turn、Step、Attempt；
- 调用了什么模型、协议和工具；
- 输入输出的可展示部分；
- HTTP 状态、耗时和 Token Usage；
- 工具成功、失败或被拒绝。

它不应该要求或伪造模型的私有思维过程。Beaver 的 `agent.Event` 和 `internal/server/trace_observer.go` 负责这层可观测性。

### 6.5 权限不能只写进 Prompt

“不要访问工作区外文件”写进 system prompt，只是一条给模型的行为建议。真正的边界必须由代码强制：

- 文件路径归一化和允许根目录判断；
- 解析符号链接后的再次校验；
- 只读模式裁剪相应写工具；当前 Memory 的 `save/forget` 仍是已知缺口，见 Memory 专题；
- 危险操作审批；
- 子进程与操作系统权限；
- 网络请求目标校验和响应大小限制。

Prompt 可以减少误调用，不能阻止恶意输入绕过。教学 Mini Agent 不提供 Shell、文件写入和任意网络抓取，正是因为它没有完整权限层。

### 6.6 队列和调度为什么分两层

Beaver 将它们分开：

- 持久化队列记录 `queued/running/...` 状态，进程重启后仍知道哪些任务待处理。
- 进程内 Scheduler 控制总并发和资源互斥，例如同一工作区或同一 Ollama endpoint 串行。

重启时不能盲目恢复所有 `running` 任务，因为某个工具的副作用可能已经发生；`queued` 可以恢复，未完成副作用则需要明确标记和审计。

### 6.7 Mini Agent 与 Beaver 的逐项对照

| Mini Agent | Beaver 生产实现 | 多出来的复杂度解决什么 |
| --- | --- | --- |
| `Message` | `internal/agent/types.go` | 多模态、结构化结果、推理续接信息 |
| `Model.Generate` | Provider Adapter + capability | 多协议、流式、Usage、错误归一化 |
| 固定工具 map | Catalog + Loader + MCP | 工具规模、动态发现、连接生命周期 |
| 简单 `for` 循环 | `agent.Runner.Run` | 恢复、强制工具、来源核验、失败收敛 |
| 内存 history | SQLite messages + runtime session | 跨请求、跨重启、分页与恢复 |
| 无压缩 | compaction checkpoint | 长会话超过上下文窗口 |
| 返回 history | Trace events | 用户可观察、调试和审计 |
| 单工具超时 | Turn timeout + tool timeout + cancel | 分层取消和长任务 |
| 无权限模型 | permissions + approval + executor checks | 强制安全边界 |
| 单进程命令 | turn queue + scheduler | 多入口、并发、资源互斥、恢复 |
| 无副作用账本 | `tool_operations` | 崩溃后判断副作用是否可能已发生 |

### 6.8 当前实现中的几个重要默认值

以下是理解源码时的路标，不是建议你盲目照搬：

- Runner 默认最多 32 个 Step。
- Runner 工具外层默认超时 5 分钟。
- 模型单请求和整轮任务还有应用层超时。
- 默认总并发为 4；同工作区以及同一 Ollama endpoint 会串行。
- 压缩保留原始消息，只改变运行时装入模型的上下文。
- 当前长期记忆是明文 Markdown，不适合保存秘密。

默认值可能随实现变更。写文档时应链接定义位置并由测试保护，不要只在多篇文档中复制数字。

### 6.9 生产化检查表

准备把自己的 Mini Agent 做成产品前，至少回答：

- 消息与 Tool Call 如何跨重启保存？
- assistant 调用和全部 tool 结果能否原子保存？
- 模型 429、5xx、4xx、空响应和截断响应分别怎样处理？
- 工具是否有最大执行时间、最大输入和最大输出？
- 未知工具和非法参数是否绝不会进入执行器？
- 写操作崩溃后，能否判断副作用可能已经发生？
- 文件、网络和 Shell 权限由哪段代码强制？
- 用户如何取消，底层工具是否真的响应取消？
- 长会话如何压缩，原始记录是否仍可审计？
- 多个入口是否复用同一条执行路径？
- 队列状态与进程内并发名额是否被混为一谈？
- 密钥是否可能进入 Tool Spec、日志、Trace 或错误信息？

如果其中多数没有答案，这仍是一个学习 Demo，不是可安全部署的 Agent 服务。

---

## 结课实验

### 实验 A：解释完整轨迹

运行：

```bash
go run ./examples/mini-agent -trace "请统计：中文 Agent"
```

不看源码，逐条说明每条消息由谁产生、哪些字段必须存在、下一条为何会出现。

### 实验 B：证明护栏有效

运行：

```bash
go test ./examples/mini-agent -v
go test -race ./examples/mini-agent
go vet ./examples/mini-agent
```

然后选一个测试，故意注释对应护栏，确认测试会失败，再恢复代码。只有“红过再绿”的测试，最能证明你理解它守住了什么。

### 实验 C：跟踪 Beaver 主路径

从 `executeSessionTurn` 开始，只沿函数调用读到 `Runner.Run`，回答：

1. 会话工作区在哪里确定？
2. Tool 在哪里组装？
3. Provider Adapter 在哪里创建？
4. assistant/tool 消息在哪里成批落库？
5. Trace Observer 在哪里挂载？
6. 权限审批通过哪个回调进入 Runner？

答案都集中在 `internal/server/turn_execution.go` 和 `internal/server/beaver_runtime.go`，不需要搜索整个仓库。

### 实验 D：新增一个安全的纯函数 Tool

自行实现 `text_reverse(text)`：

- 按 Unicode rune 反转，不按字节反转；
- 空文本返回参数错误；
- Schema 禁止额外字段；
- 加入注册表；
- 用 Fake Model 写完整调用与配对测试；
- 不修改 `Runner.Run`。

如果新增这个能力必须改循环，先停下来重新检查 Tool 抽象。

---

## 你现在真正掌握的原理

一个 Agent 的核心并不神秘：

```text
状态（messages）
  + 可选动作（tool schemas）
  → 模型做一次决策
  → 程序验证并执行动作
  → 结果写回状态
  → 重复，直到得到最终回答或触发护栏
```

难点不在把这个循环写出来，而在它进入真实世界以后：协议会失败，模型会输出坏参数，工具会超时，副作用可能处于未知状态，会话会超过上下文，多入口会争抢资源，权限不能依赖 Prompt。

Mini Agent 让你看清循环；Beaver 展示这些现实问题如何逐层解决。接下来建议按 [课程主页](README.md) 的三天路线继续读规划、RAG、Memory 和应用运行时。
