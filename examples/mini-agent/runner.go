package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	defaultMaxSteps    = 6
	defaultToolTimeout = 10 * time.Second
)

// Runner 实现最小 Agent 循环：模型决定是否调用工具，代码执行工具，
// 再把结果作为 tool 消息交回模型，直到模型给出最终回答。
type Runner struct {
	Model       Model
	Tools       map[string]Tool
	MaxSteps    int
	ToolTimeout time.Duration
}

func NewRunner(model Model, tools []Tool) (*Runner, error) {
	if model == nil {
		return nil, errors.New("缺少模型")
	}
	indexed := make(map[string]Tool, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Spec.Name)
		if name == "" || tool.Run == nil {
			return nil, errors.New("工具缺少名称或执行函数")
		}
		if _, exists := indexed[name]; exists {
			return nil, fmt.Errorf("工具 %q 重复", name)
		}
		tool.Spec.Name = name
		if tool.Spec.Parameters == nil {
			tool.Spec.Parameters = objectSchema(nil, nil)
		}
		indexed[name] = tool
	}
	return &Runner{
		Model:       model,
		Tools:       indexed,
		MaxSteps:    defaultMaxSteps,
		ToolTimeout: defaultToolTimeout,
	}, nil
}

// Run 返回最终答案和完整消息历史。最后一步不再暴露工具，要求模型收敛。
func (runner *Runner) Run(ctx context.Context, input []Message) (string, []Message, error) {
	if runner == nil || runner.Model == nil {
		return "", nil, errors.New("Agent 尚未初始化")
	}
	if len(input) == 0 {
		return "", nil, errors.New("至少需要一条消息")
	}
	maxSteps := runner.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}
	toolTimeout := runner.ToolTimeout
	if toolTimeout <= 0 {
		toolTimeout = defaultToolTimeout
	}

	history := append([]Message(nil), input...)
	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return "", history, err
		}

		// 最后一步不给工具，避免模型永远“再调用一次”。
		specs := runner.toolSpecs()
		if step == maxSteps {
			specs = nil
		}
		assistant, err := runner.Model.Generate(ctx, ModelRequest{Messages: history, Tools: specs})
		if err != nil {
			return "", history, fmt.Errorf("第 %d 步调用模型: %w", step, err)
		}
		assistant.Role = RoleAssistant
		for index := range assistant.ToolCalls {
			if strings.TrimSpace(assistant.ToolCalls[index].ID) == "" {
				assistant.ToolCalls[index].ID = fmt.Sprintf("call_%d_%d", step, index+1)
			}
		}
		history = append(history, assistant)

		if len(assistant.ToolCalls) == 0 {
			answer := strings.TrimSpace(assistant.Content)
			if answer == "" {
				return "", history, errors.New("模型既没有回答，也没有调用工具")
			}
			return answer, history, nil
		}
		if len(specs) == 0 {
			return "", history, errors.New("已到最后一步，但模型仍然请求工具")
		}

		for _, call := range assistant.ToolCalls {
			result := runner.executeTool(ctx, call, toolTimeout)
			history = append(history, Message{
				Role:       RoleTool,
				Content:    result,
				ToolCallID: call.ID,
				Name:       call.Name,
			})
		}
	}
	return "", history, errors.New("Agent 达到最大步数")
}

func (runner *Runner) toolSpecs() []ToolSpec {
	names := make([]string, 0, len(runner.Tools))
	for name := range runner.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]ToolSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, runner.Tools[name].Spec)
	}
	return specs
}

func (runner *Runner) executeTool(parent context.Context, call ToolCall, timeout time.Duration) string {
	tool, exists := runner.Tools[call.Name]
	if !exists {
		return toolFailure("unknown_tool", fmt.Sprintf("工具 %q 未注册", call.Name))
	}
	arguments := call.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if !json.Valid(arguments) {
		return toolFailure("invalid_arguments", "工具参数不是合法 JSON")
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	result, err := tool.Run(ctx, arguments)
	// context 只能通知工具停止，不能强行抢占一个 Go 函数。即使工具忽略
	// 取消并稍后返回成功，已经越过 deadline 的结果也不能再当作成功使用。
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return toolFailure("timeout", "工具执行超时")
	}
	if err != nil {
		return toolFailure("tool_error", err.Error())
	}
	return result
}

func toolFailure(code, message string) string {
	encoded, _ := json.Marshal(map[string]any{
		"ok": false,
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
	return string(encoded)
}
