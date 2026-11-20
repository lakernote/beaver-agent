# 学习：用 `todo_write` 给 Agent 加上任务规划

对应源码：

- `internal/builtin/tools/todo.go`：契约与执行逻辑；
- `internal/builtin/tools/todo_test.go`：确定性测试；
- `internal/builtin/tools/loader.go`：工具如何进入上下文；
- `internal/builtin/prompt/system.md`：模型何时规划；
- `internal/agent/runner.go`：通用模型—工具循环。

这篇从 Beaver 已有实现出发，拆解一个最小规划能力如何由 **Tool Schema + 运行时校验 + 提示词规则** 组成。

## 学习目标与可见产物

完成本篇后，你应该能够：

1. 说清 `todo_write` 的输入、输出和全部校验边界；
2. 解释“清单在对话里”与“工具自己存状态”的区别；
3. 解释为什么这个能力不需要修改 `Runner`；
4. 只运行它的确定性测试，并补一条边界测试；
5. 判断哪些需求会迫使设计从无状态变成有状态。

模型发出的工具参数：

~~~json
{"todos":[
  {"content":"读代码","status":"completed"},
  {"content":"改实现","status":"in_progress"},
  {"content":"跑测试","status":"pending"}
]}
~~~

工具回给模型：

~~~text
清单已更新：
1. [x] 读代码
2. [~] 改实现
3. [ ] 跑测试
~~~

这段文本作为 `tool` 消息进入对话；下一轮模型能看到当前进度。

## 1. `todo_write` 的完整契约

顶层只有必填字段 `todos`。它是数组，每项必须有：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `content` | string | 一句话描述步骤 |
| `status` | string | `pending` / `in_progress` / `completed` |

状态与输出标记的映射是：

| 状态 | 标记 | 含义 |
| --- | --- | --- |
| `pending` | `[ ]` | 尚未开始 |
| `in_progress` | `[~]` | 正在执行 |
| `completed` | `[x]` | 已完成 |

Schema 规定 `todos`、`content`、`status` 必填，状态是固定枚举；`objectSchema` 还会让两层对象都带 `additionalProperties: false`。

运行时另外强制：

- 清单必须有 1–20 项；
- `content` 去掉首尾空白后不能为空；
- `content` 最多 200 个 Unicode 字符；
- `status` 必须精确匹配三个合法值之一；
- 同一份清单最多有一项 `in_progress`。

可以没有 `in_progress`，例如任务刚全部完成。字符数由 `utf8.RuneCountInString` 计算，不按 UTF-8 字节数计算。

注意：Schema 没有写 `minItems`、`maxItems` 或字符串长度，因此阅读工具必须同时看 `Spec.Parameters` 和 `Run`。

## 2. 逐段阅读真实源码

### 2.1 常量与 Spec

`todo.go` 用 `maxTodoItems = 20` 和 `maxTodoRunes = 200` 定义运行时边界。

`todoWriteTool()` 返回普通的 `agent.Tool`。`Spec.Name` 是 `todo_write`；Description 告诉模型：

1. 只为需要 3 步以上的复杂任务维护清单；
2. 开始前先列步骤，每完成一步立即更新；
3. 每次提交完整清单；
4. 同时只能有一项 `in_progress`。

Description 负责“何时调用”，JSON Schema 负责“参数长什么样”。

### 2.2 解析参数

`Run` 先用 `json.Unmarshal` 把原始 JSON 解析到含 `Todos []struct{ Content, Status string }` 的匿名结构体。

无效 JSON 会走“参数错误”分支；Go 的 `encoding/json` 能将小写 JSON 字段匹配到对应导出字段。

一个重要细节：`json.Unmarshal` 默认忽略未知字段。所以 Schema 虽有 `additionalProperties: false`，直接调用 `tool.Run` 时，Go 本身仍会忽略额外字段。Schema 契约不等于运行时严格校验。

### 2.3 检查清单长度

源码用 `len(args.Todos) == 0 || len(args.Todos) > maxTodoItems` 判断越界：0 项与 21 项失败，1 项和 20 项可继续。运行时允许 1、2 项；“3 步以上才使用”是模型策略，不是数据合法性规则。

### 2.4 校验、计数和渲染

循环会：

1. 用 `strings.TrimSpace` 清理 `content`；
2. 检查内容非空且不超过 200 个 rune；
3. 从 `marks` 查找状态对应标记；
4. 统计 `in_progress` 数量；
5. 按输入顺序写入 `strings.Builder`。

核心格式化语句是 `fmt.Fprintf(&out, "%d. %s %s\n", index+1, mark, content)`。

输出从 1 编号，首尾空白被移除，中间空白保留。重复内容当前合法；工具不替模型判断步骤是否合理。任一项非法，整个调用失败，不会返回半份清单。

循环结束后若 `active > 1`，返回：

~~~text
同一时刻只能有一项 in_progress，现在有 2 项
~~~

成功时返回 `清单已更新：` 加完整列表。

## 3. 为什么无状态，又为什么不改 Runner

`todo_write` 不写文件、数据库或进程内 map，也不读取旧清单。每次输入完整清单，工具只校验并回显。最新结果在语义上代表当前清单；旧结果仍在对话历史里，并没有被工具真正“覆盖删除”。

无状态的好处：

