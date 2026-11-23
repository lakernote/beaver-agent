package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/lakernote/beaver-agent/internal/agent"
	codexmodel "github.com/lakernote/beaver-agent/internal/agent/codex"
	"github.com/lakernote/beaver-agent/internal/agent/modeladapter"
	"github.com/lakernote/beaver-agent/internal/agent/qualification"
	"github.com/lakernote/beaver-agent/internal/chatgpt"
	"github.com/lakernote/beaver-agent/internal/codexruntime"
	"github.com/lakernote/beaver-agent/internal/store"
)

// newModelAdapter is the only server-level construction point for Beaver
// models. Runtime orchestration depends on agent.Model and model capabilities,
// never on a provider client concrete type.
func (server *Server) newModelAdapter(settings store.ModelSettings) (agent.Model, error) {
	if settings.IsChatGPTCodex() {
		if err := server.requireChatGPTAccount(settings); err != nil {
			return nil, err
		}
		return codexmodel.New(func(ctx context.Context, config codexruntime.Config, prompt string) (codexruntime.Result, error) {
			config.Timeout = time.Duration(settings.TurnTimeoutSeconds) * time.Second
			return server.chatGPTCodex.RunTurn(ctx, settings.ChatGPTAccountID, config, prompt)
		}, time.Duration(settings.RequestTimeoutSeconds)*time.Second), nil
	}
	registry := server.modelAdapterRegistry()
	config := modeladapter.Config{
		Provider: settings.Provider, Protocol: settings.Protocol, BaseURL: settings.BaseURL,
		APIKey: settings.APIKey, Timeout: time.Duration(settings.RequestTimeoutSeconds) * time.Second,
		DisableThinking: settings.Thinking == "disabled", ContextWindowTokens: settings.ContextWindowTokens,
	}
	if settings.IsChatGPTSubscription() {
		if err := server.requireChatGPTAccount(settings); err != nil {
			return nil, err
		}
		config.Protocol = modeladapter.ProtocolResponses
		config.BaseURL = chatgpt.APIBaseURL
		config.APIKey = ""
		config.DisableThinking = false
		config.ChatGPTSubscription = true
		config.HTTPClient = server.chatGPT.InferenceClient(settings.ChatGPTAccountID, config.Timeout)
	}
	model, err := registry.New(config)
	if err != nil {
		return nil, err
	}
	capabilities := agent.CapabilitiesOf(model)
	if !capabilities.NativeTools {
		return nil, fmt.Errorf("模型协议 %s 不支持原生 Function Calling，Beaver 不会解析文本伪工具调用", settings.Protocol)
	}
	if !capabilities.Streaming {
		return nil, fmt.Errorf("模型协议 %s 没有实现流式 API，不能用于 Beaver", settings.Protocol)
	}
	return model, nil
}

func (server *Server) probeModel(request *http.Request, settings store.ModelSettings) (modelTestResult, error) {
	client, err := server.newModelAdapter(settings)
	if err != nil {
		return modelTestResult{}, err
	}
	defer closeModel(client)
	return qualifyModel(request, settings, client)
}

func closeModel(model agent.Model) {
	if closer, ok := model.(io.Closer); ok {
		_ = closer.Close()
	}
}

func (server *Server) modelAdapterRegistry() *modeladapter.Registry {
	if server.modelAdapters != nil {
		return server.modelAdapters
	}
	// Some focused unit tests construct Server directly. Production instances
	// always receive their registry from newServer.
	return modeladapter.NewRegistry()
}

func qualifyModel(request *http.Request, settings store.ModelSettings, client agent.Model) (modelTestResult, error) {
	result, err := qualification.Run(request.Context(), client, settings.Model)
	value := modelTestResult{
		OK: err == nil, Model: result.Model, ToolCall: result.ToolCall, Answer: result.Answer,
		InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, DurationMS: result.Duration.Milliseconds(),
	}
	if err != nil {
		return value, err
	}
	return value, nil
}
