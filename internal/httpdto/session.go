// Package httpdto defines the stable JSON contract exposed by Beaver's
// HTTP API. Persistence structs are converted here instead of being serialized
// directly, so database changes do not silently alter the public API.
package httpdto

import (
	"encoding/json"
	"time"

	"github.com/lakernote/beaver-agent/internal/permissions"
	"github.com/lakernote/beaver-agent/internal/store"
)

type Session struct {
	ID                 string             `json:"id"`
	Title              string             `json:"title"`
	ProjectID          string             `json:"projectId,omitempty"`
	Status             string             `json:"status"`
	Error              string             `json:"error,omitempty"`
	Runtime            string             `json:"runtime"`
	Channel            string             `json:"channel,omitempty"`
	ProfileID          string             `json:"profileId,omitempty"`
	Model              string             `json:"model,omitempty"`
	ReasoningEffort    string             `json:"reasoningEffort,omitempty"`
	Permissions        permissions.Policy `json:"permissions"`
	Workspace          string             `json:"workspace"`
	SourceWorkspace    string             `json:"sourceWorkspace,omitempty"`
	WorktreeBranch     string             `json:"worktreeBranch,omitempty"`
	WorkspaceIsolation string             `json:"workspaceIsolation,omitempty"`
	WorkspaceNotice    string             `json:"workspaceNotice,omitempty"`
	CreatedAt          time.Time          `json:"createdAt"`
	UpdatedAt          time.Time          `json:"updatedAt"`
	Messages           []Message          `json:"messages"`
	Events             []Event            `json:"events"`
	MessageCount       int                `json:"messageCount,omitempty"`
	EventCount         int                `json:"eventCount,omitempty"`
	UserTurnCount      int                `json:"userTurnCount,omitempty"`
	MessagesTruncated  bool               `json:"messagesTruncated,omitempty"`
	EventsTruncated    bool               `json:"eventsTruncated,omitempty"`
	MessagesHasMore    bool               `json:"messagesHasMore,omitempty"`
	EventsHasMore      bool               `json:"eventsHasMore,omitempty"`
	Usage              Usage              `json:"usage"`
	Context            Context            `json:"context"`
	PartialOutput      string             `json:"partialOutput,omitempty"`
	RunProgress        string             `json:"runProgress,omitempty"`
	CodexRequest       *CodexRequest      `json:"codexRequest,omitempty"`
}

type Attachment struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MIMEType string `json:"mimeType"`
	Kind     string `json:"kind"`
	Size     int64  `json:"size"`
}

type ToolCall struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Arguments      string `json:"arguments"`
	ActivityKind   string `json:"activityKind,omitempty"`
	ActivitySource string `json:"activitySource,omitempty"`
	DisplayName    string `json:"displayName,omitempty"`
}

