package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lakernote/beaver-agent/internal/agent"
)

func TestChatGPTSubscriptionRunsLocalToolWithStatelessResponses(t *testing.T) {
	var requests, toolRuns atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.URL.Path != "/v1/responses" || request.Header.Get("Accept") != "text/event-stream" ||
			body["stream"] != true || body["store"] != false || body["instructions"] != "Beaver runs the tools." {
			t.Errorf("invalid subscription Responses request: %#v", body)
		}
		for _, field := range []string{"previous_response_id", "temperature", "max_output_tokens"} {
			if _, exists := body[field]; exists {
				t.Errorf("unsupported field %s on subscription route", field)
			}
		}
		tools, _ := json.Marshal(body["tools"])
		if !strings.Contains(string(tools), `"type":"namespace"`) ||
			!strings.Contains(string(tools), `"name":"beaver"`) ||
			!strings.Contains(string(tools), `"name":"local_echo"`) {
			t.Errorf("missing namespaced tool: %s", tools)
		}
		response.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			fmt.Fprint(response, "data: "+`{"type":"response.completed","response":{"id":"first","status":"completed","output":[{"type":"reasoning","id":"rs_fixture","summary":[],"encrypted_content":"opaque-example"},{"type":"function_call","namespace":"beaver","name":"local_echo","call_id":"call_fixture","arguments":"{}"}]}}`+"\n\n")
			return
		}
		input, _ := json.Marshal(body["input"])
		for _, value := range []string{"run the local tool", "function_call", "function_call_output", "call_fixture", "beaver", "opaque-example", "tool result from Beaver"} {
			if !strings.Contains(string(input), value) {
				t.Errorf("second request did not include %q: %s", value, input)
			}
		}
		if toolRuns.Load() != 1 {
			t.Error("model received a tool output before the Beaver Runner executed the tool")
		}
		fmt.Fprint(response, "data: "+`{"type":"response.output_text.delta","delta":"Finished"}`+"\n\n")
		fmt.Fprint(response, "data: "+`{"type":"response.completed","response":{"id":"second","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Finished"}]}],"usage":{"input_tokens":10,"output_tokens":1}}}`+"\n\n")
	}))
	defer provider.Close()

	model, err := New(Config{BaseURL: provider.URL + "/v1", Protocol: Responses, ChatGPTSubscription: true})
	if err != nil {
		t.Fatal(err)
	}
	if model.Capabilities().ServerContinuation {
		t.Fatal("store:false must disable server continuation")
	}
	runner, err := agent.NewRunner(model, "fixture-model", []agent.Tool{{
		Spec: agent.ToolSpec{Name: "local_echo", Parameters: map[string]any{"type": "object"}},
		Run: func(_ context.Context, _ json.RawMessage) (string, error) {
			toolRuns.Add(1)
			return "tool result from Beaver", nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), agent.RunRequest{
		Messages: []agent.Message{{Role: agent.RoleSystem, Content: "Beaver runs the tools."}, {Role: agent.RoleUser, Content: "run the local tool"}},
		MaxSteps: 3,
	})
	if err != nil || result.Answer != "Finished" || requests.Load() != 2 || toolRuns.Load() != 1 || result.ResponseID != "" {
		t.Fatalf("unexpected result: %+v, %v, requests=%d, tool runs=%d", result, err, requests.Load(), toolRuns.Load())
	}
}

func TestChatGPTSubscriptionDoesNotRetryPlanLimitFromStream(t *testing.T) {
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		response.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(response, "\n"+"data: "+`{"type":"response.failed","response":{"status":"failed","error":{"code":"subscription_sharing_usage_limit_exceeded","message":"App limit"}}}`+"\n\n")
	}))
	defer provider.Close()
	model, err := New(Config{BaseURL: provider.URL, Protocol: Responses, ChatGPTSubscription: true})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(model, "fixture-model", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), agent.RunRequest{Messages: []agent.Message{{Role: agent.RoleUser, Content: "hello"}}, MaxSteps: 2})
	if err == nil || !strings.Contains(err.Error(), "用量已达上限") || requests.Load() != 1 {
		t.Fatalf("expected one request ending at the plan limit: %v, requests=%d", err, requests.Load())
	}
}
