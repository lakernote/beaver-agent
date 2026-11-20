package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	defaultMaxSteps = 32
	// Shell 自己默认 60 秒、最多 300 秒。Runner 的外层上限必须允许 Shell 的
	// 显式长任务，否则页面写 300 秒而实际 60 秒就会被提前取消。
	defaultToolTimeout = 5 * time.Minute
)

// Runner 是 Beaver 的核心。它只负责一个很小的循环：
//
//	模型 -> 工具调用 -> 工具结果 -> 模型 -> 最终回答
//
// Skill 通过提示词指导模型，MCP 只需注册成 Tool；二者都不需要在这里增加分支。
type Runner struct {
	Model           Model
	ModelName       string
	Tools           []Tool
	Temperature     float64
	MaxOutputTokens int
	ReasoningEffort string
	Observe         Observer
	modelToolSpecs  []ToolSpec
	toolsByName     map[string]Tool
}

// NewRunner 校验并索引工具。工具名必须唯一，避免模型调用时产生歧义。
func NewRunner(model Model, modelName string, tools []Tool) (*Runner, error) {
	if model == nil {
		return nil, errors.New("Agent 缺少模型")
	}
	// Temperature 为 0 时适配器会省略该字段。OpenAI 的部分推理模型不接受
	// temperature，而 Ollama/DeepSeek 也不需要 Runtime 强行覆盖模型默认值。
	runner := &Runner{Model: model, ModelName: strings.TrimSpace(modelName), MaxOutputTokens: 4096}
	runner.toolsByName = make(map[string]Tool, len(tools))
	runner.modelToolSpecs = make([]ToolSpec, 0, len(tools))
	if err := runner.AddTools(tools); err != nil {
		return nil, err
	}
	return runner, nil
}

// AddTools 在一次运行中动态追加工具。它让工具组或 MCP 搜索先暴露一个很小的入口，
// 只有模型确认需要某项能力时，才把少量真实工具加入下一轮模型请求。
// 整批工具会先完成校验再写入，避免校验失败后只追加了一半。
func (runner *Runner) AddTools(tools []Tool) error {
	if runner == nil {
		return errors.New("Agent 尚未初始化")
	}
	prepared := make([]Tool, 0, len(tools))
	seen := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Spec.Name)
		if name == "" || (tool.Execute == nil && tool.Run == nil) {
			return errors.New("Agent 工具缺少名称或执行器")
		}
		if _, exists := runner.toolsByName[name]; exists {
			return fmt.Errorf("Agent 工具 %q 重复", name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("Agent 工具 %q 重复", name)
		}
		seen[name] = struct{}{}
		tool.Spec.Name = name
		if tool.Spec.Parameters == nil {
			tool.Spec.Parameters = map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
		}
		prepared = append(prepared, tool)
	}
	for _, tool := range prepared {
		name := tool.Spec.Name
		runner.toolsByName[name] = tool
		runner.modelToolSpecs = append(runner.modelToolSpecs, tool.Spec)
		runner.Tools = append(runner.Tools, tool)
	}
	return nil
}

// runState 保存一次 Run 中随步骤变化的全部状态。
// 把它们集中在一处，Run 的主循环就只剩“选工具 → 问模型 → 跑工具”三件事。
type runState struct {
	messages           []Message // 完整对话历史（含本轮新增的 assistant / tool 消息）
	pending            []Message // 本步需要发给模型的“增量”消息，供支持 continuation 的 Provider 使用
	previousResponseID string
	usage              Usage

	requiredToolNames []string                  // 用户通过 @tool 指定的工具，本轮只暴露它们
	failedCalls       map[string]failedToolCall // 已失败的调用，防止原样重复

	consecutiveFailedToolSteps int  // 连续“所有工具都失败”的步数
	forceConverge              bool // 为 true 时下一步不再给工具，逼模型收敛回答
	requireLoadedToolCall      bool // Loader 刚加载完能力，下一步必须调用真实工具
	hasCurrentRealToolResult   bool // 本次 Run 是否成功执行过真实工具

	pendingSourceVerification      bool // 只拿到搜索摘要，还没读过原始来源
	sourceVerificationReminderSent bool // 纠偏提醒最多发一次
}

