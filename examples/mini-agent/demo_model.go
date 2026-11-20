package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// DemoModel 是一个确定性的“假模型”。它让教程在没有 API Key 时也能完整运行，
// 同时把测试重点放在 Agent 循环，而不是外部网络。
type DemoModel struct{}

func (DemoModel) Generate(ctx context.Context, request ModelRequest) (Message, error) {
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	if len(request.Messages) == 0 {
		return Message{}, errors.New("没有输入消息")
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role == RoleTool {
		var result struct {
			OK            bool `json:"ok"`
			Bytes         int  `json:"bytes"`
			Runes         int  `json:"runes"`
			NonSpaceRunes int  `json:"non_space_runes"`
		}
		if err := json.Unmarshal([]byte(last.Content), &result); err != nil || !result.OK {
			return Message{Content: "工具没有返回可用的统计结果：" + last.Content}, nil
		}
		return Message{Content: fmt.Sprintf(
			"统计完成：%d 个 UTF-8 字节，%d 个 Unicode 字符，其中 %d 个不是空白。",
			result.Bytes, result.Runes, result.NonSpaceRunes,
		)}, nil
	}

	if len(request.Tools) == 0 {
		return Message{Content: "本步不能调用工具，因此无法完成精确统计。"}, nil
	}
	text := last.Content
	if before, after, found := strings.Cut(text, "："); found && strings.TrimSpace(before) != "" {
		text = strings.TrimSpace(after)
	}
	arguments, _ := json.Marshal(map[string]string{"text": text})
	return Message{ToolCalls: []ToolCall{{
		ID:        "call_demo_1",
		Name:      "text_stats",
		Arguments: arguments,
	}}}, nil
}
