// Package modeladapter is the transport boundary for EasyAgent models. The
// server selects a protocol by name; the core runner never imports a provider.
package modeladapter

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lakernote/easy-agent/internal/agent"
	"github.com/lakernote/easy-agent/internal/agent/anthropic"
	"github.com/lakernote/easy-agent/internal/agent/ollama"
	"github.com/lakernote/easy-agent/internal/agent/openai"
)

const (
	ProtocolChatCompletions = "chat_completions"
	ProtocolResponses       = "responses"
	ProtocolOllamaChat      = ollama.Protocol
	ProtocolAnthropic       = anthropic.Protocol
)

type Config struct {
	Provider            string
	Protocol            string
	BaseURL             string
	APIKey              string
	HTTPClient          *http.Client
	Timeout             time.Duration
	DisableThinking     bool
	ContextWindowTokens int
}

type Factory func(Config) (agent.Model, error)

// Registry owns the model protocol factories used by one application instance.
// It is intentionally explicit instead of package-global: tests and future
// embedders can build an isolated registry without sharing mutable process
// state with other instances.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

// NewRegistry returns a registry containing EasyAgent's built-in adapters.
func NewRegistry() *Registry {
	registry := &Registry{factories: make(map[string]Factory)}
	registry.factories[ProtocolChatCompletions] = func(config Config) (agent.Model, error) {
		isOllama := strings.EqualFold(strings.TrimSpace(config.Provider), "ollama")
		return openai.New(openai.Config{
			BaseURL: config.BaseURL, APIKey: config.APIKey, Protocol: openai.ChatCompletions,
			HTTPClient: config.HTTPClient, Timeout: config.Timeout, DisableThinking: config.DisableThinking,
			KeepThinkingForTools: isOllama, DisableContinuation: isOllama,
		})
	}
	registry.factories[ProtocolResponses] = func(config Config) (agent.Model, error) {
		isOllama := strings.EqualFold(strings.TrimSpace(config.Provider), "ollama")
		return openai.New(openai.Config{
			BaseURL: config.BaseURL, APIKey: config.APIKey, Protocol: openai.Responses,
			HTTPClient: config.HTTPClient, Timeout: config.Timeout, DisableThinking: config.DisableThinking,
			KeepThinkingForTools: isOllama, DisableContinuation: isOllama,
		})
	}
	registry.factories[ProtocolOllamaChat] = func(config Config) (agent.Model, error) {
		return ollama.New(ollama.Config{
			BaseURL: config.BaseURL, APIKey: config.APIKey, HTTPClient: config.HTTPClient, Timeout: config.Timeout,
			DisableThinking: config.DisableThinking, ContextWindow: config.ContextWindowTokens,
		})
	}
	registry.factories[ProtocolAnthropic] = func(config Config) (agent.Model, error) {
		return anthropic.New(anthropic.Config{BaseURL: config.BaseURL, APIKey: config.APIKey, HTTPClient: config.HTTPClient, Timeout: config.Timeout})
	}
	return registry
}

func (registry *Registry) Register(protocol string, factory Factory) error {
	if registry == nil {
		return fmt.Errorf("模型 Adapter Registry 不能为空")
	}
	protocol = strings.TrimSpace(protocol)
	if protocol == "" || factory == nil {
		return fmt.Errorf("模型 Adapter 协议名和 Factory 不能为空")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.factories == nil {
		registry.factories = make(map[string]Factory)
	}
	if _, exists := registry.factories[protocol]; exists {
		return fmt.Errorf("模型 Adapter %q 已注册", protocol)
	}
	registry.factories[protocol] = factory
	return nil
}

func (registry *Registry) New(config Config) (agent.Model, error) {
	if registry == nil {
		return nil, fmt.Errorf("模型 Adapter Registry 不能为空")
	}
	protocol := strings.TrimSpace(config.Protocol)
	if protocol == "" {
		protocol = ProtocolChatCompletions
	}
	registry.mu.RLock()
	factory := registry.factories[protocol]
	registry.mu.RUnlock()
	if factory == nil {
		return nil, fmt.Errorf("不支持的模型协议 %q（可用：%s）", protocol, strings.Join(registry.Protocols(), "、"))
	}
	return factory(config)
}

func (registry *Registry) Supports(protocol string) bool {
	if registry == nil {
		return false
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	_, ok := registry.factories[strings.TrimSpace(protocol)]
	return ok
}

func (registry *Registry) Protocols() []string {
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := make([]string, 0, len(registry.factories))
	for protocol := range registry.factories {
		result = append(result, protocol)
	}
	sort.Strings(result)
	return result
}