// syncFrom 用 PrepareRequest（如上下文压缩）改写后的请求同步状态。
func (state *runState) syncFrom(request Request) {
	state.messages = append([]Message(nil), request.Messages...)
	state.pending = append([]Message(nil), request.NewMessages...)
	state.previousResponseID = strings.TrimSpace(request.PreviousResponseID)
}

// stepToolStats 汇总一步内所有工具调用的结果，用来决定下一步的策略。
type stepToolStats struct {
	failed                                int
	loader, real, discovery, verification bool
}

// Run 执行一次任务，这就是整个 Agent 循环：
//
//	for 每一步 {
//	    1. 决定本步暴露哪些工具
//	    2. 调用模型（含重试）
//	    3. 没有工具调用 -> 得到最终回答，结束
//	    4. 有工具调用   -> 逐个执行，把结果追加到历史，进入下一步
//	}
//
// 最后一步会停止暴露工具，要求模型基于已有结果收敛回答。
func (runner *Runner) Run(ctx context.Context, input RunRequest) (RunResult, error) {
	if runner == nil || runner.Model == nil {
		return RunResult{}, errors.New("Agent 尚未初始化")
	}
	if len(input.Messages) == 0 {
		return RunResult{}, errors.New("Agent 至少需要一条消息")
	}
	maxSteps := input.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}
	toolTimeout := input.ToolTimeout
	if toolTimeout <= 0 {
		toolTimeout = defaultToolTimeout
	}

	state := &runState{
		messages:           append([]Message(nil), input.Messages...),
		pending:            append([]Message(nil), input.NewMessages...),
		previousResponseID: strings.TrimSpace(input.PreviousResponseID),
		requiredToolNames:  availableRequiredTools(runner.modelToolSpecs, input.RequiredToolNames),
		failedCalls:        make(map[string]failedToolCall),
	}
	if len(state.pending) == 0 {
		state.pending = append(state.pending, input.Messages...)
	}

	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return RunResult{}, err
		}

		// 1. 本步暴露的工具，以及模型是否可以调用它们。
		tools, toolChoice := runner.chooseTools(state, step == maxSteps)
		request := Request{
			Model: runner.ModelName, Messages: state.messages, NewMessages: state.pending, Tools: tools,
			ToolChoice: toolChoice, PromptCacheKey: input.PromptCacheKey,
			Temperature: runner.Temperature, MaxOutputTokens: runner.MaxOutputTokens,
			ReasoningEffort: runner.ReasoningEffort, PreviousResponseID: state.previousResponseID,
			OnTextDelta: input.OnTextDelta,
		}
		if input.PrepareRequest != nil {
			prepared, changed, err := input.PrepareRequest(ctx, request, false)
			if err != nil {
				return RunResult{}, err
			}
			request = prepared
			if changed {
				state.syncFrom(request)
			}
			tools = request.Tools
		}

		// 2. 调用模型。
		response, err := runner.generateWithRecovery(ctx, input, state, request, step)
		if err != nil {
			return RunResult{}, err
		}
		addUsage(&state.usage, response.Usage)
		if response.ID != "" {
			state.previousResponseID = response.ID
		}
		assistant := response.Message
		assistant.Role = RoleAssistant
		for index, call := range assistant.ToolCalls {
			if strings.TrimSpace(call.ID) == "" {
				call.ID = fmt.Sprintf("call_%d_%d", step, index+1)
			}
			assistant.ToolCalls[index] = runner.describeToolCall(call)
		}
		state.messages = append(state.messages, assistant)
		turnMessages := []Message{assistant}
		if input.OnTurnMessages == nil && input.OnMessage != nil {
			if err := input.OnMessage(assistant); err != nil {
				return RunResult{}, err
			}
		}

		// 3. 模型没有请求工具：这就是最终回答（除非触发下面的纠偏）。
		if len(assistant.ToolCalls) == 0 {
			if step == 1 && len(state.requiredToolNames) > 0 {
				return RunResult{}, fmt.Errorf("用户明确指定了工具 %s，但模型没有发起工具调用", strings.Join(state.requiredToolNames, ", "))
			}
			if state.requireLoadedToolCall {
				return RunResult{}, errors.New("模型在能力加载后没有调用真实工具")
			}
			if runner.remindSourceVerification(input, state, step, maxSteps) {
				continue
			}
			if input.OnTurnMessages != nil {
				if err := input.OnTurnMessages(turnMessages); err != nil {
					return RunResult{}, err
				}
			}
			answer := strings.TrimSpace(assistant.Content)
			if answer == "" {
				return RunResult{}, errors.New("模型既没有返回回答，也没有调用工具")
			}
			return RunResult{Answer: answer, Messages: state.messages, Usage: state.usage, ResponseID: state.previousResponseID, Steps: step}, nil
		}
		if len(tools) == 0 {
			return RunResult{}, errors.New("本步已禁用工具，但模型仍在请求工具")
		}

		// 4. 逐个执行工具，把结果作为 tool 消息追加到历史。
		state.pending = state.pending[:0]
		var stats stepToolStats
		for _, call := range assistant.ToolCalls {
			toolMessage, err := runner.executeToolCall(ctx, input, state, step, call, toolTimeout, &stats)
			if err != nil {
				return RunResult{}, err
			}
			state.messages = append(state.messages, toolMessage)
			state.pending = append(state.pending, toolMessage)
			turnMessages = append(turnMessages, toolMessage)
			if input.OnTurnMessages == nil && input.OnMessage != nil {
				if err := input.OnMessage(toolMessage); err != nil {
					return RunResult{}, err
				}
			}
		}
		runner.updateStrategy(state, stats, len(assistant.ToolCalls))
		if input.OnTurnMessages != nil {
			if err := input.OnTurnMessages(turnMessages); err != nil {
				return RunResult{}, err
			}
		}
	}
	return RunResult{}, errors.New("Agent 达到最大步数")
}

