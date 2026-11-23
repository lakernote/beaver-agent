package codex

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lakernote/beaver-agent/internal/agent"
	"github.com/lakernote/beaver-agent/internal/codexruntime"
)

func toolCatalog(tools []agent.ToolSpec) string {
	values := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		values = append(values, map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters})
	}
	data, _ := json.Marshal(values)
	return string(data)
}

func turnInput(request agent.Request, catalog string, useTools bool) (codexruntime.Config, string, error) {
	config := codexruntime.Config{Model: request.Model, ClientTools: &codexruntime.ClientTools{}}
	if len(request.Messages) == 0 {
		return config, "", errors.New("Codex 模型至少需要一条消息")
	}
	var instructions []string
	instructions = append(instructions, "工具由 Beaver 执行。只能通过 beaver_call_tool 调用 Beaver 工具。选择 name，并按对应参数 Schema 填写 arguments。使用最新的 Beaver 可用工具目录。目录为空时直接回答。")
	if useTools {
		instructions = append(instructions, "本轮 Beaver 工具目录："+catalog)
		config.ClientTools.Specs = []map[string]any{{
			"type": "function", "name": dispatch, "description": "调用 Beaver 的已注册工具。工具名称和参数 Schema 见当前 Beaver 工具目录。",
			"inputSchema": map[string]any{
				"type": "object", "required": []string{"name", "arguments"}, "additionalProperties": false,
				"properties": map[string]any{
					"name":      map[string]any{"type": "string"},
					"arguments": map[string]any{"type": "object", "additionalProperties": true},
				},
			},
		}}
	}
	last := len(request.Messages) - 1
	current := request.Messages[last]
	end := last
	if current.Role != agent.RoleUser {
		end = len(request.Messages)
		current = agent.Message{Role: agent.RoleUser, Content: "请继续处理本轮请求。"}
	}
	for _, message := range request.Messages[:end] {
		if message.Role == agent.RoleSystem {
			instructions = append(instructions, message.Content)
		} else {
			config.ClientTools.History = append(config.ClientTools.History, historyItems(message)...)
		}
	}
	config.DeveloperInstructions = strings.Join(instructions, "\n\n")
	for _, file := range current.Attachments {
		config.Attachments = append(config.Attachments, codexruntime.Attachment{
			Name: file.Name, MIMEType: file.MIMEType, Kind: file.Kind, Data: file.Data,
		})
	}
	return config, current.Content, nil
}

func historyItems(message agent.Message) []map[string]any {
	if message.Role == agent.RoleTool {
		return []map[string]any{{"type": "function_call_output", "call_id": message.ToolCallID, "output": message.Content}}
	}
	var items []map[string]any
	var content []map[string]any
	role, contentType := string(message.Role), "input_text"
	if message.Role == agent.RoleAssistant {
		contentType = "output_text"
	}
	if message.Content != "" {
		content = append(content, map[string]any{"type": contentType, "text": message.Content})
	}
	for _, file := range message.Attachments {
		switch file.Kind {
		case "image":
			content = append(content, map[string]any{"type": "input_image", "image_url": imageURL(file.MIMEType, file.Data)})
		case "text":
			content = append(content, map[string]any{"type": "input_text", "text": fmt.Sprintf("用户附件 %q\n%s", file.Name, file.Data)})
		case "pdf":
			content = append(content, map[string]any{"type": "input_text", "text": fmt.Sprintf("此前的 PDF 附件 %q", file.Name)})
		}
	}
	if len(content) > 0 {
		items = append(items, map[string]any{"type": "message", "role": role, "content": content})
	}
	for _, call := range message.ToolCalls {
		arguments, _ := json.Marshal(map[string]any{"name": call.Name, "arguments": call.Arguments})
		items = append(items, map[string]any{"type": "function_call", "name": dispatch, "call_id": call.ID, "arguments": string(arguments)})
	}
	return items
}

func imageURL(mimeType string, data []byte) string {
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
}
