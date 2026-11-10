package server

import (
	"fmt"
	"net/http"
	"time"

	"github.com/lakernote/beaver-agent/internal/agent"
	"github.com/lakernote/beaver-agent/internal/agent/modeladapter"
	"github.com/lakernote/beaver-agent/internal/agent/qualification"
	"github.com/lakernote/beaver-agent/internal/store"
)

// newModelAdapter is the only server-level construction point for Beaver
// models. Runtime orchestration depends on agent.Model and model capabilities,
// never on a provider client concrete type.
func (server *Server) newModelAdapter(settings store.ModelSettings) (agent.Model, error) {
	registry := server.modelAdapterRegistry()
	model, err := registry.New(modeladapter.Config{
		Provider: settings.Provider, Protocol: settings.Protocol, BaseURL: settings.BaseURL,
		APIKey: settings.APIKey, Timeout: time.Duration(settings.RequestTimeoutSeconds) * time.Second,
		DisableThinking: settings.Thinking == "disabled", ContextWindowTokens: settings.ContextWindowTokens,
	})
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

func (server *Server) modelAdapterRegistry() *modeladapter.Registry {
	if server.modelAdapters != nil {
		return server.modelAdapters
	}
	// Some focused unit tests construct Server directly. Production instances
	// always receive their registry from newServer.
	return modeladapter.NewRegistry()
}

// runModelTest is kept as a small package-level helper for isolated tests and
// non-HTTP callers. Production Server paths use the registry owned by Server.
func runModelTest(request *http.Request, settings store.ModelSettings) (modelTestResult, error) {
	return runModelTestWithRegistry(request, settings, modeladapter.NewRegistry())
}

func runModelTestWithRegistry(request *http.Request, settings store.ModelSettings, registry *modeladapter.Registry) (modelTestResult, error) {
	client, err := registry.New(modeladapter.Config{
		Provider: settings.Provider, Protocol: settings.Protocol, BaseURL: settings.BaseURL,
		APIKey: settings.APIKey, Timeout: time.Duration(settings.RequestTimeoutSeconds) * time.Second,
		DisableThinking: settings.Thinking == "disabled", ContextWindowTokens: settings.ContextWindowTokens,
	})
	if err != nil {
		return modelTestResult{}, err
	}
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
