package codexruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// OAuthStatus reads Codex's credential-aware MCP status without starting a
// login flow. The CLI returns the same auth-state vocabulary as app-server's
// mcpServerStatus/list response.
func OAuthStatus(ctx context.Context, config Config, name string) (bool, string, error) {
	// `mcp get --json` describes transport configuration but, in current Codex
	// CLI releases, does not include auth_status. The list JSON does, so use it
	// for status checks and select the managed server by name.
	output, err := runMCPCommand(ctx, config, "list", "--json")
	if err != nil {
		return false, "", err
	}
	return parseMCPOAuthStatusForName(output, MCPServerName(name))
}

func parseMCPOAuthStatus(output []byte) (bool, string, error) {
	var value struct {
		AuthStatus string `json:"auth_status"`
	}
	if err := json.Unmarshal(output, &value); err != nil {
		return false, "", fmt.Errorf("解析 Codex MCP OAuth 状态: %w", err)
	}
	return oauthStatusValue(value.AuthStatus)
}

func parseMCPOAuthStatusForName(output []byte, name string) (bool, string, error) {
	var values []struct {
		Name       string `json:"name"`
		AuthStatus string `json:"auth_status"`
	}
	if err := json.Unmarshal(output, &values); err != nil {
		// Keep compatibility with older/fake Codex runtimes that return one
		// object from a status command instead of the list shape.
		return parseMCPOAuthStatus(output)
	}
	for _, value := range values {
		if value.Name == name {
			return oauthStatusValue(value.AuthStatus)
		}
	}
	return false, "Codex 未找到 MCP 配置", nil
}

func oauthStatusValue(authStatus string) (bool, string, error) {
	switch strings.ToLower(strings.ReplaceAll(authStatus, "_", "")) {
	case "oauth":
		return true, "Codex OAuth 凭证已保存", nil
	case "notloggedin", "unknown", "":
		return false, "Codex 尚未授权此 MCP", nil
	default:
		return false, "Codex 没有可用的 OAuth 凭证", nil
	}
}

// OAuthLogout removes credentials from Codex's own credential store. Removing
// the config.toml entry alone is insufficient because the store is independent.
func OAuthLogout(ctx context.Context, config Config, name string) error {
	_, err := runMCPCommand(ctx, config, "logout", MCPServerName(name))
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "not logged in") || strings.Contains(message, "no oauth credential") || strings.Contains(message, "no oauth token") {
		return nil
	}
	return err
}

func runMCPCommand(ctx context.Context, config Config, arguments ...string) ([]byte, error) {
	if strings.TrimSpace(config.Path) == "" {
		return nil, errors.New("Codex Runtime 未安装")
	}
	command := exec.CommandContext(ctx, config.Path, append([]string{"mcp"}, arguments...)...)
	if strings.TrimSpace(config.Workspace) != "" {
		command.Dir = config.Workspace
	}
	if len(config.Env) > 0 {
		command.Env = config.Env
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("Codex MCP 命令失败: %s", detail)
	}
	return output, nil
}

// OAuthLogin starts a long-lived Codex app-server OAuth transaction. The
// authorization URL is emitted once; Codex itself performs discovery,
// registration, token exchange, refresh, and credential storage.
func OAuthLogin(ctx context.Context, config Config, name string, scopes []string, onAuthorizationURL func(string) error) error {
	if strings.TrimSpace(config.Path) == "" {
		return errors.New("Codex Runtime 未安装")
	}
	command := exec.Command(config.Path, "app-server")
	command.Dir = config.Workspace
	if len(config.Env) > 0 {
		command.Env = config.Env
	}
	configureProcessTree(command)
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return err
	}
	var stderrTail synchronizedBuffer
	stderrTail.limit = 16 * 1024
	go func() { _, _ = io.Copy(&stderrTail, stderr) }()
	if err := command.Start(); err != nil {
		return fmt.Errorf("启动 Codex app-server: %w", err)
	}
	defer func() { stopProcess(command, stdin) }()

	type readResult struct {
		message rpcMessage
		err     error
	}
	readResults := make(chan readResult, 1)
	go func() {
		decoder := json.NewDecoder(stdout)
		for {
			var message rpcMessage
			if decodeErr := decoder.Decode(&message); decodeErr != nil {
				if errors.Is(decodeErr, io.EOF) {
					if detail := strings.TrimSpace(stderrTail.Snapshot()); detail != "" {
						readResults <- readResult{err: fmt.Errorf("Codex app-server 已退出: %s", detail)}
					} else {
						readResults <- readResult{err: io.EOF}
					}
					return
				}
				readResults <- readResult{err: fmt.Errorf("Codex app-server 返回无效 JSON: %w", decodeErr)}
				return
			}
			select {
			case readResults <- readResult{message: message}:
			case <-ctx.Done():
				return
			}
		}
	}()
	var writeMu sync.Mutex
	write := func(value any) error {
		payload, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return marshalErr
		}
		payload = append(payload, '\n')
		writeMu.Lock()
		defer writeMu.Unlock()
		_, writeErr := stdin.Write(payload)
		return writeErr
	}
	read := func() (rpcMessage, error) {
		select {
		case <-ctx.Done():
			return rpcMessage{}, ctx.Err()
		case result := <-readResults:
			return result.message, result.err
		}
	}
	respond := func(message rpcMessage) error {
		return write(map[string]any{"id": json.RawMessage(message.ID), "error": map[string]any{"code": -32000, "message": "EasyAgent 不处理此 Codex app-server 请求"}})
	}
	request := func(id int, method string, params any) (json.RawMessage, error) {
		if err := write(map[string]any{"method": method, "id": id, "params": params}); err != nil {
			return nil, err
		}
		for {
			message, err := read()
			if err != nil {
				return nil, err
			}
			if len(message.ID) > 0 && message.Method != "" {
				if err := respond(message); err != nil {
					return nil, err
				}
				continue
			}
			if len(message.ID) == 0 || string(message.ID) != fmt.Sprintf("%d", id) {
				continue
			}
			if len(message.Error) > 0 && string(message.Error) != "null" {
				return nil, rpcError(message.Error)
			}
			return message.Result, nil
		}
	}
	if _, err := request(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "easyagent", "title": "EasyAgent", "version": "0.1.0"}}); err != nil {
		return err
	}
	if err := write(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return err
	}
	params := map[string]any{"name": MCPServerName(name)}
	if len(scopes) > 0 {
		params["scopes"] = scopes
	}
	result, err := request(2, "mcpServer/oauth/login", params)
	if err != nil {
		return err
	}
	var login struct {
		AuthorizationURL string `json:"authorizationUrl"`
	}
	if err := json.Unmarshal(result, &login); err != nil {
		return err
	}
	if strings.TrimSpace(login.AuthorizationURL) == "" {
		return errors.New("Codex app-server 未返回 OAuth authorizationUrl")
	}
	if onAuthorizationURL != nil {
		if err := onAuthorizationURL(login.AuthorizationURL); err != nil {
			return err
		}
	}
	for {
		message, err := read()
		if err != nil {
			return err
		}
		if message.Method != "mcpServer/oauthLogin/completed" {
			if len(message.ID) > 0 && message.Method != "" {
				if err := respond(message); err != nil {
					return err
				}
			}
			continue
		}
		var completed struct {
			Name    string `json:"name"`
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal(message.Params, &completed); err != nil {
			return err
		}
		if completed.Name != "" && completed.Name != MCPServerName(name) {
			continue
		}
		if !completed.Success {
			if completed.Error == "" {
				completed.Error = "Codex OAuth 登录失败"
			}
			return errors.New(completed.Error)
		}
		return nil
	}
}
