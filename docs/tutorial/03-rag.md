# 实操：从零理解 RAG、分词与 BM25

对应源码：`internal/search/search.go`、`internal/search/search_test.go`。

这一篇不引入向量数据库，也不调用外部服务。我们只用标准库和一组可以手算的小语料，把本项目的 RAG 实现完整走一遍。

学完后，你应该能解释切块、中文双字分词、索引统计和 BM25 排序，能正确处理无命中、Top K 与来源字段，并能区分本地 RAG 和联网来源核验。

## 1. RAG 到底做了什么

RAG 是 Retrieval-Augmented Generation，即“检索增强生成”：

```text
资料准备：文件 ─► 切块 ─► 分词与建索引
在线问答：问题 ─► 分词 ─► 检索 Top K ─► 连同出处交给模型 ─► 回答
```

模型没有因此学会新知识。程序只是每次提问时找出几段资料，临时放进模型上下文。

本项目的最小实现只有三个主要数据结构：

| 类型 | 保存什么 |
| --- | --- |
| `Chunk` | 一段原文、来源路径、起始行号，以及内部词频 |
| `Index` | 所有块、词的文档频率、平均块长度 |
| `Result` | 命中的 `Chunk` 加一个 BM25 `Score` |

调用关系如下：

```text
BuildFromDir
  └─ 对每个 .md / .txt 调 SplitText
       └─ NewIndex([]Chunk)
            └─ Index.Search(query, limit)
                 └─ []Result
```

## 2. 第一步：`SplitText` 切块

当前阈值是：

```go
const maxChunkRunes = 800
```

800 指 Unicode 字符数，即 `len([]rune(text))`，不是 UTF-8 字节数。一个汉字按一个字符计算。

`SplitText(source, text)` 的真实流程是：

1. 用两个连续换行 `\n\n` 拆成段落；
2. 从前往后合并相邻段落；
3. “当前块 + 下一段”超过 800 字符时，先输出当前块；
4. 新块从该段开始，并记录这一段的起始行；
5. 输出时保留 `Source`、`Line` 和 `Text`。

现有测试构造了：

```go
text := "第一段\n\n" + strings.Repeat("长", 700) + "\n\n" + strings.Repeat("尾", 300)
chunks := SplitText("a.md", text)
```

`第一段` 和 700 个“长”进入第一块；再加入 300 个“尾”会超过阈值，因此“尾”进入从原文第 5 行开始的第二块。

一个重要边界是：**800 是段落合并阈值，不是硬上限。**

如果原文只有一个 1200 字、内部没有空行的段落，当前实现不会从中间劈开它，仍会产生一个超过 800 字的块。它优先保持段落完整，没有句子级或字符级二次切分。

还要记住：

- 空文本或纯空白得到零个块；
- 分段依据是 `\n\n`，不是 Markdown 标题；
- `Line` 从 1 开始，是块的起始行，不是命中词的精确行；
- 输出前会用 `TrimSpace` 去掉块首尾空白。

`BuildFromDir(root)` 再包一层目录遍历：

- 递归读取 `.md` 和 `.txt`，扩展名不区分大小写；
- 跳过根目录以下以 `.` 开头的隐藏目录；
- 单个文件读失败时跳过它，目录遍历失败则返回错误；
- 遍历得到的文件路径会进入 `Chunk.Source`；
- 索引只在内存中建立，没有增量更新或磁盘持久化。

## 3. 第二步：`Tokenize` 分词

BM25 比较的是词，所以文档和查询必须使用同一个分词函数。

当前规则是：

- 连续字母和数字组成一个词；
- 英文统一转小写；
- 连续汉字拆成重叠的相邻双字，即 bigram；
- 单独一个汉字保留为单字词；
- 空格和标点会结束当前连续片段。

现有测试：

```go
Tokenize("Beaver 定时任务 v2")
```

结果必须是：

```text
[beaver 定时 时任 任务 v2]
```

四个连续汉字使用滑动窗口，依次得到 `定时`、`时任`、`任务`。双字法不理解词义，`时任` 在这里会显得奇怪，但这种方法无须词典，并能让局部字符重叠参与匹配。

几个边界例子：

| 输入 | 输出 | 原因 |
| --- | --- | --- |
| `Agent AGENT` | `agent`, `agent` | 转小写，但不去重 |
| `定时任务` | `定时`, `时任`, `任务` | 连续汉字产生重叠双字 |
| `定时 任务` | `定时`, `任务` | 空格阻断，不产生 `时任` |
| `猫` | `猫` | 单个汉字保留 |
| `Go1.22` | `go1`, `22` | 句点切断片段 |