// chooseTools 决定本步给模型看哪些工具。规则按优先级叠加：
//  1. 默认暴露全部工具，tool_choice=auto；没有工具则为 none。
//  2. 用户用 @tool 指定了工具：只暴露这些，且每一步都保持，避免结果回来后突然恢复整套 Schema 浪费 Token。
//     部分 Ollama 模型会忽略 required，所以仍用 auto，由 Run 校验首轮必须真的调用。
//  3. Loader（工具组/MCP 入口）刚加载完能力：本步隐藏所有 Loader，逼模型去用真实工具。
//  4. 连续失败或已是最后一步：不再暴露工具，要求模型根据已有信息回答。
func (runner *Runner) chooseTools(state *runState, lastStep bool) ([]ToolSpec, ToolChoice) {
	tools := runner.modelToolSpecs
	choice := ToolChoice{Mode: ToolChoiceNone}
	if len(tools) > 0 {
		choice = ToolChoice{Mode: ToolChoiceAuto}
	}
	if len(state.requiredToolNames) > 0 {
		tools = filterToolsByName(tools, state.requiredToolNames)
		choice = ToolChoice{Mode: ToolChoiceAuto}
	}
	if state.requireLoadedToolCall {
		tools = nonLoaderTools(tools)
		if len(tools) > 0 {
			choice = ToolChoice{Mode: ToolChoiceAuto}
		}
	}
	if state.forceConverge || lastStep {
		return nil, ToolChoice{Mode: ToolChoiceNone}
	}
	return tools, choice
}

// callModel 发起一次模型调用并校验响应，同时记录 Trace 事件。
func (runner *Runner) callModel(ctx context.Context, request Request, step, attempt int) (Response, error) {
	runner.emit(Event{Kind: EventModelStart, Step: step, Attempt: attempt, StartedAt: time.Now()})
	response, err := runner.Model.Generate(ctx, request)
	if err == nil {
		err = ValidateModelResponse(&response)
	}
	runner.emit(Event{Kind: EventModelEnd, Step: step, Attempt: attempt, Exchange: response.Exchange, Err: err, Duration: response.Exchange.Duration})
	return response, err
}

