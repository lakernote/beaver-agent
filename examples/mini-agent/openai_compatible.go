package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxModelResponseBytes = 2 * 1024 * 1024

// OpenAICompatibleModel 是刻意精简的非流式 Chat Completions 适配器。
// 它只负责协议转换，不参与 Agent 循环。
type OpenAICompatibleModel struct {
	baseURL    string
	model      string
	apiKey     string
	httpClient *http.Client
}

func NewOpenAICompatibleModel(baseURL, model, apiKey string) (*OpenAICompatibleModel, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	model = strings.TrimSpace(model)
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("MINI_AGENT_BASE_URL 必须是完整的 http(s) 地址")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(baseURL, "#") {
		return nil, errors.New("MINI_AGENT_BASE_URL 不能包含用户信息、查询参数或片段")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return nil, errors.New("非本机模型服务必须使用 HTTPS")
	}
	if model == "" {
		return nil, errors.New("MINI_AGENT_MODEL 不能为空")
	}
	return &OpenAICompatibleModel{
		baseURL: baseURL,
		model:   model,
		apiKey:  strings.TrimSpace(apiKey),
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
			// Base URL 已经过传输安全校验。禁止自动重定向，避免远端用
			// 307/308 把 Prompt、工具结果或 Bearer Token 带到未校验地址。
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

type chatRequest struct {
	Model      string        `json:"model"`
	Messages   []chatMessage `json:"messages"`
	Tools      []chatTool    `json:"tools,omitempty"`
	ToolChoice string        `json:"tool_choice,omitempty"`
	Stream     bool          `json:"stream"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type chatToolCall struct {
	ID       string               `json:"id,omitempty"`
	Type     string               `json:"type"`
	Function chatToolCallFunction `json:"function"`
}

type chatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

func (model *OpenAICompatibleModel) Generate(ctx context.Context, request ModelRequest) (Message, error) {
	payload := chatRequest{Model: model.model, Stream: false}
	for _, message := range request.Messages {
		payload.Messages = append(payload.Messages, encodeChatMessage(message))
	}
	for _, spec := range request.Tools {
		payload.Tools = append(payload.Tools, chatTool{
			Type: "function",
			Function: chatToolFunction{
				Name:        spec.Name,
				Description: spec.Description,
				Parameters:  spec.Parameters,
			},
		})
	}
	if len(payload.Tools) > 0 {
		payload.ToolChoice = "auto"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Message{}, err
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, model.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if model.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+model.apiKey)
	}
	httpResponse, err := model.httpClient.Do(httpRequest)
	if err != nil {
		return Message{}, err
	}
	defer httpResponse.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxModelResponseBytes+1))
	if err != nil {
		return Message{}, err
	}
	if len(responseBody) > maxModelResponseBytes {
		return Message{}, errors.New("模型响应超过 2 MiB")
	}
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return Message{}, fmt.Errorf("模型返回 HTTP %d: %s", httpResponse.StatusCode, strings.TrimSpace(string(responseBody)))
	}

	var decoded chatResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return Message{}, fmt.Errorf("解析模型响应: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return Message{}, errors.New("模型响应没有 choices")
	}
	choice := decoded.Choices[0].Message
	finishReason := decoded.Choices[0].FinishReason
	switch finishReason {
	case "length":
		return Message{}, errors.New("模型输出因长度限制被截断")
	case "content_filter":
		return Message{}, errors.New("模型输出被内容过滤器拦截")
	}
	result := Message{Role: RoleAssistant, Content: choice.Content}
	for _, call := range choice.ToolCalls {
		arguments, err := decodeToolArguments(call.Function.Arguments)
		if err != nil {
			return Message{}, fmt.Errorf("工具 %q 的参数: %w", call.Function.Name, err)
		}
		result.ToolCalls = append(result.ToolCalls, ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: arguments,
		})
	}
	if strings.TrimSpace(result.Content) == "" && len(result.ToolCalls) == 0 {
		return Message{}, errors.New("模型既没有回答，也没有工具调用")
	}
	return result, nil
}

func encodeChatMessage(message Message) chatMessage {
	encoded := chatMessage{
		Role:       string(message.Role),
		Content:    message.Content,
		ToolCallID: message.ToolCallID,
	}
	for _, call := range message.ToolCalls {
		arguments := call.Arguments
		if len(arguments) == 0 {
			arguments = json.RawMessage(`{}`)
		}
		encoded.ToolCalls = append(encoded.ToolCalls, chatToolCall{
			ID:   call.ID,
			Type: "function",
			Function: chatToolCallFunction{
				Name:      call.Name,
				Arguments: string(arguments),
			},
		})
	}
	return encoded
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func decodeToolArguments(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`), nil
	}
	var encoded string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return nil, err
		}
		raw = json.RawMessage(encoded)
	}
	if !json.Valid(raw) {
		return nil, errors.New("不是合法 JSON")
	}
	return append(json.RawMessage(nil), raw...), nil
}
