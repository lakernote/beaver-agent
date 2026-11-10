package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lakernote/beaver-agent/internal/codexruntime"
	"github.com/lakernote/beaver-agent/internal/mcpclient"
	"github.com/lakernote/beaver-agent/internal/store"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

const (
	mcpOAuthRuntimeBeaver = "beaver"
	mcpOAuthTimeout       = 10 * time.Minute
)

type mcpOAuthCallback struct {
	Code  string
	State string
	Iss   string
	Err   string
}

type mcpOAuthFlow struct {
	mcpID       string
	redirectURL string
	manager     *mcpOAuthManager
	ctx         context.Context
	cancel      context.CancelFunc

	urlCh      chan string
	callbackCh chan mcpOAuthCallback
	doneCh     chan error

	mu               sync.Mutex
	authorizationURL string
	state            string
	resource         string
}

type mcpOAuthManager struct {
	server *Server

	mu          sync.Mutex
	active      map[string]*mcpOAuthFlow
	byState     map[string]*mcpOAuthFlow
	codexActive map[string]*codexOAuthFlow
}

type codexOAuthFlow struct {
	mcpID            string
	ctx              context.Context
	cancel           context.CancelFunc
	urlCh            chan string
	doneCh           chan error
	mu               sync.Mutex
	authorizationURL string
}

func newMCPOAuthManager(server *Server) *mcpOAuthManager {
	return &mcpOAuthManager{server: server, active: map[string]*mcpOAuthFlow{}, byState: map[string]*mcpOAuthFlow{}, codexActive: map[string]*codexOAuthFlow{}}
}

