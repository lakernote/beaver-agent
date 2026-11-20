# 实操：从会话历史到长期记忆

对应源码：

- `internal/memory/memory.go`：文件存储、检索与删除；
- `internal/search/search.go`：中文/英文分词与 BM25；
- `internal/builtin/tools/memory.go`：三个 Tool 的 Schema 和执行函数；
- `internal/builtin/tools/catalog.go`：把记忆工具注册到 Beaver；
- `internal/server/server.go`、`internal/server/beaver_runtime.go`：创建共享 Store、绑定 owner；
- `internal/server/codex_memory.go`：给 Codex Runtime 注入只读的相关记忆；
- `internal/store/session_store.go`：把微信账号与会话关联；
- `internal/server/context_compaction.go`：会话压缩，注意它不是长期记忆。

这篇以“读代码、跑测试、故意触发失败”为主，完成约需 60～90 分钟。

## 1. 先建立正确的心智模型

Agent 至少有三种容易被混称为“记忆”的状态：

| 层次 | 回答的问题 | 当前保存位置 | 进入模型的方式 |
| --- | --- | --- | --- |
| 会话历史 | 这次会话刚才说了什么？ | SQLite 的 `messages` | 活跃消息随模型请求发送 |
| 压缩检查点 | 会话很长时，较早部分讲了什么？ | SQLite 的 `compactions` | 最新摘要代替较早消息进入请求 |
| 长期记忆 | 跨会话仍有用的稳定事实是什么？ | `memory/` 下的 Markdown | Beaver 调 `memory_search`；Codex 由 Server 按本轮消息检索后作为只读背景注入 |

三者不能互相替代。

“我上一句说了什么”应查会话历史，不值得永久保存。

“把前 100 条消息压成摘要”是在控制上下文长度，不代表其中每个事实都可靠、都应跨会话保留。

“以后都用中文简短回复”才适合长期记忆，因为它在新会话里仍然有用。

当前压缩流程会调用同一个模型生成检查点，但不给它任何 Tool，避免摘要阶段产生副作用。
检查点只记录到 `compactions`，原始 `messages` 不会被删除，仍可审计和重建。

长期记忆则完全独立：它不会把全部 Markdown 塞进提示词，也不会自动从聊天历史提炼事实。Beaver Runtime 由模型调用记忆工具读写；Codex Runtime 没有这三个 Tool，Server 会在每轮开始时自动检索最多 5 条相关记忆，只读注入本轮上下文。

## 2. 一次完整调用经过哪里

假设微信用户说“记住我喜欢简短的中文回复”，保存链路是：

```text
用户 → Agent → 模型调用 memory_save
     → Tool 闭包补上 owner → Store.Save → personal/notes.md
```

Beaver 新会话问“我喜欢什么回复风格”时，读取链路是：

```text
用户 → 模型调用 memory_search → 读取 personal + team
     → 段落建 BM25 索引 → 前 5 条交回模型 → 模型回答
```

Codex Runtime 的读取链路不同：`codexMemoryContext` 直接用本轮用户消息检索 personal + team，把命中项放进 `<beaver_memory>` 只读背景；无命中就不注入。这里仍没有关键词规则，只使用和 `memory_search` 相同的 BM25。

只有 Beaver 的 Tool 调用由模型决策；Codex 的只读检索由应用层在每轮开始时完成。

## 3. 当前文件布局

默认运行目录是 `~/.beaver`，也可以由 `appenv.Config` 改成其他目录。
Server 启动时创建逻辑上的根：

```go
memory.New(filepath.Join(environment.Home(), "memory"))
```

第一次写入前目录可以不存在。写入后布局如下：

```text
~/.beaver/memory/
├── team/
│   └── notes.md
└── people/
    ├── admin/
    │   └── notes.md
    └── <safeName(owner)>/
        └── notes.md
```

`team/notes.md` 是共享记忆；`people/<owner>/notes.md` 是个人记忆。没有 owner 的 Web 会话和定时任务落到 `people/admin/notes.md`。

一条记忆就是一个以空行分隔的 Markdown 段落：

```markdown
2026-11-20 用户偏好简短的中文回复

2026-11-20 默认部署环境是 staging
```

日期由 `Save` 写入。保存时会统一换行符、去掉首尾空白，并把连续空行折成单换行；判重与落盘使用同一份规范化文本，避免一条记忆被拆成多个段落或因换行形式不同而重复保存。新目录用 `0700`、新文件用 `0600`；但文件仍是明文，不是密钥保险库。

## 4. Store 的四个核心动作

`memory.New(root)` 只保存根路径，不扫描文件，也不建立常驻索引。