查询词也不会去重；查询中重复的词会重复参与分数累加。

## 4. 第三步：`NewIndex` 建索引

用三个已切好的块做手算：

| 块 | `Text` | 分词后 | 长度 |
| --- | --- | --- | ---: |
| A | `agent tool tool` | `agent`, `tool`, `tool` | 3 |
| B | `agent memory` | `agent`, `memory` | 2 |
| C | `weather sunny` | `weather`, `sunny` | 2 |

查询为 `agent tool`。人一眼能判断 A 最相关，B 次之，C 无关。

`NewIndex(chunks)` 为每个块计算：

- `terms[词]`：词在当前块出现几次，即 TF；
- `size`：当前块分词后的总词数；
- `docFreq[词]`：有多少个块至少包含一次该词，即 DF；
- `avgLength`：所有块的平均词数。

这组语料的统计是：

| 统计项 | 值 |
| --- | --- |
| A 的 TF | `agent=1`, `tool=2` |
| B 的 TF | `agent=1`, `memory=1` |
| C 的 TF | `weather=1`, `sunny=1` |
| `DF(agent)` | 2，因为 A、B 包含它 |
| `DF(tool)` | 1，因为只有 A 包含它 |
| 块总数 N | 3 |
| 平均长度 | `(3+2+2)/3 = 7/3 ≈ 2.333` |

A 中虽然有两个 `tool`，`DF(tool)` 仍是 1；DF 按包含该词的块数计算。

## 5. 第四步：手算 BM25

`Search` 固定使用 `k1=1.5`、`b=0.75`：

```text
IDF(q) = ln(1 + (N - DF(q) + 0.5) / (DF(q) + 0.5))

词得分 = IDF(q) × TF × (k1 + 1)
                    ─────────────────────────────────────
                    TF + k1 × (1 - b + b × 块长度 / 平均长度)

块总分 = 查询中每个词的词得分之和
```

先算查询词的稀有程度：

```text
IDF(agent) = ln(1 + (3-2+0.5)/(2+0.5)) ≈ 0.4700
IDF(tool)  = ln(1 + (3-1+0.5)/(1+0.5)) ≈ 0.9808
```

`tool` 只出现在一个块里，比出现在两个块里的 `agent` 更有区分度。

A 的长度为 3，长度归一项是：

```text
1.5 × (1 - 0.75 + 0.75 × 3/(7/3)) ≈ 1.8214
```

代入 A 的 `TF(agent)=1`、`TF(tool)=2`：

```text
agent 部分 ≈ 0.4165
tool  部分 ≈ 1.2833
A 总分     ≈ 1.6998
```

B 只有 `agent` 命中，长度为 2，总分约 `0.5023`。C 没有查询词，总分为 0。

最终顺序为：

```text
A  1.6998
B  0.5023
C  0，不返回
```

这体现了 BM25 的三点：稀有词更重要；同词多次出现有帮助但会饱和；较长的块会被适度降权。

## 6. `Search` 的准确边界

调用形式是：

```go
results := index.Search(query, limit)
```

当前行为如下：

- `limit <= 0` 时默认取 Top 5；
- 正数 `limit` 就是本次上限，没有额外的全局封顶；
- 只返回总分严格大于 0 的块；
- 没有词重叠时返回空切片；
- 当前没有最低分阈值；
- 并列分数没有业务级次序保证，不应依赖其先后顺序。

Top 5 是“最多五条”，不是保证五条。调用方必须显式处理无命中：

```go
results := index.Search(question, 5)
if len(results) == 0 {
    return "没有在本地资料中找到相关内容"
}
```

不要把空的“参考资料”交给模型，再期待它自己意识到没有证据。

## 7. 来源字段不可省

`Chunk` 的公开字段是：

```go
type Chunk struct {
    Source string
    Line   int
    Text   string
}
```

文档检索工具可以把结果标成 `[docs/runtime.md:29]`，下一行附上命中的原文。

`Text` 用于回答，`Source` 和 `Line` 用于回到原文核对；`Score` 主要用于排序。

来源元数据只让答案可核对，不能自动保证模型忠实引用。工具说明仍应要求模型依据片段作答，并在资料不足时明确说明。

长期记忆也复用了这个检索器，但把 `Source` 用作 `personal` / `team` 作用域，没有文件行号语义。普通文档工具应保留真实路径和行号。

## 8. 运行现有测试

在仓库根目录执行：

```bash
go test ./internal/search -count=1 -v
```

