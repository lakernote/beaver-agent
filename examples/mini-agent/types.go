package main

import (
	"context"
	"encoding/json"
)

// Role 是一条消息在对话协议中的身份。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall 是模型提出的一次工具调用。Arguments 必须是 JSON 对象。
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Message 是模型与 Agent 共享的最小消息格式。
// ToolCallID 把 tool 结果与对应的 assistant 工具调用配对。
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	Name       string
}

// ToolSpec 只描述“工具是什么”，它会作为 JSON Schema 发给模型。
type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Tool 把给模型看的说明和只在本地执行的函数绑定在一起。
type Tool struct {
	Spec ToolSpec
	Run  func(context.Context, json.RawMessage) (string, error)
}

// ModelRequest 是一次模型调用所需的全部上下文。
type ModelRequest struct {
	Messages []Message
	Tools    []ToolSpec
}

// Model 是核心循环和具体模型服务之间的边界。
type Model interface {
	Generate(context.Context, ModelRequest) (Message, error)
}
