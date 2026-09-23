package modeladapter

import (
	"testing"

	"github.com/lakernote/easy-agent/internal/agent"
)

func TestBuiltInRegistryExposesNativeAndCompatibleProtocols(t *testing.T) {
	registry := NewRegistry()
	for _, protocol := range []string{ProtocolChatCompletions, ProtocolResponses, ProtocolOllamaChat, ProtocolAnthropic} {
		if !registry.Supports(protocol) {
			t.Fatalf("protocol %q not registered", protocol)
		}
	}
	model, err := registry.New(Config{Provider: "ollama", Protocol: ProtocolOllamaChat, BaseURL: "http://127.0.0.1:11434"})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := agent.CapabilitiesOf(model)
	if capabilities.Protocol != ProtocolOllamaChat || !capabilities.Streaming || !capabilities.NativeTools || capabilities.ServerContinuation {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	for _, protocol := range []string{ProtocolChatCompletions, ProtocolResponses, ProtocolAnthropic} {
		candidate, err := registry.New(Config{Provider: "fixture", Protocol: protocol, BaseURL: "http://127.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		capabilities := agent.CapabilitiesOf(candidate)
		if !capabilities.Streaming || !capabilities.NativeTools {
			t.Fatalf("protocol %q must advertise native streaming tools: %+v", protocol, capabilities)
		}
	}
	legacyResponses, err := registry.New(Config{Provider: "ollama", Protocol: ProtocolResponses, BaseURL: "http://127.0.0.1:11434/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if agent.CapabilitiesOf(legacyResponses).ServerContinuation {
		t.Fatal("Ollama Responses compatibility must not claim previous_response_id support")
	}
}

func TestRegistryIsInstanceScoped(t *testing.T) {
	first := NewRegistry()
	second := NewRegistry()
	const customProtocol = "test_custom"
	if err := first.Register(customProtocol, func(Config) (agent.Model, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if !first.Supports(customProtocol) {
		t.Fatal("registered protocol is not visible in its registry")
	}
	if second.Supports(customProtocol) {
		t.Fatal("custom protocol leaked into another registry")
	}
}

func TestRegistryZeroValueCanRegisterFactory(t *testing.T) {
	var registry Registry
	if err := registry.Register("custom", func(Config) (agent.Model, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if !registry.Supports("custom") {
		t.Fatal("zero-value registry did not retain registered factory")
	}
}
