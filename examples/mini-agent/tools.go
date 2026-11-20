package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

func textStatsTool() Tool {
	return Tool{
		Spec: ToolSpec{
			Name:        "text_stats",
			Description: "统计文本的 UTF-8 字节数、Unicode 字符数和非空白字符数",
			Parameters: objectSchema(
				map[string]any{
					"text": map[string]any{
						"type":        "string",
						"description": "需要统计的原始文本",
					},
				},
				[]string{"text"},
			),
		},
		Run: runTextStats,
	}
}

func runTextStats(ctx context.Context, raw json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var input struct {
		Text string `json:"text"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return "", err
	}
	if strings.TrimSpace(input.Text) == "" {
		return "", errors.New("text 不能为空")
	}
	nonSpace := 0
	for _, character := range input.Text {
		if !unicode.IsSpace(character) {
			nonSpace++
		}
	}
	encoded, err := json.Marshal(map[string]any{
		"ok":              true,
		"bytes":           len([]byte(input.Text)),
		"runes":           utf8.RuneCountInString(input.Text),
		"non_space_runes": nonSpace,
	})
	return string(encoded), err
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
