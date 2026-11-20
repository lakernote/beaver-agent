package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	mode := flag.String("mode", envOr("MINI_AGENT_MODE", "demo"), "模型模式：demo 或 openai")
	baseURL := flag.String("base-url", os.Getenv("MINI_AGENT_BASE_URL"), "OpenAI-compatible API 的 /v1 基础地址")
	modelName := flag.String("model", os.Getenv("MINI_AGENT_MODEL"), "模型名称")
	apiKey := flag.String("api-key", os.Getenv("MINI_AGENT_API_KEY"), "API Key；本地服务可留空")
	trace := flag.Bool("trace", os.Getenv("MINI_AGENT_TRACE") == "1", "打印完整消息轨迹")
	maxSteps := flag.Int("max-steps", defaultMaxSteps, "最大模型调用步数")
	toolTimeout := flag.Duration("tool-timeout", defaultToolTimeout, "单次工具的协作式超时")
	flag.Parse()

	prompt := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if prompt == "" {
		prompt = "请统计：Agent = 模型 + 工具 + 循环"
	}

	var model Model
	switch strings.ToLower(strings.TrimSpace(*mode)) {
	case "demo":
		model = DemoModel{}
	case "openai":
		adapter, err := NewOpenAICompatibleModel(*baseURL, *modelName, *apiKey)
		if err != nil {
			exitWithError(err)
		}
		model = adapter
	default:
		exitWithError(fmt.Errorf("未知 mode %q；只能是 demo 或 openai", *mode))
	}

	runner, err := NewRunner(model, []Tool{textStatsTool()})
	if err != nil {
		exitWithError(err)
	}
	runner.MaxSteps = *maxSteps
	runner.ToolTimeout = *toolTimeout

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startedAt := time.Now()
	answer, history, err := runner.Run(ctx, []Message{
		{Role: RoleSystem, Content: "你是一个教学 Agent。需要精确统计文本时必须调用 text_stats。"},
		{Role: RoleUser, Content: prompt},
	})
	if err != nil {
		exitWithError(err)
	}
	if *trace {
		printTrace(history)
	}
	fmt.Printf("\n最终回答：%s\n", answer)
	fmt.Printf("耗时：%s\n", time.Since(startedAt).Round(time.Millisecond))
}

func printTrace(messages []Message) {
	fmt.Println("消息轨迹：")
	for index, message := range messages {
		fmt.Printf("%d. %s", index+1, message.Role)
		if message.Name != "" {
			fmt.Printf(" name=%s", message.Name)
		}
		if message.ToolCallID != "" {
			fmt.Printf(" tool_call_id=%s", message.ToolCallID)
		}
		if message.Content != "" {
			fmt.Printf("\n   content: %s", message.Content)
		}
		for _, call := range message.ToolCalls {
			fmt.Printf("\n   tool_call: id=%s name=%s arguments=%s", call.ID, call.Name, call.Arguments)
		}
		fmt.Println()
	}
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func exitWithError(err error) {
	fmt.Fprintln(os.Stderr, "错误：", err)
	os.Exit(1)
}
