package chatgpt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/lakernote/beaver-agent/internal/appenv"
	"github.com/lakernote/beaver-agent/internal/codexruntime"
)

// CodexClient gives each authorized account a private CODEX_HOME. Beaver only
// manages account metadata. The official Codex client owns the OAuth tokens.
type CodexClient struct {
	root string
	env  *appenv.Environment

	// Serialize app-server processes for an account so two tasks cannot rotate
	// the same refresh token concurrently. Different accounts run independently.
	locks sync.Map
}

func NewCodex(env *appenv.Environment) *CodexClient {
	return &CodexClient{root: filepath.Join(env.Home(), "chatgpt-codex"), env: env}
}

func (client *CodexClient) Accounts() ([]Account, error) {
	entries, err := os.ReadDir(client.root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	accounts := make([]Account, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validCodexAccountID(entry.Name()) {
			continue
		}
		account, err := client.Account(entry.Name())
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, nil
}

func (client *CodexClient) Account(id string) (Account, error) {
	if !validCodexAccountID(id) {
		return Account{}, ErrAccountMissing
	}
	home := filepath.Join(client.root, id)
	data, err := os.ReadFile(filepath.Join(home, "account.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Account{}, ErrAccountMissing
	}
	if err != nil {
		return Account{}, err
	}
	var account Account
	if err := json.Unmarshal(data, &account); err != nil {
		return Account{}, fmt.Errorf("读取 Codex 账号信息: %w", err)
	}
	account.ID = id
	account.SignedIn = hasCodexCredentials(home)
	account.PlanAuthorized = account.SignedIn
	return account, nil
}

func hasCodexCredentials(home string) bool {
	info, err := os.Lstat(filepath.Join(home, "auth.json"))
	return err == nil && info.Mode().IsRegular() &&
		(runtime.GOOS == "windows" || info.Mode().Perm()&0o077 == 0)
}

func validCodexAccountID(id string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(id)
	return err == nil && len(decoded) == 32
}

func (client *CodexClient) lock(ctx context.Context, id string) (func(), error) {
	value, _ := client.locks.LoadOrStore(id, make(chan struct{}, 1))
	lock := value.(chan struct{})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	}
}

func (client *CodexClient) config(ctx context.Context, home string, config codexruntime.Config) (codexruntime.Config, error) {
	status := codexruntime.DetectContext(ctx, client.env)
	if !status.AppServerAvailable {
		return config, errors.New(status.Message)
	}
	// A separate working directory keeps native Codex discovery away from the
	// task project. The Beaver Runner still uses the real project workspace.
	config.Workspace = filepath.Join(client.root, ".workspace")
	if err := os.MkdirAll(config.Workspace, 0o700); err != nil {
		return config, err
	}
	config.Path = status.Path
	config.ManagedChatGPT = true
	config.Env = []string{"CODEX_HOME=" + home}
	for _, entry := range client.env.Environ(nil) {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if strings.HasPrefix(key, "CODEX_") || strings.HasPrefix(key, "OPENAI_") || strings.HasPrefix(key, "CHATGPT_") {
			continue
		}
		config.Env = append(config.Env, entry)
	}
	return config, nil
}

func (client *CodexClient) StartLogin(request, parent context.Context, id string, stillAllowed func() bool) (*Login, error) {
	var previous Account
	if id != "" {
		var err error
		previous, err = client.Account(id)
		if err != nil {
			return nil, err
		}
	} else {
		id = randomSecret()
	}
	if err := os.MkdirAll(client.root, 0o700); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(client.root, ".login-")
	if err != nil {
		return nil, err
	}
	config, err := client.config(request, staging, codexruntime.Config{Timeout: 5 * time.Minute})
	if err != nil {
		_ = os.RemoveAll(staging)
		return nil, err
	}

	ctx, cancel := context.WithTimeout(parent, config.Timeout)
	login := &Login{ID: randomSecret(), done: make(chan struct{}), cancel: cancel}
	ready := make(chan string, 1)
	go func() {
		defer cancel()
		account, err := codexruntime.SignInChatGPT(ctx, config, func(target string) { ready <- target })
		var value Account
		if err == nil {
			value, err = client.saveLogin(ctx, staging, id, previous, account, stillAllowed)
		}
		if err != nil && ctx.Err() != nil {
			err = errors.New("Codex 登录已取消或超时，请重新开始")
		}
		_ = os.RemoveAll(staging)
		login.complete(value, err)
	}()
	select {
	case login.URL = <-ready:
		return login, nil
	case <-login.Done():
		_, err := login.Result()
		return nil, err
	case <-request.Done():
		cancel()
		return nil, request.Err()
	}
}

func (client *CodexClient) saveLogin(ctx context.Context, staging, id string, previous Account, account codexruntime.ChatGPTAccount, stillAllowed func() bool) (Account, error) {
	if previous.Email != "" && !strings.EqualFold(previous.Email, account.Email) {
		return Account{}, errors.New("请用原账号重新授权，或选择添加账号")
	}
	if previous.Plan != "" && account.PlanType != "" && previous.Plan != account.PlanType {
		return Account{}, errors.New("请用原工作空间重新授权，或选择添加账号")
	}
	unlock, err := client.lock(ctx, id)
	if err != nil {
		return Account{}, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return Account{}, err
	}
	if stillAllowed != nil && !stillAllowed() {
		return Account{}, errors.New("Beaver 登录已失效，请重新登录后再授权")
	}
	if !hasCodexCredentials(staging) {
		return Account{}, errors.New("Codex 没有写入有效的本机凭据文件")
	}
	home := filepath.Join(client.root, id)
	if previous.ID != "" {
		// Reauth replaces only the official auth file after success. A canceled
		// login leaves the previous login and all bound profiles usable.
		if err := os.Rename(filepath.Join(staging, "auth.json"), filepath.Join(home, "auth.json")); err != nil {
			return Account{}, err
		}
		return client.Account(id)
	}
	value := Account{ID: id, Email: account.Email, Plan: account.PlanType}
	metadata, err := json.Marshal(value)
	if err != nil {
		return Account{}, err
	}
	if err := os.WriteFile(filepath.Join(staging, "account.json"), metadata, 0o600); err != nil {
		return Account{}, err
	}
	if err := os.Rename(staging, home); err != nil {
		return Account{}, err
	}
	return client.Account(id)
}

func (client *CodexClient) SignOut(ctx context.Context, id string) (bool, error) {
	if _, err := client.Account(id); err != nil {
		return false, err
	}
	unlock, err := client.lock(ctx, id)
	if err != nil {
		return false, err
	}
	defer unlock()
	err = os.Remove(filepath.Join(client.root, id, "auth.json"))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return false, err
}

func (client *CodexClient) session(ctx context.Context, id string, config codexruntime.Config) (codexruntime.Config, func(), error) {
	if !validCodexAccountID(id) {
		return config, nil, ErrAccountMissing
	}
	unlock, err := client.lock(ctx, id)
	if err != nil {
		return config, nil, err
	}
	account, err := client.Account(id)
	if err == nil && !account.SignedIn {
		err = ErrSignInRequired
	}
	if err == nil {
		config, err = client.config(ctx, filepath.Join(client.root, id), config)
	}
	if err != nil {
		unlock()
		return config, nil, err
	}
	return config, unlock, nil
}

func (client *CodexClient) Models(ctx context.Context, id string) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	config, unlock, err := client.session(ctx, id, codexruntime.Config{})
	if err != nil {
		return nil, err
	}
	defer unlock()
	raw, err := codexruntime.Call(ctx, config, "model/list", map[string]any{"limit": 100, "includeHidden": false})
	if err != nil {
		return nil, err
	}
	var response struct {
		Data []struct {
			Model, DisplayName string
			Hidden             bool
		}
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(response.Data))
	for _, value := range response.Data {
		if value.Model != "" && !value.Hidden {
			models = append(models, Model{Slug: value.Model, DisplayName: value.DisplayName})
		}
	}
	return models, nil
}

func (client *CodexClient) RunTurn(ctx context.Context, id string, config codexruntime.Config, prompt string) (codexruntime.Result, error) {
	config, unlock, err := client.session(ctx, id, config)
	if err != nil {
		return codexruntime.Result{}, err
	}
	defer unlock()
	return codexruntime.RunMessage(ctx, config, prompt)
}
