# Mini Agent：可运行的教学实现

这个目录不是 Beaver 的缩小部署版，而是一套专门用来学习 Agent 原理的实验代码。它只用 Go 标准库，把最重要的协议和循环保留在约几百行代码里：

```text
模型看到消息和工具说明
        │
        ▼
模型直接回答 ───────────────► 结束
        │
        └─ 请求工具 ─► 本地代码执行 ─► tool 消息回给模型 ─► 再判断
```

完整课程见 [6 小时从 0 到 1 手搓一个 Agent](../../docs/tutorial/00-build-an-agent-in-6-hours.md)。

## 先跑起来

不需要 API Key，也不会访问网络：

```bash
go run ./examples/mini-agent -trace "请统计：Agent = 模型 + 工具 + 循环"
```

你会看到四种消息角色和一次完整工具调用：

```text
1. system
2. user
3. assistant
   tool_call: id=call_demo_1 name=text_stats ...
4. tool name=text_stats tool_call_id=call_demo_1
   content: {"bytes":32,"non_space_runes":14,"ok":true,"runes":20}
5. assistant
   content: 统计完成：32 个 UTF-8 字节，20 个 Unicode 字符，其中 14 个不是空白。
```

这里最重要的不是数字，而是第 3、4 条消息使用同一个 `tool_call_id`。模型提出调用，程序执行调用；工具结果不能伪装成用户消息。

运行全部确定性测试：

```bash
go test ./examples/mini-agent -v
```

## 按这个顺序读

| 顺序 | 文件 | 先回答的问题 |
| --- | --- | --- |
| 1 | [`types.go`](types.go) | 消息、工具说明、工具调用和模型接口分别是什么？ |
| 2 | [`demo_model.go`](demo_model.go) | 没有真实模型，为什么仍然能测试 Agent？ |
| 3 | [`tools.go`](tools.go) | JSON Schema 和本地执行函数为什么要分开？ |
| 4 | [`runner.go`](runner.go) | 什么条件继续循环，什么条件结束？ |
| 5 | [`runner_test.go`](runner_test.go) | 如何确定工具确实执行并且结果正确配对？ |
| 6 | [`openai_compatible.go`](openai_compatible.go) | 厂商协议如何转换成核心类型？ |
| 7 | [`openai_compatible_test.go`](openai_compatible_test.go) | 不访问真实服务，如何验证 HTTP 协议？ |
| 8 | [`main.go`](main.go) | 命令行、取消信号和配置怎样接到 Runner？ |

## 接一个 OpenAI-compatible 服务

服务必须支持原生 Function Calling。把地址和模型名替换成你实际使用的值：

```bash
MINI_AGENT_MODE=openai \
MINI_AGENT_BASE_URL=http://127.0.0.1:11434/v1 \
MINI_AGENT_MODEL='<支持工具调用的模型名>' \
MINI_AGENT_TRACE=1 \
go run ./examples/mini-agent "请统计：工具结果必须回传给模型"
```

需要鉴权的服务再设置：

```bash
export MINI_AGENT_API_KEY='<你的 API Key>'
```

示例不会打印 API Key，但 shell 历史和进程环境仍可能暴露秘密；教学或生产环境都应使用合适的密钥管理方式。只有 `localhost`、`127.0.0.1`、`::1` 等本机回环地址允许使用 HTTP，其他主机必须使用 HTTPS。

也可以用命令行参数覆盖环境变量：

```bash
go run ./examples/mini-agent \
  -mode openai \
  -base-url http://127.0.0.1:11434/v1 \
  -model '<支持工具调用的模型名>' \
  -trace \
  "请统计：hello Agent"
```

## 这个例子故意没做什么

它没有实现流式输出、重试、持久化、上下文压缩、权限审批、附件、渐进加载、MCP 和多入口调度。少不是遗漏，而是为了让第一遍学习时能看见核心循环。上述能力在 Beaver 主代码里都有对应实现，课程最后一小时会逐项映射。

不要给这个教学样例直接加入 Shell、任意文件写入或任意 URL 抓取后部署到公网。一个可运行的 Agent 循环，不等于一个具备生产安全边界的 Agent 产品。
