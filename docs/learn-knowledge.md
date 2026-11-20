# 学习：团队知识库

知识库就是 RAG 的落地：用户上传文档，Agent 需要时检索相关片段，再据此回答并标注出处。
检索原理见 [learn-rag.md](learn-rag.md)（BM25 关键词检索，不用向量）。

## 数据流

```
Web 上传 / 微信发文件+"/kb"
        ↓
~/.beaver/knowledge/*.md|*.txt      （全队共享，一个文件一份文档）
        ↓
knowledge_search(query) 工具        （模型自己决定何时调用）
        ↓
BM25 检索 → 返回 [文件名:行号] 片段
        ↓
模型据片段作答，并标注出处
```

## 关键代码

| 位置 | 作用 |
| --- | --- |
| `internal/knowledge/knowledge.go` | 文档增删查；每次检索现读目录建 BM25 索引（几百份文档足够快，没有缓存失效问题） |
| `internal/builtin/tools/knowledge.go` | `knowledge_search` 工具，只负责“检索并带出处” |
| `internal/server/api_knowledge.go` | `GET/POST/DELETE /api/v1/knowledge` |
| `internal/server/weixin_inbox.go` | 微信 `/kb`：文件 + 文字 `/kb` 入库 |
| `web/src/Knowledge.tsx` | 设置 → 知识库页面 |

## 设计取舍

- **为什么是工具而不是每轮自动检索？** 闲聊不需要查库；让模型自己判断，省 token，也更易理解。
- **限制**：仅 `.md` / `.txt`，单文件 2MB，最多 200 份；文件名不允许路径，防止越界写。
- **只有 Beaver Runtime 能用**；Codex Runtime 不带这个工具。
- **全队共享**：任何成员上传的文档所有人可检索、可删除（与默认安全策略一致，未做权限分级）。

## 可以继续练手

1. 给片段加上相邻上下文再返回。
2. 支持 PDF（先转文本）。
3. 检索量大后给索引加缓存，按目录修改时间失效。
