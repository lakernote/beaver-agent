package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type modelFunc func(context.Context, ModelRequest) (Message, error)

func (function modelFunc) Generate(ctx context.Context, request ModelRequest) (Message, error) {
	return function(ctx, request)
}

func TestRunnerCompletesToolLoop(t *testing.T) {
	t.Parallel()
	modelCalls := 0
	model := modelFunc(func(_ context.Context, request ModelRequest) (Message, error) {
		modelCalls++
		switch modelCalls {
		case 1:
			if len(request.Tools) != 1 || request.Tools[0].Name != "text_stats" {
				t.Fatalf("first request tools = %#v", request.Tools)
			}
			return Message{ToolCalls: []ToolCall{{
				ID:        "call_test",
				Name:      "text_stats",
				Arguments: json.RawMessage(`{"text":"Agent"}`),
			}}}, nil
		case 2:
			last := request.Messages[len(request.Messages)-1]
			if last.Role != RoleTool || last.ToolCallID != "call_test" {
				t.Fatalf("tool result was not paired: %#v", last)
			}
			if !strings.Contains(last.Content, `"runes":5`) {
				t.Fatalf("unexpected tool result: %s", last.Content)
			}
			return Message{Content: "Agent 有 5 个 Unicode 字符。"}, nil
		default:
			return Message{}, errors.New("unexpected extra model call")
		}
	})
	runner, err := NewRunner(model, []Tool{textStatsTool()})
	if err != nil {
		t.Fatal(err)
	}
	answer, history, err := runner.Run(context.Background(), []Message{{Role: RoleUser, Content: "统计 Agent"}})
	if err != nil {
		t.Fatal(err)
	}
	if answer != "Agent 有 5 个 Unicode 字符。" {
		t.Fatalf("answer = %q", answer)
	}
	if modelCalls != 2 || len(history) != 4 {
		t.Fatalf("calls=%d history=%d", modelCalls, len(history))
	}
}

func TestRunnerReturnsUnknownToolFailureToModel(t *testing.T) {
	t.Parallel()
	modelCalls := 0
	model := modelFunc(func(_ context.Context, request ModelRequest) (Message, error) {
		modelCalls++
		if modelCalls == 1 {
			return Message{ToolCalls: []ToolCall{{ID: "missing", Name: "delete_world", Arguments: json.RawMessage(`{}`)}}}, nil
		}
		last := request.Messages[len(request.Messages)-1]
		if last.Role != RoleTool || !strings.Contains(last.Content, "unknown_tool") {
			t.Fatalf("failure was not returned as a tool message: %#v", last)
		}
		return Message{Content: "这个工具不可用。"}, nil
	})
	runner, err := NewRunner(model, []Tool{textStatsTool()})
	if err != nil {
		t.Fatal(err)
	}
	answer, _, err := runner.Run(context.Background(), []Message{{Role: RoleUser, Content: "测试未知工具"}})
	if err != nil {
		t.Fatal(err)
	}
	if answer != "这个工具不可用。" {
		t.Fatalf("answer = %q", answer)
	}
}

func TestRunnerRejectsInvalidArgumentsBeforeExecution(t *testing.T) {
	t.Parallel()
	executed := false
	tool := Tool{
		Spec: ToolSpec{Name: "safe_tool"},
		Run: func(context.Context, json.RawMessage) (string, error) {
			executed = true
			return `{}`, nil
		},
	}
	modelCalls := 0
	model := modelFunc(func(_ context.Context, request ModelRequest) (Message, error) {
		modelCalls++
		if modelCalls == 1 {
			return Message{ToolCalls: []ToolCall{{Name: "safe_tool", Arguments: json.RawMessage(`{"broken"`)}}}, nil
		}
		if !strings.Contains(request.Messages[len(request.Messages)-1].Content, "invalid_arguments") {
			t.Fatal("expected invalid_arguments tool result")
		}
		return Message{Content: "参数错误，已经停止。"}, nil
	})
	runner, err := NewRunner(model, []Tool{tool})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runner.Run(context.Background(), []Message{{Role: RoleUser, Content: "运行"}}); err != nil {
		t.Fatal(err)
	}
	if executed {
		t.Fatal("tool executed with invalid JSON")
	}
}

func TestTextStatsRejectsUnknownArgument(t *testing.T) {
	t.Parallel()
	_, err := runTextStats(context.Background(), json.RawMessage(`{"text":"Agent","surprise":true}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunnerCancelsSlowTool(t *testing.T) {
	t.Parallel()
	tool := Tool{
		Spec: ToolSpec{Name: "slow"},
		Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}
	modelCalls := 0
	model := modelFunc(func(_ context.Context, request ModelRequest) (Message, error) {
		modelCalls++
		if modelCalls == 1 {
			return Message{ToolCalls: []ToolCall{{Name: "slow", Arguments: json.RawMessage(`{}`)}}}, nil
		}
		if !strings.Contains(request.Messages[len(request.Messages)-1].Content, "timeout") {
			t.Fatal("expected timeout tool result")
		}
		return Message{Content: "工具超时。"}, nil
	})
	runner, err := NewRunner(model, []Tool{tool})
	if err != nil {
		t.Fatal(err)
	}
	runner.ToolTimeout = 10 * time.Millisecond
	if _, _, err := runner.Run(context.Background(), []Message{{Role: RoleUser, Content: "运行"}}); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerRejectsLateSuccessAfterToolDeadline(t *testing.T) {
	t.Parallel()
	tool := Tool{
		Spec: ToolSpec{Name: "ignores_cancel"},
		Run: func(context.Context, json.RawMessage) (string, error) {
			time.Sleep(20 * time.Millisecond)
			return `{"ok":true}`, nil
		},
	}
	modelCalls := 0
	model := modelFunc(func(_ context.Context, request ModelRequest) (Message, error) {
		modelCalls++
		if modelCalls == 1 {
			return Message{ToolCalls: []ToolCall{{Name: "ignores_cancel", Arguments: json.RawMessage(`{}`)}}}, nil
		}
		if !strings.Contains(request.Messages[len(request.Messages)-1].Content, "timeout") {
			t.Fatal("expected late success to become timeout")
		}
		return Message{Content: "工具没有按时完成。"}, nil
	})
	runner, err := NewRunner(model, []Tool{tool})
	if err != nil {
		t.Fatal(err)
	}
	runner.ToolTimeout = 5 * time.Millisecond
	if _, _, err := runner.Run(context.Background(), []Message{{Role: RoleUser, Content: "运行"}}); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerStopsToolCallsAtLastStep(t *testing.T) {
	t.Parallel()
	model := modelFunc(func(_ context.Context, _ ModelRequest) (Message, error) {
		return Message{ToolCalls: []ToolCall{{Name: "text_stats", Arguments: json.RawMessage(`{"text":"x"}`)}}}, nil
	})
	runner, err := NewRunner(model, []Tool{textStatsTool()})
	if err != nil {
		t.Fatal(err)
	}
	runner.MaxSteps = 2
	_, _, err = runner.Run(context.Background(), []Message{{Role: RoleUser, Content: "永远调用工具"}})
	if err == nil || !strings.Contains(err.Error(), "最后一步") {
		t.Fatalf("err = %v", err)
	}
}
