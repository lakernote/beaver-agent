# 学习：Agent 的记忆（Memory）

对应源码：`internal/memory/memory.go`、`internal/builtin/tools/memory.go`。

## 1. 两种记忆，别混淆

| | 短期记忆 | 长期记忆 |
| --- | --- | --- |
| 是什么 | 当前会话的对话历史 | 跨会话保留的事实 |
| 存哪里 | 数据库里的 messages，随每次请求发给模型 | `~/.beaver/memory/*.md` |
| 问题 | 上下文窗口有限，会话长了装不下 | 不能全塞进提示词，需要时才取 |
| 解法 | **压缩**：把旧消息总结成摘要（`compactIfNeeded`） | **落盘 + 检索**：写入文件，用 `memory_search` 取回 |

本篇讲长期记忆。它本质上就是 [RAG](03-rag.md) 加“写入”：普通 RAG 的资料是人放进去的，记忆是 Agent 自己写进去的。

## 2. 设计

- **一条记忆 = 文件里的一个段落**，形如 `2026-11-20 张三偏好中文简短回复`。人可以直接用编辑器查看、修改、删除。
- **三个工具**，只是普通 Tool，Runner 循环不用改：
  - `memory_save(text, scope)`：保存一条简短事实；
  - `memory_search(query)`：BM25 检索，返回最相关的几条；
  - `memory_forget(keyword, scope)`：删除含关键词的条目，用来纠错。
- **模型自己决定何时记、何时查**：提示词规则 6 只描述了原则，代码里没有“出现‘记住’就保存”这类关键词路由。

## 3. 团队场景：按成员隔离

```text
~/.beaver/memory/
├── team/notes.md              团队共享：所有成员可读可写
└── people/
    ├── admin/notes.md         Web、定时任务（没有 owner 的会话）
    └── <微信账号ID>/notes.md   每位微信成员各一份
```

关键点是**身份不由模型传入**：

1. 微信创建会话时，用 `SetSessionOwner` 把会话和微信账号绑定（`sessions.owner_id`）。
2. 每轮运行时 `runBeaverTurn` 读出 owner，构造 `tools.MemoryScope{Store, Owner}`，工具闭包里已经固定了 owner。
3. 工具参数只有 `text`/`query`/`scope`，模型没法指定“读 bob 的记忆”。`owner` 还会被 `safeName` 净化，防止 `../` 目录穿越。

团队记忆是共享的，任何成员的 Agent 都能写入；只应保存团队共同约定。

## 4. 取舍与局限

- **只存短事实**：单条上限 500 字，单文件 256KB。记忆是“偏好、约定、环境”，不是聊天记录。
- **没有自动提炼**：现在要靠用户说“记住”或模型主动判断。更进一步可以在会话结束时让模型总结“值得记住的事”，但要小心把错误结论写成永久记忆，所以保留了 `memory_forget`。
- **检索是关键词**：换种说法可能找不到。升级路径同 RAG：给每条记忆算 Embedding，改用向量检索。
- **只对 Beaver Runtime 生效**：Codex Runtime 使用它自己的会话与工具，不会调用这些记忆工具。
- **别存秘密**：记忆是明文 Markdown，提示词规则已禁止保存密码和密钥。

## 5. 练习

1. 在微信里说“记住我喜欢简短回复”，再到 `~/.beaver/memory/people/` 下找到对应文件。
2. 新建会话问“我喜欢什么风格的回复”，在 Web 的 Trace 里确认模型调用了 `memory_search`。
3. 读 `memory_test.go` 的隔离测试，试着写一个“团队记忆被另一成员读到”的测试。
4. 实现“会话结束时自动提炼记忆”的钩子，并思考如何避免记错。
