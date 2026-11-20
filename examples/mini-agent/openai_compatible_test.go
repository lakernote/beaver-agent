package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAICompatibleModelEncodesToolsAndDecodesToolCall(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		var payload chatRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		if payload.Model != "test-model" || payload.Stream || payload.ToolChoice != "auto" {
			t.Errorf("unexpected payload: %#v", payload)
		}
		if len(payload.Tools) != 1 || payload.Tools[0].Function.Name != "text_stats" {
			t.Errorf("tools = %#v", payload.Tools)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{
              "id":"call_http","type":"function","function":{"name":"text_stats","arguments":"{\"text\":\"Agent\"}"}
            }]}}]
        }`))
	}))
	defer server.Close()

	model, err := NewOpenAICompatibleModel(server.URL+"/v1", "test-model", "test-key")
	if err != nil {
		t.Fatal(err)
	}
	message, err := model.Generate(context.Background(), ModelRequest{
		Messages: []Message{{Role: RoleUser, Content: "统计 Agent"}},
		Tools:    []ToolSpec{textStatsTool().Spec},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", message.ToolCalls)
	}
	call := message.ToolCalls[0]
	if call.ID != "call_http" || call.Name != "text_stats" || string(call.Arguments) != `{"text":"Agent"}` {
		t.Fatalf("tool call = %#v", call)
	}
}

func TestOpenAICompatibleModelEncodesToolResultPairing(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload chatRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		last := payload.Messages[len(payload.Messages)-1]
		if last.Role != "tool" || last.ToolCallID != "call_7" || last.Content != `{"ok":true}` {
			t.Errorf("tool message = %#v", last)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"完成"}}]}`))
	}))
	defer server.Close()
	model, err := NewOpenAICompatibleModel(server.URL, "test-model", "")
	if err != nil {
		t.Fatal(err)
	}
	message, err := model.Generate(context.Background(), ModelRequest{Messages: []Message{{
		Role: RoleTool, Content: `{"ok":true}`, ToolCallID: "call_7", Name: "text_stats",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if message.Content != "完成" {
		t.Fatalf("content = %q", message.Content)
	}
}

func TestOpenAICompatibleModelKeepsEmptyToolContent(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		last := payload.Messages[len(payload.Messages)-1]
		if string(last["role"]) != `"tool"` || string(last["content"]) != `""` || string(last["tool_call_id"]) != `"call_empty"` {
			t.Errorf("tool message = %#v", last)
		}
		if _, exists := last["name"]; exists {
			t.Errorf("tool message contains unsupported name: %#v", last)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"完成"}}]}`))
	}))
	defer server.Close()

	model, err := NewOpenAICompatibleModel(server.URL, "test-model", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Generate(context.Background(), ModelRequest{Messages: []Message{{
		Role: RoleTool, ToolCallID: "call_empty", Name: "text_stats",
	}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOpenAICompatibleModelRejectsTruncatedResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"finish_reason":"length","message":{"content":"半截答案"}}]}`))
	}))
	defer server.Close()

	model, err := NewOpenAICompatibleModel(server.URL, "test-model", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Generate(context.Background(), ModelRequest{Messages: []Message{{Role: RoleUser, Content: "回答"}}})
	if err == nil || !strings.Contains(err.Error(), "截断") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAICompatibleModelRejectsUnsafeBaseURL(t *testing.T) {
	t.Parallel()
	for _, baseURL := range []string{
		"http://example.com/v1",
		"https://example.com/v1?api-version=1",
		"https://example.com/v1?",
		"https://example.com/v1#fragment",
		"https://example.com/v1#",
	} {
		if _, err := NewOpenAICompatibleModel(baseURL, "test-model", "secret"); err == nil {
			t.Errorf("NewOpenAICompatibleModel(%q) succeeded", baseURL)
		}
	}
}

func TestOpenAICompatibleModelRejectsRedirects(t *testing.T) {
	t.Parallel()
	targetHit := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		targetHit <- struct{}{}
		writer.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	model, err := NewOpenAICompatibleModel(redirect.URL, "test-model", "secret")
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Generate(context.Background(), ModelRequest{Messages: []Message{{Role: RoleUser, Content: "回答"}}})
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-targetHit:
		t.Fatal("redirect target received request")
	default:
	}
}
