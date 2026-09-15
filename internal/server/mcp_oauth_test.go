package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lakernote/easy-agent/internal/appenv"
	"github.com/lakernote/easy-agent/internal/store"
	"golang.org/x/oauth2"
)

func TestMCPOAuthCallbackAcceptsFormPost(t *testing.T) {
	application, database := newMCPOAuthTestServer(t)
	defer application.Shutdown(context.Background())
	defer database.Close()

	flowContext, cancel := context.WithCancel(application.ctx)
	defer cancel()
	flow := &mcpOAuthFlow{mcpID: "gitlab", ctx: flowContext, cancel: cancel, callbackCh: make(chan mcpOAuthCallback, 1)}
	application.mcpOAuth.mu.Lock()
	application.mcpOAuth.byState["expected-state"] = flow
	application.mcpOAuth.mu.Unlock()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/oauth/callback", strings.NewReader("state=expected-state&code=authorization-code&iss=https%3A%2F%2Fissuer.example"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("form_post callback failed: HTTP %d body=%s", response.Code, response.Body.String())
	}
	select {
	case callback := <-flow.callbackCh:
		if callback.State != "expected-state" || callback.Code != "authorization-code" || callback.Iss != "https://issuer.example" {
			t.Fatalf("unexpected callback: %+v", callback)
		}
	default:
		t.Fatal("OAuth callback was not delivered to the active flow")
	}
}

func TestMCPOAuthStartRequiresEnabledMCP(t *testing.T) {
	application, database := newMCPOAuthTestServer(t)
	defer application.Shutdown(context.Background())
	defer database.Close()
	if err := database.SaveMCP(store.MCPConfig{ID: "gitlab", Name: "GitLab", Enabled: false, Transport: "http", Endpoint: "https://gitlab.example/mcp", AuthType: "oauth"}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/gitlab/oauth/start", nil)
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "请先启用") {
		t.Fatalf("disabled OAuth MCP should not start a flow: HTTP %d body=%s", response.Code, response.Body.String())
	}
}

func TestMCPOAuthCallbackProxyPreservesFormBodyAndUsesOnlyActiveCodexFlow(t *testing.T) {
	received := make(chan url.Values, 1)
	listener := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		received <- request.Form
		response.WriteHeader(http.StatusNoContent)
	}))
	defer listener.Close()
	parsed, err := url.Parse(listener.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	application, database := newMCPOAuthTestServer(t)
	defer application.Shutdown(context.Background())
	defer database.Close()
	for _, config := range []store.MCPConfig{
		{ID: "first", Name: "First", Transport: "http", Endpoint: "https://first.example/mcp", AuthType: "oauth", OAuthCallbackPort: 18765},
		{ID: "second", Name: "Second", Transport: "http", Endpoint: "https://second.example/mcp", AuthType: "oauth", OAuthCallbackPort: port},
	} {
		if err := database.SaveMCP(config); err != nil {
			t.Fatal(err)
		}
	}
	flowContext, cancel := context.WithCancel(application.ctx)
	defer cancel()
	application.mcpOAuth.mu.Lock()
	application.mcpOAuth.codexActive["second"] = &codexOAuthFlow{mcpID: "second", ctx: flowContext, cancel: cancel}
	application.mcpOAuth.mu.Unlock()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/first/oauth/callback/provider-id", strings.NewReader("state=codex-state&code=codex-code"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("proxied callback failed: HTTP %d body=%s", response.Code, response.Body.String())
	}
	select {
	case form := <-received:
		if form.Get("state") != "codex-state" || form.Get("code") != "codex-code" {
			t.Fatalf("proxied form body changed: %v", form)
		}
	default:
		t.Fatal("Codex callback proxy did not receive the request")
	}
}