### 4.1 Save：读、判重、追加、整文件重写

`Save(owner, scope, text)` 按以下顺序执行：

1. `TrimSpace`，空内容直接报错；
2. 按 Unicode rune 计数，超过 500 个字符报错；
3. 根据 owner 和 scope 算出文件路径；
4. 获取当前 Store 实例的互斥锁；
5. 读取文件中全部段落；
6. 去掉旧记录的日期后做精确判重；
7. 给新内容加当天日期；
8. 把全部段落重新写回文件。

判重是精确字符串比较，不是语义去重：“用户喜欢中文回复”和“用户更喜欢使用中文回复”会同时保存。同一文本第二天再保存仍会判重，因为比较前会去掉已有日期。

`scope == ""` 与 `personal` 等价，这是 Tool 里省略可选 scope 时的默认行为。
其他值会返回“未知的记忆范围”错误。

### 4.2 Search：合并两个作用域，临时建立索引

`Search(owner, query, limit)` 固定读取：

1. 当前 owner 的 personal 文件；
2. 全员共享的 team 文件。

每个段落直接成为一个 `search.Chunk`，`Source` 填 `personal` 或 `team`。
它没有调用文档 RAG 的 `SplitText`，因为记忆在写入时已经是一条一个段落。

每次搜索都会用当前文件内容执行 `search.NewIndex(chunks)`。
优点是手工编辑 Markdown 后立刻生效；缺点是文件变大后，每次都要重新分词和统计。

Tool 把 limit 固定为 5，输出形如：

```text
[personal] 2026-11-20 用户偏好简短的中文回复
[team] 2026-11-20 发布前必须运行 go test ./...
```

没有正分数命中时返回“没有找到相关记忆”，模型不应据此编造。

### 4.3 Forget：包含匹配，而且一次删除全部命中

`Forget(owner, scope, keyword)` 只操作指定的一个作用域。
scope 省略时仍是 personal，并不会同时删除 team。

匹配使用区分大小写的 `strings.Contains`，对象是带日期的完整段落。
因此关键词 `2026-11-20` 可能删除当天所有条目，关键词 `服务器` 也可能删除多条。

这是“删除”而不是“撤销”：当前没有回收站、版本历史或二次确认。
调用前应优先用更具体的关键词确认范围。

### 4.4 文件容量

单条上限是 500 个 rune，单文件上限是 256 KiB。

前者按字符数判断，后者按最终文件的字节数判断，所以中文的字节占用通常大于 ASCII。
达到文件上限后，写入返回“记忆已满”，需要先删除过时内容。

这个限制保护的是单个文件，不是整个 `memory/` 目录的总容量。

## 5. BM25 到底怎样找到记忆

当前检索只用 Go 标准库，不调用 Embedding。分词规则是：

- 英文和数字按连续单词切分，并转成小写；
- 连续中文按相邻两个汉字组成 bigram；
- 单个汉字单独成为一个词；
- 标点和空白结束当前词。

例如 `Beaver 定时任务 v2` 会变成 `beaver, 定时, 时任, 任务, v2`。

BM25 的直觉有三部分：

1. 查询词在一条记忆里出现越多，相关性通常越高，但收益逐渐变小；
2. 在所有记忆里越少见的词，区分能力越强；
3. 很长的段落会被长度归一化，避免仅靠词多占便宜。

源码使用 `k1 = 1.5`、`b = 0.75`。
只返回分数大于 0 的块，再按分数降序截取 limit。

这不是语义检索。“上线前要跑测试”和“发布前必须验证”可能因为没有共享词而互相找不到。
升级到向量检索时，可以替换索引和打分层，Store 与 Tool 的身份边界仍可保留。

## 6. owner：身份由服务端绑定，不让模型填写

`MemoryScope` 只有两个字段：

```go
type MemoryScope struct {
    Store *memory.Store
    Owner string
}
```

微信创建会话时，服务端调用 `SetSessionOwner(sessionID, account.ID)`，把账号 ID 写进
`sessions.owner_id`。每轮开始时再读取它，构造绑定 owner 的 `MemoryScope`。

三个 Tool 的 JSON 参数里都没有 owner。
因此模型不能把参数改成 bob 来读取 bob 的个人文件，这是关键的信任边界。

注意：owner 只用于选择记忆目录，不参与权限判定。
team 本来就是所有已绑定成员共同读写的空间。

### 6.1 当前目录编码

`safeName` 保留 ASCII 字母、数字、`-` 和 `_`，其余字符替换为 `_`。
只要发生替换，就追加 owner 的 SHA-256 前 4 字节，也就是 8 个十六进制字符。