- 不需要会话主键、锁、过期和清理；
- 同一输入产生稳定结果，容易测试；
- Web、微信等入口可以复用；
- 模型与 Trace 看到同一份清单。

代价是每次重复传完整清单，工具无法比较新旧版本，也无法阻止“已完成又退回待办”。若历史被压缩，最新清单能否保留属于上层上下文机制的职责。

`Runner` 只执行通用流程：

~~~text
模型请求工具 -> 按名字查找 -> 执行 Run -> 追加 tool 消息 -> 再问模型
~~~

它没有 todo 专用分支。工具失败也作为结果交回模型，由模型修正参数。`loader.go` 把 `todo_write` 同时放进 `codingCoreToolNames` 和 `readOnlyCoreToolNames`，所以可写与只读会话都会预加载，不必先调用 `load_tools`。

## 4. 模型什么时候应该规划

Go 代码不靠关键词判断任务是否复杂。决定权来自 Tool Description 和 `system.md` 规则 7：需要 3 步以上的复杂任务先列清单，逐步执行，每完成一步立即更新；简单问题不要列清单。

适合：跨多个文件调查、修改、验证；搜索并核验多份来源；多阶段交付。不适合：解释一个函数；读一个已知文件；执行一条确定命令。

步骤应有可观察的完成条件，如“运行指定测试并得到 PASS”；不要写“想一想”“继续处理”“回答用户”这类空洞动作。

## 5. 确定性测试

在仓库根目录只运行现有测试：

~~~bash
go test ./internal/builtin/tools -run '^TestTodoWrite$' -count=1 -v
~~~

应看到 `=== RUN   TestTodoWrite` 与 `--- PASS: TestTodoWrite`。`-count=1` 禁用缓存，锚定的 `-run` 只匹配这个测试名。

当前 `TestTodoWrite` 覆盖：

- 三种状态的正确渲染；
- 空清单；
- 两项 `in_progress`；
- 未知状态 `done`；
- 只有空白的内容。

再跑整个工具包：

~~~bash
go test ./internal/builtin/tools -count=1
~~~

两条命令都不需要模型、网络或外部服务。

## 6. 失败分支实验

| 输入 | 当前结果 | 原因 |
| --- | --- | --- |
| `{"todos":[]}` | 失败 | 少于 1 项 |
| 21 个合法项 | 失败 | 超过 20 项 |
| 两项 `in_progress` | 失败 | 同时进行超过 1 项 |
| `status: "done"` | 失败 | 非法状态 |
| `status: " pending "` | 失败 | 状态不做 TrimSpace |
| 200 个汉字 | 成功 | 上限包含 200 |
| 201 个汉字 | 失败 | rune 数超限 |
| 重复内容 | 成功 | 当前不去重 |
| 0 项 `in_progress` | 成功 | 只限制“最多一项” |

额外字段按 Schema 是非法的，但直接调用当前 `Run` 时会被忽略。这提醒我们：Schema 能引导模型，不能自动替代服务端校验。

## 7. 练习与答案

### 练习一：补边界测试

增加 20 项成功、21 项失败，以及 200/201 字符测试。

答案：循环生成切片，用 `json.Marshal` 产生参数；边界内断言 `err == nil`，边界外断言 `err != nil`。用汉字或 emoji 可验证这里数 rune 而不是 byte。

### 练习二：设计 `blocked` 状态

答案：Schema enum 与 `marks` 都要新增 `blocked`；Description、系统规则、成功渲染和非法状态测试也要更新。还要明确它是否计入“正在进行”，不能只加一个字符串。

### 练习三：禁止状态回退

需求：“同一项一旦 `completed`，以后不能变回 `pending`。”

答案：当前 `Run` 没有上一版，`content` 也不是稳定 ID，因此做不到。至少要有上一版状态和稳定 item ID；引入存储后还要处理会话隔离、并发、恢复和清理。也可让调用方显式传上一版，但契约会更复杂。这不是现状，也不是只补一个 `if`。

### 练习四：判断是否该调用

“解释 `maxTodoItems`”通常一步即可回答，不应列清单。“扫描文档链接、修复失效项并验证”包含扫描、判断、修改、验证，适合先调用 `todo_write`。

## 8. Beaver 的职责边界

| 层 | 负责 | 不负责 |
| --- | --- | --- |
| System Prompt | 规定复杂任务先规划、完成后更新 | 执行校验 |
| Tool Schema | 描述字段、必填项与状态枚举 | 保存清单 |
| `todo_write.Run` | 解析、校验、格式化 | 读取旧版、判断计划质量 |
| Runner | 通用执行工具并追加结果消息 | todo 专用状态机 |
| 对话历史 / Trace | 保留调用与结果 | `todo_write` 内部数据库 |

本例的核心原则是：先用最小通用机制验证能力，再决定是否真的需要状态机、数据库或专用调度器。

## 检查点

- 为什么 2 项清单运行时合法，但提示词通常不建议调用？
- 为什么 201 个汉字失败，而不是按 UTF-8 字节计算？
- 为什么“禁止状态回退”不是多写一个 `if`？
- 为什么 `todo_write` 在只读会话也可用？

能说明这四题，就理解了 Beaver 的最小任务规划能力。

下一篇：[学习：RAG](03-rag.md)，之后是 [长期记忆](04-memory.md)。返回 [课程主页](README.md)。
