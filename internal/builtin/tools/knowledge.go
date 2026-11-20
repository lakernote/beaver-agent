package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lakernote/beaver-agent/internal/agent"
	"github.com/lakernote/beaver-agent/internal/knowledge"
)

const knowledgeSearchLimit = 5

// knowledgeSearchTool 是 RAG 在 Agent 里的样子：一个普通工具。
// 模型自己决定何时查；工具只负责“检索并带出处返回”，由模型据此作答。
func knowledgeSearchTool(store *knowledge.Store) agent.Tool {
	return agent.Tool{
		Spec: agent.ToolSpec{
			Name:        "knowledge_search",
			Description: "在团队知识库（用户上传的文档）中检索相关片段，返回内容和出处文件。问题涉及团队文档、内部流程、产品或运维手册时调用；回答时标注出处文件名。没有命中时如实说明，不要编造。",
			Parameters:  objectSchema(map[string]any{"query": stringSchema("检索关键词或问题")}, []string{"query"}),
		},
		Run: func(_ context.Context, raw json.RawMessage) (string, error) {
			var args struct{ Query string }
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("knowledge_search 参数错误: %w", err)
			}
			results, err := store.Search(args.Query, knowledgeSearchLimit)
			if err != nil {
				return "", err
			}
			if len(results) == 0 {
				return "知识库中没有找到相关内容", nil
			}
			var out strings.Builder
			for _, hit := range results {
				fmt.Fprintf(&out, "[%s:%d]\n%s\n\n", hit.Source, hit.Line, hit.Text)
			}
			return out.String(), nil
		},
	}
}