关键输出应包含三个通过的测试；耗时会因机器而异：

```text
=== RUN   TestTokenizeMixedLanguages
--- PASS: TestTokenizeMixedLanguages
=== RUN   TestSplitTextKeepsLinesAndSize
--- PASS: TestSplitTextKeepsLinesAndSize
=== RUN   TestSearchRanksRelevantChunkFirst
--- PASS: TestSearchRanksRelevantChunkFirst
PASS
```

也可以只跑一项：

```bash
go test ./internal/search -run '^TestTokenizeMixedLanguages$' -count=1 -v
go test ./internal/search -run '^TestSearchRanksRelevantChunkFirst$' -count=1 -v
```

第二个测试会创建三个临时文件，调用 `BuildFromDir`，断言相关文件排第一；随后查询不存在的词，并断言结果为空。

## 9. 接成 Agent Tool

RAG 不需要修改 Runner：模型调用 `docs_search`，工具执行 `index.Search(query, 5)` 并返回 `Source`、`Line`、`Text`，模型再依据片段回答。

索引应建在工具闭包外，不要每次调用工具都重新遍历目录。工具还应校验查询、处理无命中、限制返回总长度，并在资料变化后重建索引。

本项目的真实复用者是长期记忆：`internal/memory/memory.go` 调用 `search.NewIndex(chunks).Search`，工具包装位于 `internal/builtin/tools/memory.go`。

## 10. 本地 RAG 与联网搜索核验不同

| | 本地文档 RAG | `web_search` |
| --- | --- | --- |
| 索引 | 当前应用或用户控制 | 外部搜索服务控制 |
| 返回 | 本地原文块 | 标题、链接、第三方摘要 |
| 定位 | `Source` + `Line` | URL |
| 下一步 | 依据片段回答并引用文件 | 再读取候选网页原文 |

当前代码把 `web_search` 标成 `DiscoveryOnly`。模型若只拿摘要就准备结束，Runner 会有界地提醒一次，让它继续使用可用的来源读取能力核对原文；`web_fetch` 用于读取公开网页正文。

这只是运行时纠偏，不代表摘要已经核验，也不保证外部网页正确。本地文档工具同样要说明语料的可信边界。

## 11. 何时升级到向量检索

BM25 擅长函数名、命令、错误码、配置键、产品名和版本号；弱点是“意思相同但用词不同”。

向量升级仍沿用现有流程：

1. `SplitText` 得到带来源的块；
2. 为每块计算 Embedding，并按内容哈希缓存；
3. 用同一模型计算查询向量；
4. 按余弦相似度排序；
5. 仍返回来源、行号和正文。

代价是索引计算、模型版本管理、文档变更后的重算和更复杂的调试。向量相似也不等于事实正确，来源核对仍不可省。

生产系统常把 BM25 与向量结果做排名融合。不要直接相加未经处理的 BM25 原始分数和余弦分数，因为两者量纲不同。

## 12. 练习与答案

先独立完成：

1. 写出 `Tokenize("Go Agent智能 v2")` 和 `Tokenize("定时 任务")` 的结果。
2. 为什么查询不存在的词返回零条，而不是返回最低分的块？
3. `Search(query, 0)`、`Search(query, -1)`、`Search(query, 2)` 最多各返回多少条？
4. 文件只有一个无空行的 1500 字段落，会输出几个块？
5. 给检索增加最低分过滤，阈值应该写死在 `Search` 里吗？
6. 为 `docs_search` 设计无歧义的 JSON 结果。

参考答案：

1. `[go agent 智能 v2]` 和 `[定时 任务]`；空格会阻止跨界产生 `时任`。
2. `Search` 只收集 `score > 0` 的块；没有词重叠时 TF 全为 0。
3. 前两者默认最多 5 条，第三个最多 2 条；实际数量还受正分结果数限制。
4. 一个约 1500 字的块；800 是合并阈值，当前实现不拆单个超长段落。
5. 可在 `score > 0` 后过滤，但更适合由调用方配置。不同语料的分数不可共用万能阈值；应补“弱相关被过滤、强相关保留”的测试。
6. 一种结果结构是：

```json
{
  "found": true,
  "results": [
    {"source": "docs/runtime.md", "line": 29, "text": "……", "score": 2.31}
  ]
}
```

无命中时返回 `{"found":false,"message":"没有在本地资料中找到相关内容","results":[]}`，让模型能区分正常无命中和工具执行错误。

下一篇：[学习：Agent 的长期记忆](04-memory.md)。