// generateWithRecovery 调用模型，并只对三类“明确可恢复”的失败做一次补救：
//  1. 429 / 5xx / 临时网络错误：等待后原样重试。
//  2. 空回答或流式+工具组合的兼容问题：关闭流式重试。
//  3. 上下文超长：让 PrepareRequest 压缩历史后重试。
//
// 其他错误（4xx、参数错误、模型内容错误）不会盲目重放。
func (runner *Runner) generateWithRecovery(ctx context.Context, input RunRequest, state *runState, request Request, step int) (Response, error) {
	attempt := 1
	response, err := runner.callModel(ctx, request, step, attempt)

	if err != nil && retryableModelError(err) {
		addUsage(&state.usage, response.Usage)
		resetAttemptOutput(input)
		if waitErr := waitForRetry(ctx, modelRetryDelay(err, attempt)); waitErr != nil {
			return Response{}, waitErr
		}
		attempt++
		response, err = runner.callModel(ctx, request, step, attempt)
	}

	// 少数兼容 Provider 在 stream + tools 下只产出隐藏推理，或把校验错误放进 HTTP 200 的 SSE 尾部。
	// 关闭流式重试一次；只有本次 Run 已成功执行真实工具时，空正文才进入无工具收敛。
	if err != nil && request.OnTextDelta != nil && (errors.Is(err, ErrEmptyModelResponse) || retryWithoutStreaming(err)) {
		addUsage(&state.usage, response.Usage)
		resetAttemptOutput(input)
		if errors.Is(err, ErrEmptyModelResponse) {
			request = prepareEmptyResponseRetry(request, state.hasCurrentRealToolResult)
		} else {
			request.OnTextDelta = nil
		}
		attempt++
		response, err = runner.callModel(ctx, request, step, attempt)
	}

	if err != nil && input.PrepareRequest != nil && input.IsContextError != nil && input.IsContextError(err) {
		prepared, changed, prepareErr := input.PrepareRequest(ctx, request, true)
		if prepareErr != nil {
			return Response{}, fmt.Errorf("%w；自动压缩失败: %v", err, prepareErr)
		}
		if changed {
			resetAttemptOutput(input)
			state.syncFrom(prepared)
			attempt++
			response, err = runner.callModel(ctx, prepared, step, attempt)
		}
	}
	return response, err
}

// remindSourceVerification：Discovery 工具（如 web_search）只返回候选摘要。
// 模型若直接拿摘要作答，就给它一次有界的纠偏机会去读原始来源。
// 返回 true 表示已追加提醒，调用方应 continue 进入下一步。
func (runner *Runner) remindSourceVerification(input RunRequest, state *runState, step, maxSteps int) bool {
	if !state.pendingSourceVerification || state.sourceVerificationReminderSent || step >= maxSteps {
		return false
	}
	state.sourceVerificationReminderSent = true
	reminder := Message{Role: RoleSystem, Content: sourceVerificationReminder}
	state.messages = append(state.messages, reminder)
	state.pending = []Message{reminder}
	resetAttemptOutput(input)
	runner.emit(Event{Kind: EventGuidance, Step: step, Name: "source_verification", Detail: "搜索结果只提供候选摘要；已提醒模型读取原始来源后再回答", StartedAt: time.Now()})
	return true
}