func TestMCPClientConfigsRestoreOnlyMatchingOAuthCredential(t *testing.T) {
	application, database := newMCPOAuthTestServer(t)
	defer application.Shutdown(context.Background())
	defer database.Close()

	config := store.MCPConfig{
		ID: "gitlab", Name: "GitLab", Enabled: true, Transport: "http", Endpoint: "https://gitlab.example/mcp", AuthType: "oauth",
		OAuthClientID: "configured-client", OAuthCallbackURL: "https://agent.example/api/v1/mcp/oauth/callback", OAuthScopes: []string{"read", "write"},
	}
	if err := database.SaveMCP(config); err != nil {
		t.Fatal(err)
	}
	credential := store.MCPOAuthCredential{
		MCPID: config.ID, Endpoint: config.Endpoint, ConfiguredClientID: config.OAuthClientID, ConfiguredScopes: []string{"write", "read"},
		AccessToken: "saved-token", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour), ClientID: "effective-client",
		TokenURL: "https://gitlab.example/oauth/token", RedirectURL: config.OAuthCallbackURL,
	}
	if err := database.SaveMCPOAuthCredential(credential); err != nil {
		t.Fatal(err)
	}

	configs, err := application.mcpClientConfigs([]store.MCPConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	source, err := configs[0].OAuthHandler.TokenSource(context.Background())
	if err != nil || source == nil {
		t.Fatalf("matching credential was not restored: source=%v err=%v", source, err)
	}
	token, err := source.Token()
	if err != nil || token.AccessToken != "saved-token" {
		t.Fatalf("unexpected restored token: token=%+v err=%v", token, err)
	}

	config.Endpoint = "https://other.example/mcp"
	configs, err = application.mcpClientConfigs([]store.MCPConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	source, err = configs[0].OAuthHandler.TokenSource(context.Background())
	if err != nil || source != nil {
		t.Fatalf("credential bound to the old endpoint must not be reused: source=%v err=%v", source, err)
	}
}

func TestPersistExistingTokenKeepsRefreshTokenWhenProviderOmitsIt(t *testing.T) {
	application, database := newMCPOAuthTestServer(t)
	defer application.Shutdown(context.Background())
	defer database.Close()

	credential := store.MCPOAuthCredential{
		MCPID: "gitlab", Endpoint: "https://gitlab.example/mcp", AccessToken: "old-access",
		RefreshToken: "long-lived-refresh", TokenType: "Bearer", TokenURL: "https://gitlab.example/oauth/token",
		ClientID: "client", RedirectURL: "https://agent.example/callback",
	}
	if err := database.SaveMCP(store.MCPConfig{ID: credential.MCPID, Name: "GitLab", Transport: "http", Endpoint: credential.Endpoint, AuthType: "oauth"}); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveMCPOAuthCredential(credential); err != nil {
		t.Fatal(err)
	}
	config := &oauth2.Config{ClientID: "client", RedirectURL: credential.RedirectURL, Endpoint: oauth2.Endpoint{TokenURL: credential.TokenURL}}
	if err := application.mcpOAuth.persistExistingToken(credential, config, &oauth2.Token{AccessToken: "new-access", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	saved, err := database.GetMCPOAuthCredential("gitlab")
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.AccessToken != "new-access" || saved.RefreshToken != "long-lived-refresh" {
		t.Fatalf("refresh token was lost during refresh: %+v", saved)
	}
}

func TestSaveMCPInvalidatesOAuthCredentialWhenBindingChanges(t *testing.T) {
	application, database := newMCPOAuthTestServer(t)
	defer application.Shutdown(context.Background())
	defer database.Close()

	callbackURL := "https://agent.example/api/v1/mcp/oauth/callback"
	if err := database.SaveMCP(store.MCPConfig{ID: "gitlab", Name: "GitLab", Transport: "http", Endpoint: "https://old.example/mcp", AuthType: "oauth", OAuthCallbackURL: callbackURL, OAuthCallbackPort: 18765}); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveMCPOAuthCredential(store.MCPOAuthCredential{MCPID: "gitlab", Endpoint: "https://old.example/mcp", AccessToken: "old-token", RedirectURL: callbackURL}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPut, "/api/v1/mcp/gitlab", strings.NewReader(`{"id":"gitlab","name":"GitLab","enabled":false,"transport":"http","endpoint":"https://new.example/mcp","authType":"oauth","oauthCallbackUrl":"https://agent.example/api/v1/mcp/oauth/callback","oauthCallbackPort":18765}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("save failed: HTTP %d body=%s", response.Code, response.Body.String())
	}
	credential, err := database.GetMCPOAuthCredential("gitlab")
	if err != nil {
		t.Fatal(err)
	}
	if credential != nil {
		t.Fatalf("old OAuth credential survived endpoint change: %+v", credential)
	}
}

func newMCPOAuthTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "easyagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	environment, err := appenv.Open(appenv.Config{Home: filepath.Join(t.TempDir(), "home")})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	application := newTestServer(t, database, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, environment)
	return application, database
}