type mcpOAuthStatus struct {
	Runtime          string     `json:"runtime"`
	Authorized       bool       `json:"authorized"`
	Pending          bool       `json:"pending"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
	AuthorizationURL string     `json:"authorizationUrl,omitempty"`
	Message          string     `json:"message,omitempty"`
}

func (server *Server) startMCPOAuth(response http.ResponseWriter, request *http.Request) {
	if strings.EqualFold(strings.TrimSpace(request.URL.Query().Get("runtime")), "codex") {
		status, err := server.mcpOAuth.startCodex(request.Context(), request.PathValue("id"))
		if err != nil {
			writeError(response, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(response, http.StatusOK, status)
		return
	}
	status, err := server.mcpOAuth.start(request.Context(), request, request.PathValue("id"))
	if err != nil {
		writeError(response, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, status)
}

func (server *Server) getMCPOAuthStatus(response http.ResponseWriter, request *http.Request) {
	if strings.EqualFold(strings.TrimSpace(request.URL.Query().Get("runtime")), "codex") {
		status, err := server.mcpOAuth.codexStatus(request.Context(), request.PathValue("id"))
		if err != nil {
			writeError(response, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(response, http.StatusOK, status)
		return
	}
	status, err := server.mcpOAuth.status(request.PathValue("id"))
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, status)
}

func (server *Server) logoutMCPOAuth(response http.ResponseWriter, request *http.Request) {
	var err error
	if strings.EqualFold(strings.TrimSpace(request.URL.Query().Get("runtime")), "codex") {
		err = server.mcpOAuth.logoutCodex(request.Context(), request.PathValue("id"))
	} else {
		err = server.mcpOAuth.logout(request.PathValue("id"))
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"ok": true})
}

func (server *Server) completeMCPOAuth(response http.ResponseWriter, request *http.Request) {
	var rawBody []byte
	if request.Body != nil {
		var err error
		const maxCallbackBody = 1 << 20
		rawBody, err = io.ReadAll(io.LimitReader(request.Body, maxCallbackBody+1))
		if err != nil || len(rawBody) > maxCallbackBody {
			writeOAuthCallbackPage(response, false, "OAuth 回调内容无效")
			return
		}
		_ = request.Body.Close()
		request.Body = io.NopCloser(bytes.NewReader(rawBody))
	}
	if err := request.ParseForm(); err != nil {
		writeOAuthCallbackPage(response, false, "OAuth 回调参数无效")
		return
	}
	state := strings.TrimSpace(request.FormValue("state"))
	if state == "" {
		writeOAuthCallbackPage(response, false, "OAuth 回调缺少 state")
		return
	}
	callback := mcpOAuthCallback{State: state, Code: strings.TrimSpace(request.FormValue("code")), Iss: strings.TrimSpace(request.FormValue("iss"))}
	if providerError := strings.TrimSpace(request.FormValue("error")); providerError != "" {
		callback.Err = "OAuth provider 返回了授权错误"
	}
	if callback.Code == "" && callback.Err == "" {
		callback.Err = "OAuth 回调缺少授权码"
	}
	if !server.mcpOAuth.complete(request.PathValue("id"), callback) {
		if port := server.mcpOAuth.callbackProxyPort(request.PathValue("id")); port > 0 {
			request.Body = io.NopCloser(bytes.NewReader(rawBody))
			request.ContentLength = int64(len(rawBody))
			server.mcpOAuth.proxyCallback(response, request, port)
			return
		}
		writeOAuthCallbackPage(response, false, "OAuth 回调已过期或不匹配")
		return
	}
	writeOAuthCallbackPage(response, callback.Err == "", "授权完成，可以返回 Beaver")
}

func writeOAuthCallbackPage(response http.ResponseWriter, success bool, message string) {
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	status := http.StatusOK
	title := "OAuth 授权失败"
	if success {
		title = "OAuth 授权完成"
	} else {
		status = http.StatusBadRequest
	}
	response.WriteHeader(status)
	_, _ = fmt.Fprintf(response, "<!doctype html><meta charset=utf-8><title>%s</title><p>%s</p><p>可以关闭此页面并返回 Beaver。</p>", html.EscapeString(title), html.EscapeString(message))
}

func (manager *mcpOAuthManager) start(parent context.Context, request *http.Request, id string) (mcpOAuthStatus, error) {
	configs, err := manager.server.store.ListMCPConfigs()
	if err != nil {
		return mcpOAuthStatus{}, err
	}
	var config *store.MCPConfig
	for index := range configs {
		if configs[index].ID == id {
			value := configs[index]
			config = &value
			break
		}
	}
	if config == nil {
		return mcpOAuthStatus{}, errors.New("MCP 配置不存在")
	}
	if config.Transport != "http" && config.Transport != "streamable_http" {
		return mcpOAuthStatus{}, errors.New("OAuth 只支持 HTTP MCP")
	}
	if !strings.EqualFold(config.AuthType, "oauth") {
		return mcpOAuthStatus{}, errors.New("此 MCP 的认证方式不是 OAuth")
	}
	if !config.Enabled {
		return mcpOAuthStatus{}, errors.New("请先启用此 MCP，再开始 OAuth 授权")
	}

	manager.mu.Lock()
	if existing := manager.active[id]; existing != nil {
		existing.mu.Lock()
		status := mcpOAuthStatus{Runtime: mcpOAuthRuntimeBeaver, Pending: true, AuthorizationURL: existing.authorizationURL, Message: "等待浏览器完成授权"}
		existing.mu.Unlock()
		manager.mu.Unlock()
		return status, nil
	}
	flowContext, cancel := context.WithTimeout(manager.server.ctx, mcpOAuthTimeout)
	flow := &mcpOAuthFlow{mcpID: id, manager: manager, redirectURL: oauthRedirectURL(request, *config), ctx: flowContext, cancel: cancel, urlCh: make(chan string, 1), callbackCh: make(chan mcpOAuthCallback, 1), doneCh: make(chan error, 1)}
	manager.active[id] = flow
	manager.mu.Unlock()

	go manager.run(flow, *config)
	select {
	case authorizationURL := <-flow.urlCh:
		return mcpOAuthStatus{Runtime: mcpOAuthRuntimeBeaver, Pending: true, AuthorizationURL: authorizationURL, Message: "请在浏览器中完成授权"}, nil
	case runErr := <-flow.doneCh:
		if runErr != nil {
			return mcpOAuthStatus{}, runErr
		}
		return manager.status(id)
	case <-time.After(5 * time.Second):
		return mcpOAuthStatus{Runtime: mcpOAuthRuntimeBeaver, Pending: true, Message: "授权流程已启动，正在等待 MCP 响应"}, nil
	case <-parent.Done():
		return mcpOAuthStatus{}, parent.Err()
	}
}

func (manager *mcpOAuthManager) startCodex(parent context.Context, id string) (mcpOAuthStatus, error) {
	configs, err := manager.server.store.ListMCPConfigs()
	if err != nil {
		return mcpOAuthStatus{}, err
	}
	var config *store.MCPConfig
	for index := range configs {
		if configs[index].ID == id {
			value := configs[index]
			config = &value
			break
		}
	}
	if config == nil {
		return mcpOAuthStatus{}, errors.New("MCP 配置不存在")
	}
	if !strings.EqualFold(config.AuthType, "oauth") {
		return mcpOAuthStatus{}, errors.New("此 MCP 的认证方式不是 OAuth")
	}
	if !config.Enabled {
		return mcpOAuthStatus{}, errors.New("请先启用此 MCP，再开始 Codex OAuth 授权")
	}
	status := manager.server.detectCodex(parent)
	if !status.Installed || !status.AppServerAvailable {
		return mcpOAuthStatus{}, errors.New(status.Message)
	}
	environment, err := manager.server.codexEnvironment()
	if err != nil {
		return mcpOAuthStatus{}, err
	}
	manager.mu.Lock()
	if existing := manager.codexActive[id]; existing != nil {
		existing.mu.Lock()
		result := mcpOAuthStatus{Runtime: "codex", Pending: true, AuthorizationURL: existing.authorizationURL, Message: "等待浏览器完成 Codex OAuth 授权"}
		existing.mu.Unlock()
		manager.mu.Unlock()
		return result, nil
	}
	for activeID := range manager.codexActive {
		manager.mu.Unlock()
		return mcpOAuthStatus{}, fmt.Errorf("Codex 正在为 MCP %s 进行 OAuth 授权，请完成或等待该流程结束", activeID)
	}
	flowContext, cancel := context.WithTimeout(manager.server.ctx, mcpOAuthTimeout)
	flow := &codexOAuthFlow{mcpID: id, ctx: flowContext, cancel: cancel, urlCh: make(chan string, 1), doneCh: make(chan error, 1)}
	manager.codexActive[id] = flow
	manager.mu.Unlock()
	go func() {
		runErr := codexruntime.OAuthLogin(flow.ctx, codexruntime.Config{Path: status.Path, Workspace: manager.server.env.Workspace(), Timeout: mcpOAuthTimeout, Env: environment}, id, config.OAuthScopes, func(value string) error {
			flow.mu.Lock()
			flow.authorizationURL = value
			flow.mu.Unlock()
			flow.urlCh <- value
			return nil
		})
		flow.cancel()
		manager.mu.Lock()
		delete(manager.codexActive, id)
		manager.mu.Unlock()
		flow.doneCh <- runErr
	}()
	select {
	case value := <-flow.urlCh:
		return mcpOAuthStatus{Runtime: "codex", Pending: true, AuthorizationURL: value, Message: "请在浏览器中完成 Codex OAuth 授权"}, nil
	case runErr := <-flow.doneCh:
		if runErr != nil {
			return mcpOAuthStatus{}, runErr
		}
		return manager.codexStatus(parent, id)
	case <-time.After(5 * time.Second):
		return mcpOAuthStatus{Runtime: "codex", Pending: true, Message: "Codex OAuth 流程已启动，正在等待浏览器完成"}, nil
	case <-parent.Done():
		return mcpOAuthStatus{}, parent.Err()
	}
}

func (manager *mcpOAuthManager) run(flow *mcpOAuthFlow, config store.MCPConfig) {
	handler, err := manager.newHandler(flow, config)
	if err == nil {
		clientConfig := mcpClientConfig(config)
		clientConfig.OAuthHandler = handler
		clientConfig.Enabled = true
		connection, connectErr := mcpclient.Connect(flow.ctx, manager.server.env, clientConfig)
		if connectErr != nil {
			err = connectErr
		} else {
			err = connection.Close()
		}
	}
	flow.cancel()
	flow.mu.Lock()
	state := flow.state
	flow.mu.Unlock()
	manager.mu.Lock()
	delete(manager.active, flow.mcpID)
	if state != "" {
		delete(manager.byState, state)
	}
	manager.mu.Unlock()
	flow.doneCh <- err
}

func (manager *mcpOAuthManager) newHandler(flow *mcpOAuthFlow, config store.MCPConfig) (*auth.AuthorizationCodeHandler, error) {
	credential, err := manager.server.store.GetMCPOAuthCredential(config.ID)
	if err != nil {
		return nil, err
	}
	if credential != nil && !oauthCredentialMatchesConfig(*credential, config) {
		if err := manager.server.store.DeleteMCPOAuthCredential(config.ID); err != nil {
			return nil, err
		}
		credential = nil
	}
	initial, err := manager.initialTokenSource(credential)
	if err != nil {
		return nil, err
	}
	handlerConfig := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL:         flow.redirectURL,
		RequestRefreshToken: true,
		InitialTokenSource:  initial,
		NewTokenSource: func(ctx context.Context, oauthConfig *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
			// RFC 6749 permits a refresh response to omit refresh_token. Keep the
			// previously issued value in that case; otherwise a successful refresh
			// would make the next process restart permanently unauthenticated.
			if token != nil && strings.TrimSpace(token.RefreshToken) == "" && credential != nil {
				token.RefreshToken = credential.RefreshToken
			}
			flow.mu.Lock()
			resource := flow.resource
			flow.mu.Unlock()
			issuer := ""
			if credential != nil {
				issuer = credential.Issuer
			}
			persist := func(value *oauth2.Token) error {
				return manager.persistToken(config, oauthConfig, value, resource, issuer)
			}
			if err := persist(token); err != nil {
				return nil, err
			}
			return &persistedTokenSource{base: tokenSourceWithResource(ctx, oauthConfig, token, resource), persist: persist}, nil
		},
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			parsed, parseErr := url.Parse(args.URL)
			if parseErr != nil {
				return nil, parseErr
			}
			flow.mu.Lock()
			flow.state = parsed.Query().Get("state")
			flow.resource = parsed.Query().Get("resource")
			flow.authorizationURL = args.URL
			state := flow.state
			flow.mu.Unlock()
			if state == "" {
				return nil, errors.New("OAuth authorization URL 缺少 state")
			}
			manager.mu.Lock()
			manager.byState[state] = flow
			manager.mu.Unlock()
			select {
			case flow.urlCh <- args.URL:
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-flow.ctx.Done():
				return nil, flow.ctx.Err()
			}
			select {
			case callback := <-flow.callbackCh:
				if callback.Err != "" {
					return nil, errors.New(callback.Err)
				}
				return &auth.AuthorizationResult{Code: callback.Code, State: callback.State, Iss: callback.Iss}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-flow.ctx.Done():
				return nil, flow.ctx.Err()
			}
		},
	}
	if strings.TrimSpace(config.OAuthClientID) != "" {
		handlerConfig.PreregisteredClient = &oauthex.ClientCredentials{ClientID: strings.TrimSpace(config.OAuthClientID)}
	} else if credential != nil && strings.TrimSpace(credential.ClientID) != "" {
		handlerConfig.PreregisteredClient = &oauthex.ClientCredentials{ClientID: credential.ClientID, Issuer: credential.Issuer}
		if credential.ClientSecret != "" {
			handlerConfig.PreregisteredClient.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: credential.ClientSecret}
		}
	} else {
		handlerConfig.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			RedirectURIs: []string{flow.redirectURL}, TokenEndpointAuthMethod: "none",
			GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
			ClientName: "Beaver", Scope: strings.Join(config.OAuthScopes, " "),
		}}
	}
	return auth.NewAuthorizationCodeHandler(handlerConfig)
}

func (manager *mcpOAuthManager) initialTokenSource(credential *store.MCPOAuthCredential) (oauth2.TokenSource, error) {
	if credential == nil || credential.AccessToken == "" || credential.TokenURL == "" || credential.ClientID == "" {
		return nil, nil
	}
	cfg := &oauth2.Config{ClientID: credential.ClientID, ClientSecret: credential.ClientSecret, RedirectURL: credential.RedirectURL, Scopes: append([]string(nil), credential.Scopes...), Endpoint: oauth2.Endpoint{AuthURL: credential.AuthURL, TokenURL: credential.TokenURL}}
	token := &oauth2.Token{AccessToken: credential.AccessToken, RefreshToken: credential.RefreshToken, TokenType: credential.TokenType, Expiry: credential.Expiry}
	return &persistedTokenSource{base: tokenSourceWithResource(context.Background(), cfg, token, credential.Resource), persist: func(value *oauth2.Token) error {
		return manager.persistExistingToken(*credential, cfg, value)
	}}, nil
}

func (manager *mcpOAuthManager) persistToken(binding store.MCPConfig, config *oauth2.Config, token *oauth2.Token, resource, issuer string) error {
	if token == nil || strings.TrimSpace(token.AccessToken) == "" {
		return errors.New("OAuth token 为空")
	}
	return manager.server.store.SaveMCPOAuthCredential(store.MCPOAuthCredential{
		MCPID: binding.ID, Endpoint: strings.TrimSpace(binding.Endpoint), AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, TokenType: token.TokenType,
		ConfiguredClientID: strings.TrimSpace(binding.OAuthClientID), ConfiguredScopes: normalizedOAuthScopes(binding.OAuthScopes),
		Expiry: token.Expiry, ClientID: config.ClientID, ClientSecret: config.ClientSecret, AuthURL: config.Endpoint.AuthURL,
		TokenURL: config.Endpoint.TokenURL, RedirectURL: config.RedirectURL, Scopes: append([]string(nil), config.Scopes...), Resource: resource,
		Issuer: issuer, UpdatedAt: time.Now(),
	})
}

func (manager *mcpOAuthManager) persistExistingToken(credential store.MCPOAuthCredential, config *oauth2.Config, token *oauth2.Token) error {
	if token == nil || strings.TrimSpace(token.AccessToken) == "" {
		return errors.New("OAuth token 为空")
	}
	credential.AccessToken = token.AccessToken
	if strings.TrimSpace(token.RefreshToken) != "" {
		credential.RefreshToken = token.RefreshToken
	}
	credential.TokenType = token.TokenType
	credential.Expiry = token.Expiry
	credential.ClientID = config.ClientID
	credential.ClientSecret = config.ClientSecret
	credential.AuthURL = config.Endpoint.AuthURL
	credential.TokenURL = config.Endpoint.TokenURL
	credential.RedirectURL = config.RedirectURL
	credential.Scopes = append([]string(nil), config.Scopes...)
	credential.UpdatedAt = time.Now()
	return manager.server.store.SaveMCPOAuthCredential(credential)
}

func normalizedOAuthScopes(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func oauthCredentialMatchesConfig(credential store.MCPOAuthCredential, config store.MCPConfig) bool {
	return strings.TrimSpace(credential.Endpoint) != "" &&
		strings.TrimSpace(credential.Endpoint) == strings.TrimSpace(config.Endpoint) &&
		strings.TrimSpace(credential.RedirectURL) == strings.TrimSpace(config.OAuthCallbackURL) &&
		strings.TrimSpace(credential.ConfiguredClientID) == strings.TrimSpace(config.OAuthClientID) &&
		slices.Equal(normalizedOAuthScopes(credential.ConfiguredScopes), normalizedOAuthScopes(config.OAuthScopes))
}

func oauthBindingChanged(previous, next store.MCPConfig) bool {
	if !strings.EqualFold(strings.TrimSpace(previous.AuthType), strings.TrimSpace(next.AuthType)) {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(next.AuthType), "oauth") {
		return false
	}
	return strings.TrimSpace(previous.Endpoint) != strings.TrimSpace(next.Endpoint) ||
		strings.TrimSpace(previous.OAuthClientID) != strings.TrimSpace(next.OAuthClientID) ||
		strings.TrimSpace(previous.OAuthCallbackURL) != strings.TrimSpace(next.OAuthCallbackURL) ||
		previous.OAuthCallbackPort != next.OAuthCallbackPort ||
		!slices.Equal(normalizedOAuthScopes(previous.OAuthScopes), normalizedOAuthScopes(next.OAuthScopes))
}

type storedOAuthHandler struct {
	tokenSource oauth2.TokenSource
}

func (handler *storedOAuthHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return handler.tokenSource, nil
}

func (handler *storedOAuthHandler) Authorize(_ context.Context, _ *http.Request, response *http.Response) error {
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	return errors.New("OAuth 凭证缺失或已失效，请在设置页重新授权此 MCP")
}

func (manager *mcpOAuthManager) runtimeHandler(config store.MCPConfig) (auth.OAuthHandler, error) {
	credential, err := manager.server.store.GetMCPOAuthCredential(config.ID)
	if err != nil {
		return nil, err
	}
	if credential == nil || !oauthCredentialMatchesConfig(*credential, config) {
		return &storedOAuthHandler{}, nil
	}
	tokenSource, err := manager.initialTokenSource(credential)
	if err != nil {
		return nil, err
	}
	return &storedOAuthHandler{tokenSource: tokenSource}, nil
}

type persistedTokenSource struct {
	base    oauth2.TokenSource
	persist func(*oauth2.Token) error
}

type oauthResourceTransport struct {
	base     http.RoundTripper
	tokenURL string
	resource string
}

func (transport oauthResourceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	if cloned.URL.String() == transport.tokenURL && cloned.URL.Query().Get("resource") == "" {
		query := cloned.URL.Query()
		query.Set("resource", transport.resource)
		cloned.URL.RawQuery = query.Encode()
	}
	return transport.base.RoundTrip(cloned)
}

func tokenSourceWithResource(ctx context.Context, config *oauth2.Config, token *oauth2.Token, resource string) oauth2.TokenSource {
	if strings.TrimSpace(resource) == "" || strings.TrimSpace(config.Endpoint.TokenURL) == "" {
		return config.TokenSource(ctx, token)
	}
	client := &http.Client{Timeout: 90 * time.Second, Transport: oauthResourceTransport{base: http.DefaultTransport, tokenURL: config.Endpoint.TokenURL, resource: resource}}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, client)
	return config.TokenSource(ctx, token)
}

func (source *persistedTokenSource) Token() (*oauth2.Token, error) {
	token, err := source.base.Token()
	if err != nil {
		return nil, err
	}
	if source.persist != nil {
		if persistErr := source.persist(token); persistErr != nil {
			return nil, persistErr
		}
	}
	return token, nil
}

func (manager *mcpOAuthManager) complete(id string, callback mcpOAuthCallback) bool {
	manager.mu.Lock()
	flow := manager.byState[callback.State]
	if flow == nil || (id != "" && flow.mcpID != id) {
		manager.mu.Unlock()
		return false
	}
	delete(manager.byState, callback.State)
	manager.mu.Unlock()
	select {
	case flow.callbackCh <- callback:
		return true
	case <-flow.ctx.Done():
		return false
	}
}

func (manager *mcpOAuthManager) status(id string) (mcpOAuthStatus, error) {
	credential, err := manager.server.store.GetMCPOAuthCredential(id)
	if err != nil {
		return mcpOAuthStatus{}, err
	}
	config, err := manager.mcpConfig(id)
	if err != nil {
		return mcpOAuthStatus{}, err
	}
	status := mcpOAuthStatus{Runtime: mcpOAuthRuntimeBeaver, Message: "尚未授权"}
	if credential != nil && config != nil && oauthCredentialMatchesConfig(*credential, *config) && (credential.AccessToken != "" || credential.RefreshToken != "") {
		status.Authorized = true
		status.Message = "OAuth 凭证已保存，运行时会在过期时自动刷新"
		if !credential.Expiry.IsZero() {
			expiry := credential.Expiry
			status.ExpiresAt = &expiry
		}
	}
	manager.mu.Lock()
	if flow := manager.active[id]; flow != nil {
		status.Pending = true
		flow.mu.Lock()
		status.AuthorizationURL = flow.authorizationURL
		flow.mu.Unlock()
	}
	manager.mu.Unlock()
	return status, nil
}

func (manager *mcpOAuthManager) codexStatus(parent context.Context, id string) (mcpOAuthStatus, error) {
	status := mcpOAuthStatus{Runtime: "codex", Message: "Codex 尚未授权此 MCP"}
	manager.mu.Lock()
	if flow := manager.codexActive[id]; flow != nil {
		status.Pending = true
		flow.mu.Lock()
		status.AuthorizationURL = flow.authorizationURL
		flow.mu.Unlock()
		manager.mu.Unlock()
		status.Message = "等待浏览器完成 Codex OAuth 授权"
		return status, nil
	}
	manager.mu.Unlock()
	config, err := manager.mcpConfig(id)
	if err != nil {
		return mcpOAuthStatus{}, err
	}
	if config == nil {
		return status, nil
	}
	if !config.Enabled {
		status.Message = "请先启用此 MCP，再查看 Codex OAuth 状态"
		return status, nil
	}
	detected := manager.server.detectCodex(parent)
	if !detected.Installed || !detected.AppServerAvailable {
		status.Message = detected.Message
		return status, nil
	}
	environment, err := manager.server.codexEnvironment()
	if err != nil {
		return mcpOAuthStatus{}, err
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	authorized, detail, err := codexruntime.OAuthStatus(ctx, codexruntime.Config{Path: detected.Path, Workspace: manager.server.env.Workspace(), Env: environment}, id)
	if err != nil {
		return mcpOAuthStatus{}, err
	}
	status.Authorized = authorized
	if authorized {
		status.Message = "Codex OAuth 凭证已保存"
	} else if detail != "" {
		status.Message = detail
	}
	return status, nil
}

func (manager *mcpOAuthManager) logout(id string) error {
	manager.mu.Lock()
	if flow := manager.active[id]; flow != nil {
		flow.cancel()
	}
	manager.mu.Unlock()
	return manager.server.store.DeleteMCPOAuthCredential(id)
}

func (manager *mcpOAuthManager) logoutCodex(parent context.Context, id string) error {
	manager.mu.Lock()
	if flow := manager.codexActive[id]; flow != nil {
		flow.cancel()
	}
	manager.mu.Unlock()
	if !manager.server.externalCapabilitySync {
		return nil
	}
	detected := manager.server.detectCodex(parent)
	if !detected.Installed {
		return nil
	}
	environment, err := manager.server.codexEnvironment()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	config := codexruntime.Config{Path: detected.Path, Workspace: manager.server.env.Workspace(), Env: environment}
	authorized, _, err := codexruntime.OAuthStatus(ctx, config, id)
	if err != nil || !authorized {
		return err
	}
	return codexruntime.OAuthLogout(ctx, config, id)
}

func (manager *mcpOAuthManager) mcpConfig(id string) (*store.MCPConfig, error) {
	configs, err := manager.server.store.ListMCPConfigs()
	if err != nil {
		return nil, err
	}
	for index := range configs {
		if configs[index].ID == id {
			value := configs[index]
			return &value, nil
		}
	}
	return nil, nil
}

func (manager *mcpOAuthManager) callbackProxyPort(id string) int {
	manager.mu.Lock()
	activeID := id
	if _, active := manager.codexActive[activeID]; !active && len(manager.codexActive) == 1 {
		for candidate := range manager.codexActive {
			activeID = candidate
		}
	}
	_, active := manager.codexActive[activeID]
	manager.mu.Unlock()
	if !active {
		return 0
	}
	configs, err := manager.server.store.ListMCPConfigs()
	if err != nil {
		return 0
	}
	for _, config := range configs {
		if config.ID == activeID && strings.EqualFold(config.AuthType, "oauth") && config.OAuthCallbackPort > 0 {
			return config.OAuthCallbackPort
		}
	}
	return 0
}

func (manager *mcpOAuthManager) proxyCallback(response http.ResponseWriter, request *http.Request, port int) {
	target, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		writeOAuthCallbackPage(response, false, "OAuth 回调代理地址无效")
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(response http.ResponseWriter, request *http.Request, proxyErr error) {
		writeOAuthCallbackPage(response, false, "Codex OAuth 回调暂时无法连接本机 app-server")
	}
	proxy.ServeHTTP(response, request)
}

func oauthRedirectURL(request *http.Request, config store.MCPConfig) string {
	if value := strings.TrimSpace(config.OAuthCallbackURL); value != "" {
		return value
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	} else if forwarded := strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	return scheme + "://" + request.Host + "/api/v1/mcp/oauth/callback"
}