// executeToolCall 处理模型请求的一次工具调用，返回要追加到历史里的 tool 消息。
// 流程：重复失败拦截 -> 审批 -> 真正执行 -> 记录结果。
// 返回的 error 只表示“生命周期回调失败”这类需要中止整个 Run 的错误；
// 工具自身的失败会写进 tool 消息，交给模型决定下一步。
func (runner *Runner) executeToolCall(ctx context.Context, input RunRequest, state *runState, step int, call ToolCall, timeout time.Duration, stats *stepToolStats) (Message, error) {
	key := toolCallKey(call)
	previous, repeated := state.failedCalls[key]

	var (
		toolResult   ToolResult
		toolErr      error
		duration     time.Duration
		lifecycleErr error
		toolStarted  bool
	)
	switch {
	case repeated && (!previous.retryable || previous.attempts >= 2):
		// 同样的调用已经失败过，不再执行，提示模型换思路。
		startedAt := time.Now()
		toolErr = &ToolError{
			Code: "duplicate_failed_call", Message: fmt.Sprintf("相同的工具调用 %s 已失败，不会重复执行", call.Name),
			Hint: "检查上一条错误，修改参数、换用其他工具，或基于已有证据回答", Retryable: false,
		}
		toolResult = toolResultWithError(ToolResult{}, toolErr)
		duration = time.Since(startedAt)
	default:
		if input.OnToolApproval != nil {
			approvalStartedAt := time.Now()
			toolErr = input.OnToolApproval(ctx, call)
			duration = time.Since(approvalStartedAt)
			if toolErr != nil {
				toolResult = toolResultWithError(ToolResult{}, toolErr)
			}
		}
		if toolErr == nil {
			toolResult, toolErr, duration, lifecycleErr, toolStarted = runner.runTool(ctx, step, call, timeout, input.OnToolLifecycle)
		}
	}
	if lifecycleErr != nil {
		return Message{}, lifecycleErr
	}

	// 记录结果，供下一步决定策略。
	if toolErr != nil {
		stats.failed++
		retryable := false
		var failure *ToolError
		if errors.As(toolErr, &failure) {
			retryable = failure.Retryable
		}
		state.failedCalls[key] = failedToolCall{attempts: previous.attempts + 1, retryable: retryable}
	} else {
		delete(state.failedCalls, key)
		tool, exists := runner.toolsByName[call.Name]
		switch {
		case exists && tool.Spec.Loader:
			stats.loader = true
		case exists && tool.Spec.DiscoveryOnly:
			stats.real, stats.discovery = true, true
			state.hasCurrentRealToolResult = true
		default:
			stats.real, stats.verification = true, true
			state.hasCurrentRealToolResult = true
		}
	}

	// Content 是兼容用的文本投影；适配器和存储优先使用 ToolResult，两者必须一致。
	output := toolResult.ModelText()
	resultCopy := toolResult
	eventKind := EventToolReject
	if toolStarted {
		eventKind = EventToolEnd
	}
	runner.emit(Event{Kind: eventKind, Step: step, ToolCall: &call, Result: &resultCopy, Output: output, Err: toolErr, Duration: duration})
	return Message{Role: RoleTool, Name: call.Name, ToolCallID: call.ID, Content: output, ToolResult: &resultCopy}, nil
}

// updateStrategy 根据本步工具结果调整下一步策略。
func (runner *Runner) updateStrategy(state *runState, stats stepToolStats, calls int) {
	if stats.failed == calls {
		state.consecutiveFailedToolSteps++
	} else {
		state.consecutiveFailedToolSteps = 0
	}
	// 连续两步所有工具都失败，说明没有取得新证据：停止暴露工具，让模型根据现有错误收敛，
	// 而不是用略有不同的参数无限重复。
	state.forceConverge = state.consecutiveFailedToolSteps >= 2
	state.requireLoadedToolCall = stats.loader && !stats.real
	if stats.verification {
		state.pendingSourceVerification = false
	} else if stats.discovery {
		state.pendingSourceVerification = true
	}
}

func (runner *Runner) describeToolCall(call ToolCall) ToolCall {
	tool, ok := runner.toolsByName[call.Name]
	if !ok {
		return call
	}
	call.ActivityKind = tool.Spec.ActivityKind
	call.ActivitySource = tool.Spec.ActivitySource
	call.DisplayName = tool.Spec.DisplayName
	return call
}

const visibleAnswerReminder = "上一响应没有可展示的最终正文。不要重复调用已经成功的工具；请根据已有工具结果，在 assistant content 中直接给出最终答案。"

const sourceVerificationReminder = "你刚才只获得了发现阶段的候选和摘要，它们不是原始来源证据。请继续调用当前可用的来源读取能力、Shell、MCP 或其他合适工具读取至少一个权威原始来源；核对实体和关键数值后再回答。若原始来源确实无法访问，请明确说明已尝试的地址和限制，不要把摘要当成已核验事实。"

