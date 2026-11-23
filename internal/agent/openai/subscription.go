package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	core "github.com/lakernote/beaver-agent/internal/agent"
)

const beaverToolNamespace = "beaver"

// Stateless Responses returns encrypted reasoning that should accompany the
// tool result within this run. Beaver only persists the visible message and
// function call for later turns.
type subscriptionReasoningItem struct {
	Type             string   `json:"type"`
	ID               string   `json:"id"`
	Summary          []string `json:"summary"`
	EncryptedContent string   `json:"encrypted_content"`
}

func subscriptionReasoning(message *core.Message, outputs []responsesOutput) error {
	var reasoning []subscriptionReasoningItem
	for _, item := range outputs {
		switch {
		case item.Type == "function_call" && item.Namespace != "" && item.Namespace != beaverToolNamespace:
			return errors.New("ChatGPT 订阅返回了未知命名空间的工具调用")
		case item.Type == "reasoning" && item.ID != "" && item.EncryptedContent != "":
			reasoning = append(reasoning, subscriptionReasoningItem{
				Type: item.Type, ID: item.ID, Summary: []string{}, EncryptedContent: item.EncryptedContent,
			})
		}
	}
	if len(reasoning) > 0 {
		message.ReasoningDetails, _ = json.Marshal(reasoning)
	}
	return nil
}

func subscriptionError(status int, failure *responsesError) error {
	if failure != nil {
		switch failure.Code {
		case "subscription_sharing_usage_limit_exceeded":
			return errors.New("ChatGPT 订阅用量已达上限，请在 ChatGPT 设置 → 用量查看套餐和 Beaver 的用量限制")
		case "subscription_sharing_user_not_eligible":
			return errors.New("当前 ChatGPT 账号或工作区没有开放订阅用量共享")
		case "subscription_sharing_invalid_user":
			return errors.New("ChatGPT 订阅授权已失效，请在模型配置里重新连接")
		case "chatpass_v2_scope_not_authorized", "chatpass_v2_invalid_authorization_context":
			return errors.New("ChatGPT 没有授予此连接订阅用量权限，请重新授权")
		case "subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable":
			return &core.ModelError{StatusCode: http.StatusServiceUnavailable, Message: "ChatGPT 订阅用量服务暂不可用"}
		case "subscription_sharing_unsupported_capability":
			return errors.New("当前模型或请求参数不在 ChatGPT 订阅预览支持范围内")
		}
	}
	if status == http.StatusUnauthorized {
		return errors.New("ChatGPT 订阅授权已失效，请在模型配置里重新连接")
	}
	if status == 0 {
		status = http.StatusBadRequest
	}
	message := fmt.Sprintf("ChatGPT 订阅请求失败（HTTP %d）", status)
	if failure != nil && strings.TrimSpace(failure.Message) != "" {
		message += "：" + failure.Message
	}
	return &core.ModelError{StatusCode: status, Message: message}
}
