package codexruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/lakernote/beaver-agent/internal/redact"
)

type ChatGPTAccount struct {
	Type     string `json:"type"`
	Email    string `json:"email"`
	PlanType string `json:"planType"`
}

// SignInChatGPT keeps the official app-server alive for the browser callback.
// Codex exchanges and stores the credentials in the supplied CODEX_HOME.
func SignInChatGPT(ctx context.Context, config Config, onAuthorize func(string)) (ChatGPTAccount, error) {
	var account ChatGPTAccount
	err := withAppServer(ctx, config, func(request appServerRequest, read func() (rpcMessage, error)) error {
		raw, err := request(2, "account/login/start", map[string]string{"type": "chatgpt"})
		if err != nil {
			return err
		}
		var login struct {
			Type, LoginID, AuthURL string
		}
		if err := json.Unmarshal(raw, &login); err != nil {
			return err
		}
		target, err := url.Parse(login.AuthURL)
		if err != nil || login.Type != "chatgpt" || login.LoginID == "" || target.Scheme != "https" ||
			target.Host != "auth.openai.com" || target.User != nil {
			return errors.New("Codex 未返回有效的 OpenAI 授权地址")
		}
		onAuthorize(login.AuthURL)

		for {
			message, err := read()
			if err != nil {
				return err
			}
			if message.Method != "account/login/completed" {
				continue
			}
			var completed struct {
				LoginID, Error string
				Success        bool
			}
			if err := json.Unmarshal(message.Params, &completed); err != nil {
				return err
			}
			if completed.LoginID != login.LoginID {
				continue
			}
			if !completed.Success {
				if completed.Error == "" {
					return errors.New("Codex 登录未完成")
				}
				return fmt.Errorf("Codex 登录未完成：%s", redact.Sanitize(completed.Error))
			}
			break
		}
		raw, err = request(3, "account/read", map[string]bool{"refreshToken": false})
		if err != nil {
			return err
		}
		var result struct{ Account ChatGPTAccount }
		if err := json.Unmarshal(raw, &result); err != nil {
			return err
		}
		account = result.Account
		if account.Type != "chatgpt" {
			return errors.New("请使用自己的 ChatGPT 账号登录 Codex")
		}
		return nil
	})
	return account, err
}
