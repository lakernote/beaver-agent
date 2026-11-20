package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lakernote/beaver-agent/internal/agent"
	"github.com/lakernote/beaver-agent/internal/memory"
)

// Memory 把记忆存储和“当前会话属于谁”绑在一起。
// 每个会话创建一份，所以工具参数里不需要（也不允许）模型指定 owner，
// 模型无法越权读取别人的个人记忆。
type Memory struct {
	Store *memory.Store
	Owner string
}

const (
	groupMemory       = "memory"
	categoryMemory    = "记忆"
	memorySearchLimit = 5
)

// memoryTools 返回三个工具：保存、检索、遗忘。
// 记忆是“稳定且以后还会用到”的事实（偏好、约定、环境信息），不是聊天流水账。
func memoryTools(mem Memory) []agent.Tool {
	scopeSchema := map[string]any{
		"type": "string", "enum": []string{memory.ScopePersonal, memory.ScopeTeam},
		"description": "personal=只有当前成员可见（默认）；team=团队所有成员可见，仅用于团队共同约定",
	}
	return []agent.Tool{
		{
			Spec: agent.ToolSpec{
				Name:        "memory_save",
				Description: "保存一条长期记忆：用户的稳定偏好、团队约定、环境信息等以后还会用到的事实。一条一个事实，写成简短完整的句子；不要保存临时任务进度、密码或密钥。用户明确说“记住……”时调用。",
				Parameters: objectSchema(map[string]any{
					"text": stringSchema("要记住的事实，一句话"), "scope": scopeSchema,
				}, []string{"text"}),
			},
			Run: func(_ context.Context, raw json.RawMessage) (string, error) {
				var args struct{ Text, Scope string }
				if err := json.Unmarshal(raw, &args); err != nil {
					return "", fmt.Errorf("memory_save 参数错误: %w", err)
				}
				if err := mem.Store.Save(mem.Owner, args.Scope, args.Text); err != nil {
					return "", err
				}
				return "已记住", nil
			},
		},
		{
			Spec: agent.ToolSpec{
				Name:        "memory_search",
				Description: "检索长期记忆（当前成员的个人记忆和团队记忆）。回答涉及用户偏好、历史约定或“之前说过”的内容前调用。没有命中时如实说明，不要编造。",
				Parameters:  objectSchema(map[string]any{"query": stringSchema("检索关键词或问题")}, []string{"query"}),
			},
			Run: func(_ context.Context, raw json.RawMessage) (string, error) {
				var args struct{ Query string }
				if err := json.Unmarshal(raw, &args); err != nil {
					return "", fmt.Errorf("memory_search 参数错误: %w", err)
				}
				notes, err := mem.Store.Search(mem.Owner, args.Query, memorySearchLimit)
				if err != nil {
					return "", err
				}
				if len(notes) == 0 {
					return "没有找到相关记忆", nil
				}
				var out strings.Builder
				for _, note := range notes {
					fmt.Fprintf(&out, "[%s] %s\n", note.Scope, note.Text)
				}
				return out.String(), nil
			},
		},
		{
			Spec: agent.ToolSpec{
				Name:        "memory_forget",
				Description: "删除包含指定关键词的记忆，用于纠正过时或错误的内容。用户明确要求忘记某件事时调用。",
				Parameters: objectSchema(map[string]any{
					"keyword": stringSchema("记忆中包含的关键词，匹配的条目都会被删除"), "scope": scopeSchema,
				}, []string{"keyword"}),
			},
			Run: func(_ context.Context, raw json.RawMessage) (string, error) {
				var args struct{ Keyword, Scope string }
				if err := json.Unmarshal(raw, &args); err != nil {
					return "", fmt.Errorf("memory_forget 参数错误: %w", err)
				}
				removed, err := mem.Store.Forget(mem.Owner, args.Scope, args.Keyword)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("已删除 %d 条记忆", removed), nil
			},
		},
	}
}
