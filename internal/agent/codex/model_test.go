package codex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lakernote/beaver-agent/internal/agent"
	"github.com/lakernote/beaver-agent/internal/codexruntime"
)

func TestRunnerCanCallToolLoadedDuringCodexTurn(t *testing.T) {
	run := func(_ context.Context, config codexruntime.Config, _ string) (codexruntime.Result, error) {
		load, err := config.OnServerRequest(codexruntime.ServerRequest{Method: "item/tool/call", Params: json.RawMessage(
			`{"tool":"beaver_call_tool","callId":"load","arguments":{"name":"load_example","arguments":{}}}`)})
		if err != nil {
			return codexruntime.Result{}, err
		}
		encoded, _ := json.Marshal(load)
		if !strings.Contains(string(encoded), "example_value") {
			return codexruntime.Result{}, errors.New("Codex did not receive the updated tool catalog")
		}
		result, err := config.OnServerRequest(codexruntime.ServerRequest{Method: "item/tool/call", Params: json.RawMessage(
			`{"tool":"beaver_call_tool","callId":"read","arguments":{"name":"example_value","arguments":{}}}`)})
		if err != nil {
			return codexruntime.Result{}, err
		}
		encoded, _ = json.Marshal(result)
		if !strings.Contains(string(encoded), "tool-value") {
			return codexruntime.Result{}, errors.New("Codex did not receive Beaver Runner's tool output")
		}
		config.OnDelta("tool-value")
		return codexruntime.Result{Answer: "tool-value"}, nil
	}

	model := New(run, time.Second)
	defer model.Close()
	var runner *agent.Runner
	runner, err := agent.NewRunner(model, "codex", []agent.Tool{{
		Spec: agent.ToolSpec{Name: "load_example", Loader: true},
		Run: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "loaded", runner.AddTools([]agent.Tool{{
				Spec: agent.ToolSpec{Name: "example_value"},
				Run:  func(context.Context, json.RawMessage) (string, error) { return "tool-value", nil },
			}})
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var streamed strings.Builder
	result, err := runner.Run(context.Background(), agent.RunRequest{
		Messages:    []agent.Message{{Role: agent.RoleUser, Content: "example"}},
		MaxSteps:    3,
		OnTextDelta: func(delta string) { streamed.WriteString(delta) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Steps != 3 || result.Answer != "tool-value" || streamed.String() != result.Answer {
		t.Fatalf("unexpected tool round trip: steps=%d answer=%q stream=%q", result.Steps, result.Answer, streamed.String())
	}
}