这已经避免了最直接的 `a.b` 与 `a_b` 碰撞，也阻止 `../` 目录穿越。
但它只是“短哈希降低碰撞概率”，不是数学上无碰撞的编码：

- 哈希只取 32 bit，理论上仍可能碰撞；
- 非空 owner `admin` 与空 owner 都映射到 `people/admin`；
- 合法 ASCII 名称可能碰巧长得和“净化名-短哈希”一样。

所以不要把当前实现描述成“绝不碰撞”。
更严格的改进方向是给空 owner 使用保留目录，并把所有非空 owner 完整编码，例如：

```text
people/_admin/notes.md
people/u-<owner 原始字节的完整十六进制>/notes.md
```

这项完整编码尚未落地，迁移时还要兼容已有目录或执行一次显式搬迁。

### 6.2 微信重新绑定保持同一 owner

登录确认后，`weixinMemberID(userID)` 计算 `wx_` 加 SHA-256 前 6 字节的十六进制，也就是 48-bit 稳定成员 ID，并把它存为 `account.ID`。同一 `UserID` 重新扫码仍得到相同 ID，因此新旧会话继续使用同一个 owner，个人记忆不会因重绑换目录。

这里的 48-bit 短哈希是稳定、低碰撞概率的工程标识，仍不是数学上无碰撞。若身份规模或安全要求提高，可使用完整哈希或数据库唯一映射；这是改进方向，不是现状。

## 7. 三个 Tool 的精确契约

| Tool | 必填参数 | 可选参数 | 当前结果 |
| --- | --- | --- | --- |
| `memory_save` | `text` | `scope` | `已记住` |
| `memory_search` | `query` | 无 | 最多 5 条 `[scope] text` |
| `memory_forget` | `keyword` | `scope` | `已删除 N 条记忆` |

scope 的 Schema 只允许 `personal` 和 `team`。
`memory_save`、`memory_forget` 省略 scope 时由空字符串落到 personal。

工具描述要求：稳定偏好、团队约定、环境信息才值得保存；临时进度、密码和密钥不要保存。
这是给模型的行为指导，不是内容分类器，也不是秘密扫描器。

工具只在 Beaver Runtime 中注册。Codex Runtime 使用自己的工具，不调用这三个 Tool；但每轮开始时 `codexMemoryContext`（`internal/server/codex_memory.go`）会按用户消息检索最多 5 条记忆，作为只读的 `<beaver_memory>` 背景附在消息前，所以 Codex 能“读到”记忆，不能写入。

## 8. 当前并发、原子性和只读边界

这是最容易被“设计目标”误写成“当前事实”的部分。

### 8.1 已实现：一个 Server 进程共用一个 Store

`Server` 启动时创建一次 `server.memory`，每轮都把同一指针放入 `MemoryScope`。
同一个 Store 的 `Save` 和 `Forget` 共用一把 `sync.Mutex`，所以普通单进程运行中，写操作会串行。

代价是不同 owner、不同文件的写入也会互相等待；当前没有按路径拆锁。

### 8.2 尚未实现：跨 Store、跨进程共享锁

锁属于 `Store` 实例，不属于文件路径。
测试或其他调用方如果对同一 root 执行两次 `memory.New(root)`，就得到两把互不相识的锁。
启动两个 Beaver 进程指向同一个 home 也一样没有共同锁。

这种情况下两个“读旧内容 → 追加 → 整文件写回”可能互相覆盖，造成丢失更新。
按规范化文件路径共享锁、进程级文件锁或改用事务数据库，都是改进方向；当前没有实现。

### 8.3 尚未实现：原子写入和读写同步

`writeNotes` 直接调用 `os.WriteFile`，不是“同目录临时文件 + fsync + rename”。
进程崩溃、磁盘写满或断电时，目标文件可能已经被截断却没有完整写完。

`Search` 也不获取 Store 的互斥锁，可能在写文件期间读到空内容或中间状态。
当前测试只证明同一个 Store 的并发 Save 在正常执行时保留全部条目，不证明崩溃安全。

### 8.4 尚未实现：只读模式只保留搜索

文件工具层会用 `IsReadOnly()` 隐藏 `edit` 和 `write`，`catalogEntries` 还会隐藏 shell；但这个过滤没有作用到 Memory Tool。
只要提供了 `MemoryScope`，三个记忆 Tool 仍会一起注册。

所以当前 `read_only` 会话仍可能通过 `memory_save` 或 `memory_forget` 修改 home 下的记忆文件。
更合理的目标是只注册 `memory_search`，但这项策略尚未落地。