// prepareEmptyResponseRetry 只在当前 Run 已成功执行真实工具时强制收敛。
// Loader、失败结果和历史消息都不是本轮已经取得事实证据的证明。
func prepareEmptyResponseRetry(request Request, hasCurrentRealToolResult bool) Request {
	request.OnTextDelta = nil
	if !hasCurrentRealToolResult {
		return request
	}
	request.Tools = nil
	request.ToolChoice = ToolChoice{Mode: ToolChoiceNone}
	reminder := Message{Role: RoleSystem, Content: visibleAnswerReminder}
	request.Messages = append(append([]Message(nil), request.Messages...), reminder)
	request.NewMessages = append(append([]Message(nil), request.NewMessages...), reminder)
	return request
}

func nonLoaderTools(tools []ToolSpec) []ToolSpec {
	result := make([]ToolSpec, 0, len(tools))
	for _, tool := range tools {
		if !tool.Loader {
			result = append(result, tool)
		}
	}
	return result
}

func availableRequiredTools(tools []ToolSpec, names []string) []string {
	available := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if !tool.Loader {
			available[tool.Name] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(names))
	result := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := available[name]; !ok {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	return result
}

func filterToolsByName(tools []ToolSpec, names []string) []ToolSpec {
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	result := make([]ToolSpec, 0, len(names))
	for _, tool := range tools {
		if _, ok := allowed[tool.Name]; ok {
			result = append(result, tool)
		}
	}
	return result
}

func (runner *Runner) runTool(ctx context.Context, step int, call ToolCall, timeout time.Duration, lifecycle func(Event) error) (ToolResult, error, time.Duration, error, bool) {
	startedAt := time.Now()
	tool, ok := runner.toolsByName[call.Name]
	if !ok {
		err := fmt.Errorf("模型请求了未知工具 %q", call.Name)
		return toolResultWithError(ToolResult{}, err), err, time.Since(startedAt), nil, false
	}
	arguments := call.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if !json.Valid(arguments) {
		err := fmt.Errorf("工具 %q 的参数不是有效 JSON", call.Name)
		return toolResultWithError(ToolResult{}, err), err, time.Since(startedAt), nil, false
	}
	startEvent := Event{Kind: EventToolStart, Step: step, ToolCall: &call, StartedAt: startedAt}
	if lifecycle != nil {
		if err := lifecycle(startEvent); err != nil {
			return ToolResult{}, nil, time.Since(startedAt), fmt.Errorf("记录工具 %s 开始执行失败: %w", call.Name, err), false
		}
	}
	runner.emit(startEvent)
	toolContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var result ToolResult
	var err error
	if tool.Execute != nil {
		result, err = tool.Execute(toolContext, arguments)
	} else {
		var output string
		output, err = tool.Run(toolContext, arguments)
		result = NewToolResult(output)
	}
	if err != nil {
		result = toolResultWithError(result, err)
	}
	// 失败工具也可能同时返回部分结果或超长错误文本；成功和失败必须经过
	// 同一个摄入边界，不能让 error 分支绕过内存与 SQLite 的大小保护。
	if validationErr := ValidateToolResult(result); validationErr != nil {
		result = ToolResult{}
		err = &ToolError{Code: "tool_result_too_large", Message: validationErr.Error(), Hint: "让工具返回摘要、分页结果或 Artifact 引用", Retryable: false, Cause: err}
		result = toolResultWithError(result, err)
	}
	duration := time.Since(startedAt)
	output := result.ModelText()
	endEvent := Event{Kind: EventToolEnd, Step: step, ToolCall: &call, Result: &result, Output: output, Err: err, StartedAt: startedAt, Duration: duration}
	if lifecycle != nil {
		if lifecycleErr := lifecycle(endEvent); lifecycleErr != nil {
			return result, err, duration, fmt.Errorf("记录工具 %s 执行结果失败: %w", call.Name, lifecycleErr), true
		}
	}
	return result, err, duration, nil, true
}

func resetAttemptOutput(input RunRequest) {
	if input.OnTextReset != nil {
		input.OnTextReset()
	}
}

func toolResultWithError(result ToolResult, err error) ToolResult {
	if err == nil {
		return result
	}
	wasEmpty := result.Empty()
	result.IsError = true
	failure := &ToolResultError{Code: "tool_error", Message: err.Error()}
	var typed *ToolError
	if errors.As(err, &typed) {
		if strings.TrimSpace(typed.Code) != "" {
			failure.Code = typed.Code
		}
		failure.Message = typed.Error()
		failure.Hint = typed.Hint
		failure.Retryable = typed.Retryable
	}
	result.Error = failure
	if wasEmpty {
		body, _ := json.Marshal(map[string]any{"ok": false, "code": failure.Code, "error": failure.Message, "hint": failure.Hint, "retryable": failure.Retryable})
		result.StructuredContent = body
	}
	return result
}

// ValidateModelResponse rejects empty or non-terminal generations before any
// caller accepts partial text or executes a potentially truncated tool call.
func ValidateModelResponse(response *Response) error {
	reason := response.StopReason
	if reason == "" {
		if len(response.Message.ToolCalls) > 0 {
			reason = StopReasonToolUse
		} else {
			reason = StopReasonStop
		}
	}
	response.StopReason = reason
	if response.Exchange.StopReason == "" {
		response.Exchange.StopReason = reason
	}
	if response.Exchange.IncompleteReason == "" {
		response.Exchange.IncompleteReason = response.IncompleteReason
	}
	switch reason {
	case StopReasonStop, StopReasonToolUse:
		if strings.TrimSpace(response.Message.Content) == "" && len(response.Message.ToolCalls) == 0 {
			return ErrEmptyModelResponse
		}
		return nil
	default:
		return &IncompleteModelResponseError{StopReason: reason, Reason: response.IncompleteReason}
	}
}

func toolErrorOutput(err error) string {
	value := map[string]any{"ok": false, "code": "tool_error", "error": err.Error(), "retryable": false}
	var failure *ToolError
	if errors.As(err, &failure) {
		if strings.TrimSpace(failure.Code) != "" {
			value["code"] = failure.Code
		}
		value["retryable"] = failure.Retryable
		if strings.TrimSpace(failure.Hint) != "" {
			value["hint"] = failure.Hint
		}
	}
	body, _ := json.Marshal(value)
	return string(body)
}

type failedToolCall struct {
	attempts  int
	retryable bool
}

func toolCallKey(call ToolCall) string {
	arguments := call.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, arguments); err != nil {
		return call.Name + "\x00" + string(arguments)
	}
	return call.Name + "\x00" + compact.String()
}

