package tools

import (
	"context"
	"strings"
	"testing"
)

func TestTodoWrite(t *testing.T) {
	tool := todoWriteTool()
	run := func(args string) (string, error) { return tool.Run(context.Background(), []byte(args)) }

	out, err := run(`{"todos":[{"content":"读代码","status":"completed"},{"content":"改实现","status":"in_progress"},{"content":"跑测试","status":"pending"}]}`)
	if err != nil || !strings.Contains(out, "1. [x] 读代码") || !strings.Contains(out, "2. [~] 改实现") || !strings.Contains(out, "3. [ ] 跑测试") {
		t.Fatalf("清单渲染不对: %q %v", out, err)
	}
	for name, args := range map[string]string{
		"空清单":            `{"todos":[]}`,
		"两项 in_progress": `{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"in_progress"}]}`,
		"未知状态":           `{"todos":[{"content":"a","status":"done"}]}`,
		"空内容":            `{"todos":[{"content":"  ","status":"pending"}]}`,
	} {
		if _, err := run(args); err == nil {
			t.Fatalf("%s 应该报错", name)
		}
	}
}