## 9. 运行当前测试

在仓库根目录依次执行：

```bash
go test ./internal/memory -v
go test ./internal/builtin/tools -run 'Memory|Catalog' -v
go test ./internal/search -v
go test -race ./internal/memory ./internal/builtin/tools
go test ./internal/server -run '^TestCodexMemoryContextInjectsRelevantNotes$' -v
```

这些命令分别验证 Store、Tool owner 绑定、BM25 分词排序、竞态和 Codex 只读注入。不要过度解读 race 结果：它检测内存访问竞态，不会证明两个独立 Store 没有文件级丢失更新，也不会证明断电时文件完整。

## 10. 故意触发几个边界

### 实验 A：让输入校验失败

```bash
go test ./internal/memory -run TestSaveRejectsBadInputAndDeduplicates -v
```

测试本身应 `PASS`：它断言空内容、501 个中文字符、未知 scope 被拒绝，并确认重复事实只落一条。

### 实验 B：观察个人与团队的差异

```bash
go test ./internal/memory -run TestPersonalMemoryIsIsolatedAndTeamIsShared -v
go test ./internal/builtin/tools -run TestMemoryToolsAreBoundToOwner -v
```

Alice 的 personal 对 Bob 不可见；Alice 写入的 team 对 Bob 可见。第二条还会删除 Alice 的记录并核对返回数量。

### 实验 C：验证“安全”不等于“无碰撞”

读 `TestOwnerCannotEscapeDirectory`：它证明 `../../evil` 不能越界，且 `a.b` 与 `a_b` 当前不会共目录。再对照 `safeName("")` 与 `safeName("admin")`：两者当前都是 `admin`，这是无需暴力碰撞哈希的反例。

### 实验 D：验证两个 Store 的锁互不共享

把并发测试中的单个 `store := New(root)` 改成两个 `New(root)`，让 goroutine 交替使用，并用 `-count=100` 重复。压力下可能看到条目数少于写入数；没复现也不代表安全。只使用 `t.TempDir()`，不要操作真实记忆。

### 实验 E：检查只读目录

在 `TestCatalogIncludesMemoryGroupOnlyWhenProvided` 中换成 `permissions.Policy{Mode: permissions.ModeReadOnly}`，枚举工具名。
当前仍能看到 save、search、forget；这是待修边界，不是测试环境配置错误。

## 11. 练习

1. 为什么 `memory_search` 没有 scope 参数，而另外两个 Tool 有？
2. 为什么 Search 不调用 `SplitText`？
3. `memory_forget(keyword="2026")` 有什么风险？怎样设计更安全的删除 API？
4. 给只读策略设计最小修改，并列出至少两个测试。
5. 设计一个不碰撞、可逆、不会目录穿越的 owner 编码。
6. 把 `writeNotes` 改成原子替换时，为什么临时文件必须放在目标文件同一目录？
7. 如果要支持多个 Beaver 进程共享记忆，仅给 `Store` 加 `RWMutex` 是否足够？

<details>
<summary>参考答案</summary>

1. 检索必须同时看到当前个人事实和团队约定；写入与删除有副作用，必须明确落到哪个作用域。
2. 每条记忆已经用空行划成独立、短小段落，直接作为 Chunk 可避免再次合并不同事实。
3. 它会包含匹配并批量删除，甚至命中日期。可以先 search/preview，再按稳定 note ID 精确删除，并增加确认。
4. 组装目录时，只读模式仅追加 `memory_search`。测试应覆盖只读只有 search、非只读仍有三个，也要验证 UI 的 InfoList 一致。
5. 例如空 owner 固定为 `_admin`；非空 owner 使用 `u-` 加原始 UTF-8 字节的完整十六进制。前缀隔离保留目录，完整编码可逆且不会出现 `/`。
6. 同一文件系统内 rename 才通常具备原子替换语义；跨文件系统 rename 可能失败或退化成复制。
7. 不足。进程内锁无法协调另一个进程，需要文件锁、单写进程、事务数据库或其他跨进程协调机制。

</details>

## 12. 本章结论

会话历史保存对话，压缩检查点控制上下文预算，Markdown 保存跨会话事实，BM25 按需取回；owner 由服务端绑定，模型不能自行选择身份。

生产化时先补权限、身份唯一性、原子写和跨进程协调，再优化索引、Embedding 与自动提炼。当前单进程共享 Store 和重绑稳定成员 ID 已实现；跨 Store/跨进程锁、原子写、严格只读、无碰撞 owner 编码仍是改进方向。下一步可回到 [RAG](03-rag.md)，或读 [微信团队入口](05-team-wechat.md)。