type Message struct {
	ID          int64           `json:"id"`
	Role        string          `json:"role"`
	Content     string          `json:"content,omitempty"`
	ToolResult  json.RawMessage `json:"toolResult,omitempty"`
	Attachments []Attachment    `json:"attachments"`
	ToolCalls   []ToolCall      `json:"toolCalls"`
	ToolCallID  string          `json:"toolCallId,omitempty"`
	Name        string          `json:"name,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
}

type Usage struct {
	InputTokens         int   `json:"inputTokens"`
	OutputTokens        int   `json:"outputTokens"`
	CachedTokens        int   `json:"cachedTokens"`
	CacheWriteTokens    int   `json:"cacheWriteTokens"`
	TotalTokens         int   `json:"totalTokens"`
	ModelDurationMS     int64 `json:"modelDurationMs"`
	ToolDurationMS      int64 `json:"toolDurationMs"`
	ModelCalls          int   `json:"modelCalls"`
	ToolCalls           int   `json:"toolCalls"`
	CacheReported       bool  `json:"cacheReported"`
	CacheInputTokens    int   `json:"cacheInputTokens"`
	ContextWindowTokens int   `json:"contextWindowTokens,omitempty"`
}

type Event struct {
	ID                  int64     `json:"id"`
	Kind                string    `json:"kind"`
	ProtocolMethod      string    `json:"protocolMethod,omitempty"`
	RawPayload          string    `json:"rawPayload,omitempty"`
	Turn                int       `json:"turn,omitempty"`
	Step                int       `json:"step"`
	Attempt             int       `json:"attempt,omitempty"`
	Name                string    `json:"name,omitempty"`
	ActivityID          string    `json:"activityId,omitempty"`
	ActivityKind        string    `json:"activityKind,omitempty"`
	ActivitySource      string    `json:"activitySource,omitempty"`
	DisplayName         string    `json:"displayName,omitempty"`
	Status              string    `json:"status"`
	StopReason          string    `json:"stopReason,omitempty"`
	IncompleteReason    string    `json:"incompleteReason,omitempty"`
	Detail              string    `json:"detail,omitempty"`
	Input               string    `json:"input,omitempty"`
	Output              string    `json:"output,omitempty"`
	InputTokens         int       `json:"inputTokens,omitempty"`
	OutputTokens        int       `json:"outputTokens,omitempty"`
	CachedTokens        int       `json:"cachedTokens,omitempty"`
	CacheWriteTokens    int       `json:"cacheWriteTokens,omitempty"`
	CacheReported       bool      `json:"cacheReported"`
	TotalTokens         int       `json:"totalTokens,omitempty"`
	ContextWindowTokens int       `json:"contextWindowTokens,omitempty"`
	Protocol            string    `json:"protocol,omitempty"`
	StatusCode          int       `json:"statusCode,omitempty"`
	HistoryMode         string    `json:"historyMode,omitempty"`
	RequestMessages     int       `json:"requestMessages,omitempty"`
	ToolDefinitions     int       `json:"toolDefinitions,omitempty"`
	DurationMS          int64     `json:"durationMs,omitempty"`
	CreatedAt           time.Time `json:"createdAt"`
}

type Context struct {
	HistoryMessages             int    `json:"historyMessages"`
	UserTurns                   int    `json:"userTurns"`
	LastInputTokens             int    `json:"lastInputTokens"`
	ContextWindowTokens         int    `json:"contextWindowTokens"`
	HistoryMode                 string `json:"historyMode"`
	RequestMessages             int    `json:"requestMessages"`
	ToolDefinitions             int    `json:"toolDefinitions"`
	CompressionMode             string `json:"compressionMode"`
	CompressionThresholdPercent int    `json:"compressionThresholdPercent"`
	CompressionCount            int    `json:"compressionCount"`
	CompressedMessages          int    `json:"compressedMessages"`
	RetainedMessages            int    `json:"retainedMessages"`
	CacheReported               bool   `json:"cacheReported"`
	LastCachedTokens            int    `json:"lastCachedTokens"`
	LastCacheWriteTokens        int    `json:"lastCacheWriteTokens"`
}

type CodexRequest struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func SessionFromStore(value store.Session, workspaceIsolation string) Session {
	policy := value.Permissions.Normalize()
	if policy.Mode == "" {
		policy = permissions.Default()
	}
	return Session{
		ID: value.ID, Title: value.Title, ProjectID: value.ProjectID, Status: string(value.Status), Error: value.Error,
		Runtime: string(value.Runtime), Channel: string(value.Channel), ProfileID: value.ProfileID, Model: value.Model, ReasoningEffort: value.ReasoningEffort,
		Permissions: policy, Workspace: value.Workspace, SourceWorkspace: value.SourceWorkspace, WorktreeBranch: value.WorktreeBranch,
		WorkspaceIsolation: workspaceIsolation, WorkspaceNotice: value.WorkspaceNotice, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
		Messages: MessagesFromStore(value.Messages), Events: EventsFromStore(value.Events), MessageCount: value.MessageCount, EventCount: value.EventCount,
		UserTurnCount: value.UserTurnCount, MessagesTruncated: value.MessagesTruncated, EventsTruncated: value.EventsTruncated,
		MessagesHasMore: value.MessagesHasMore, EventsHasMore: value.EventsHasMore, Usage: UsageFromStore(value.Usage),
		Context: ContextFromStore(value.Context), PartialOutput: value.PartialOutput, RunProgress: value.RunProgress,
	}
}

func MessagesFromStore(values []store.Message) []Message {
	result := make([]Message, 0, len(values))
	for _, value := range values {
		attachments := make([]Attachment, 0, len(value.Attachments))
		for _, attachment := range value.Attachments {
			attachments = append(attachments, Attachment{ID: attachment.ID, Name: attachment.Name, MIMEType: attachment.MIMEType, Kind: attachment.Kind, Size: attachment.Size})
		}
		toolCalls := make([]ToolCall, 0, len(value.ToolCalls))
		for _, call := range value.ToolCalls {
			toolCalls = append(toolCalls, ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments, ActivityKind: call.ActivityKind, ActivitySource: call.ActivitySource, DisplayName: call.DisplayName})
		}
		result = append(result, Message{ID: value.ID, Role: value.Role, Content: value.Content, ToolResult: value.ToolResult, Attachments: attachments, ToolCalls: toolCalls, ToolCallID: value.ToolCallID, Name: value.Name, CreatedAt: value.CreatedAt})
	}
	return result
}

func EventsFromStore(values []store.Event) []Event {
	result := make([]Event, 0, len(values))
	for _, value := range values {
		result = append(result, Event{
			ID: value.ID, Kind: value.Kind, ProtocolMethod: value.ProtocolMethod, RawPayload: value.RawPayload, Turn: value.Turn, Step: value.Step, Attempt: value.Attempt,
			Name: value.Name, ActivityID: value.ActivityID, ActivityKind: value.ActivityKind, ActivitySource: value.ActivitySource, DisplayName: value.DisplayName,
			Status: value.Status, StopReason: value.StopReason, IncompleteReason: value.IncompleteReason, Detail: value.Detail, Input: value.Input, Output: value.Output,
			InputTokens: value.InputTokens, OutputTokens: value.OutputTokens, CachedTokens: value.CachedTokens, CacheWriteTokens: value.CacheWriteTokens,
			CacheReported: value.CacheReported, TotalTokens: value.TotalTokens, ContextWindowTokens: value.ContextWindowTokens, Protocol: value.Protocol,
			StatusCode: value.StatusCode, HistoryMode: value.HistoryMode, RequestMessages: value.RequestMessages, ToolDefinitions: value.ToolDefinitions,
			DurationMS: value.DurationMS, CreatedAt: value.CreatedAt,
		})
	}
	return result
}

func UsageFromStore(value store.Usage) Usage {
	return Usage{InputTokens: value.InputTokens, OutputTokens: value.OutputTokens, CachedTokens: value.CachedTokens, CacheWriteTokens: value.CacheWriteTokens, TotalTokens: value.TotalTokens, ModelDurationMS: value.ModelDurationMS, ToolDurationMS: value.ToolDurationMS, ModelCalls: value.ModelCalls, ToolCalls: value.ToolCalls, CacheReported: value.CacheReported, CacheInputTokens: value.CacheInputTokens, ContextWindowTokens: value.ContextWindowTokens}
}

func ContextFromStore(value store.ContextInfo) Context {
	return Context{HistoryMessages: value.HistoryMessages, UserTurns: value.UserTurns, LastInputTokens: value.LastInputTokens, ContextWindowTokens: value.ContextWindowTokens, HistoryMode: value.HistoryMode, RequestMessages: value.RequestMessages, ToolDefinitions: value.ToolDefinitions, CompressionMode: value.CompressionMode, CompressionThresholdPercent: value.CompressionThresholdPercent, CompressionCount: value.CompressionCount, CompressedMessages: value.CompressedMessages, RetainedMessages: value.RetainedMessages, CacheReported: value.CacheReported, LastCachedTokens: value.LastCachedTokens, LastCacheWriteTokens: value.LastCacheWriteTokens}
}
