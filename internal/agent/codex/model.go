// Package codex exposes an official Codex app-server turn as a Beaver model.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lakernote/beaver-agent/internal/agent"
	"github.com/lakernote/beaver-agent/internal/codexruntime"
	"github.com/lakernote/beaver-agent/internal/redact"
)

const (
	Protocol = "app_server"
	dispatch = "beaver_call_tool"
)

type RunTurn func(context.Context, codexruntime.Config, string) (codexruntime.Result, error)

// Model belongs to one Beaver Runner. Codex waits for each tool result while
// the Runner performs its regular validation, approval, execution and tracing.
type Model struct {
	run     RunTurn
	timeout time.Duration
	active  *turn
}

type turn struct {
	cancel  context.CancelFunc
	done    chan struct{}
	events  chan turnEvent
	pending *toolCall
	catalog string
}

type turnEvent struct {
	delta  string
	call   *toolCall
	result codexruntime.Result
	err    error
}

type toolCall struct {
	call  agent.ToolCall
	reply chan any
}

func New(run RunTurn, timeout time.Duration) *Model {
	return &Model{run: run, timeout: timeout}
}

func (*Model) Capabilities() agent.ModelCapabilities {
	return agent.ModelCapabilities{
		Provider: "chatgpt-codex", Protocol: Protocol, NativeTools: true, Streaming: true, MultimodalInput: true,
	}
}

func (model *Model) Close() error {
	if active := model.active; active != nil {
		model.active = nil
		active.cancel()
		<-active.done
	}
	return nil
}

func (model *Model) Generate(ctx context.Context, request agent.Request) (response agent.Response, err error) {
	startedAt := time.Now()
	tools := request.Tools
	if request.ToolChoice.Mode == agent.ToolChoiceNone {
		tools = nil
	}
	catalog := toolCatalog(tools)
	continued := model.active != nil
	defer func() {
		if err != nil {
			_ = model.Close()
		}
		history := "full_history"
		messages := len(request.Messages)
		if continued {
			history, messages = "codex_tool_callback", 1
		}
		response.Exchange = agent.Exchange{
			Model: request.Model, Protocol: Protocol, Duration: time.Since(startedAt),
			Response: redact.Marshal(response.Message, 8192), Usage: response.Usage, StopReason: response.StopReason,
			HistoryMode: history, RequestMessages: messages, ToolDefinitions: len(tools),
		}
	}()

	if !continued {
		config, prompt, err := turnInput(request, catalog, len(tools) > 0)
		if err != nil {
			return agent.Response{}, err
		}
		model.start(ctx, config, prompt, catalog)
	} else {
		if err := model.active.returnToolResult(request.Messages, catalog); err != nil {
			return agent.Response{}, err
		}
	}

	active := model.active
	wait, cancel := context.WithTimeout(ctx, model.timeout)
	defer cancel()
	for {
		select {
		case <-wait.Done():
			if ctx.Err() != nil {
				return agent.Response{}, ctx.Err()
			}
			return agent.Response{}, errors.New("Codex 模型请求超时")
		case event := <-active.events:
			switch {
			case event.delta != "":
				if request.OnTextDelta != nil {
					request.OnTextDelta(event.delta)
				}
			case event.call != nil:
				call := event.call.call
				if !offersTool(tools, call.Name) {
					return agent.Response{}, fmt.Errorf("Codex 请求了当前步骤不可用的工具 %q", call.Name)
				}
				active.pending = event.call
				return agent.Response{StopReason: agent.StopReasonToolUse,
					Message: agent.Message{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{call}}}, nil
			default:
				_ = model.Close()
				result := event.result
				response := agent.Response{
					StopReason: agent.StopReason(result.StopReason), Message: agent.Message{Role: agent.RoleAssistant, Content: result.Answer},
					Usage: agent.Usage{
						InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens,
						CachedInputTokens: result.Usage.CachedInputTokens, CacheWriteTokens: result.Usage.CacheWriteInputTokens,
						TotalTokens: result.Usage.TotalTokens, CacheReported: result.Usage.Reported,
					},
				}
				return response, event.err
			}
		}
	}
}

func (model *Model) start(parent context.Context, config codexruntime.Config, prompt, catalog string) {
	ctx, cancel := context.WithCancel(parent)
	active := &turn{cancel: cancel, done: make(chan struct{}), events: make(chan turnEvent, 8), catalog: catalog}
	model.active = active
	emit := func(event turnEvent) bool {
		select {
		case active.events <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}
	config.OnDelta = func(delta string) { emit(turnEvent{delta: delta}) }
	config.OnServerRequest = func(request codexruntime.ServerRequest) (any, error) {
		call, err := parseToolCall(request)
		if err != nil {
			return nil, err
		}
		pending := &toolCall{call: call, reply: make(chan any, 1)}
		if emit(turnEvent{call: pending}) {
			select {
			case output := <-pending.reply:
				return output, nil
			case <-ctx.Done():
			}
		}
		return nil, ctx.Err()
	}
	go func() {
		defer close(active.done)
		result, err := model.run(ctx, config, prompt)
		emit(turnEvent{result: result, err: err})
	}()
}

func parseToolCall(request codexruntime.ServerRequest) (agent.ToolCall, error) {
	var incoming struct {
		Tool, CallID string
		Arguments    struct {
			Name      string
			Arguments json.RawMessage
		}
	}
	if request.Method != "item/tool/call" || json.Unmarshal(request.Params, &incoming) != nil ||
		incoming.Tool != dispatch || incoming.CallID == "" || incoming.Arguments.Name == "" {
		return agent.ToolCall{}, errors.New("Beaver 只接收已注册的 Codex 工具请求")
	}
	var arguments map[string]json.RawMessage
	if json.Unmarshal(incoming.Arguments.Arguments, &arguments) != nil || arguments == nil {
		return agent.ToolCall{}, errors.New("Beaver 工具参数必须是 JSON 对象")
	}
	return agent.ToolCall{ID: incoming.CallID, Name: incoming.Arguments.Name, Arguments: incoming.Arguments.Arguments}, nil
}

func (active *turn) returnToolResult(messages []agent.Message, catalog string) error {
	if active.pending == nil {
		return errors.New("Codex 没有等待工具结果")
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != agent.RoleTool || message.ToolCallID != active.pending.call.ID {
			continue
		}
		content := []map[string]any{{"type": "inputText", "text": message.Content}}
		success := message.ToolResult == nil || !message.ToolResult.IsError
		if catalog != active.catalog {
			content = append(content, map[string]any{"type": "inputText", "text": "Beaver 可用工具已更新：" + catalog})
			active.catalog = catalog
		}
		if message.ToolResult != nil {
			for _, block := range message.ToolResult.Content {
				if block.Type == "image" && len(block.Data) > 0 {
					content = append(content, map[string]any{"type": "inputImage", "imageUrl": imageURL(block.MIMEType, block.Data)})
				}
			}
		}
		active.pending.reply <- map[string]any{"success": success, "contentItems": content}
		active.pending = nil
		return nil
	}
	return errors.New("Beaver Runner 没有返回与 Codex 调用匹配的工具结果")
}

func offersTool(tools []agent.ToolSpec, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