func (runner *Runner) emit(event Event) {
	if runner.Observe != nil {
		runner.Observe(event)
	}
}

func addUsage(total *Usage, current Usage) {
	total.InputTokens += current.InputTokens
	total.OutputTokens += current.OutputTokens
	total.CachedInputTokens += current.CachedInputTokens
	total.CacheWriteTokens += current.CacheWriteTokens
	total.TotalTokens += current.TotalTokens
	total.CacheReported = total.CacheReported || current.CacheReported
	if current.TotalTokens == 0 {
		total.TotalTokens += current.InputTokens + current.OutputTokens
	}
}

func retryableModelError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var provider *ModelError
	if errors.As(err, &provider) {
		return provider.Retryable()
	}
	var network net.Error
	return errors.As(err, &network) && network.Timeout()
}

func retryWithoutStreaming(err error) bool {
	var failure *ModelError
	return errors.As(err, &failure) && failure.RetryWithoutStreaming
}

func modelRetryDelay(err error, attempt int) time.Duration {
	var provider *ModelError
	if errors.As(err, &provider) && provider.RetryAfter > 0 {
		if provider.RetryAfter > 30*time.Second {
			return 30 * time.Second
		}
		return provider.RetryAfter
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := 250 * time.Millisecond * time.Duration(1<<(attempt-1))
	if delay > 2*time.Second {
		return 2 * time.Second
	}
	return delay
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
