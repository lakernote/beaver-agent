package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lakernote/beaver-agent/internal/agent"
)

const (
	groupPlanning    = "planning"
	categoryPlanning = "规划"
	maxTodoItems     = 20
	maxTodoRunes     = 200
)

// todoWriteTool 让模型把复杂任务拆成步骤清单，并在执行中不断更新。
//
// 它故意没有任何存储：每次调用都提交“完整清单”，工具只校验并原样回显。
// 清单因此就存在对话历史（以及 Trace）里——模型下一轮看到的就是最新的计划。
// 这是最小的“规划”实现：不新增状态、不新增表，能力全部来自模型的自觉。
func todoWriteTool() agent.Tool {
	return agent.Tool{
		Spec: agent.ToolSpec{
			Name:        "todo_write",
			Description: "为需要 3 步以上的复杂任务维护步骤清单。开始前先列出步骤；每完成一步立刻更新状态。每次都提交完整清单（会覆盖上一次）。同一时刻只能有一项 in_progress。简单问答或一两步能完成的事不要使用。",
			Parameters: objectSchema(map[string]any{
				"todos": map[string]any{
					"type": "array", "description": "完整的步骤清单",
					"items": objectSchema(map[string]any{
						"content": stringSchema("这一步要做什么，一句话"),
						"status":  map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
					}, []string{"content", "status"}),
				},
			}, []string{"todos"}),
		},
		Run: func(_ context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Todos []struct{ Content, Status string }
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("todo_write 参数错误: %w", err)
			}
			if len(args.Todos) == 0 || len(args.Todos) > maxTodoItems {
				return "", fmt.Errorf("清单需要 1 到 %d 项", maxTodoItems)
			}
			marks := map[string]string{"pending": "[ ]", "in_progress": "[~]", "completed": "[x]"}
			var out strings.Builder
			active := 0
			for index, item := range args.Todos {
				content := strings.TrimSpace(item.Content)
				mark, ok := marks[item.Status]
				if content == "" || utf8.RuneCountInString(content) > maxTodoRunes || !ok {
					return "", fmt.Errorf("第 %d 项无效：content 需非空且不超过 %d 字，status 只能是 pending/in_progress/completed", index+1, maxTodoRunes)
				}
				if item.Status == "in_progress" {
					active++
				}
				fmt.Fprintf(&out, "%d. %s %s\n", index+1, mark, content)
			}
			if active > 1 {
				return "", fmt.Errorf("同一时刻只能有一项 in_progress，现在有 %d 项", active)
			}
			return "清单已更新：\n" + out.String(), nil
		},
	}
}
