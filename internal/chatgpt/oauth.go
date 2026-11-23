package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/coreos/go-oidc/v3/oidc"
)

const (
	issuer          = "https://auth.openai.com"
	authorizeURL    = issuer + "/api/accounts/authorize"
	tokenURL        = issuer + "/api/accounts/oauth/token"
	revocationURL   = issuer + "/api/accounts/oauth/revoke"
	dynamicClientID = "dynamic_agent_client"
	scopes          = "openid profile email offline_access resource.invoke " + planScope
)

func validClientID(id string) bool {
	if id == "" || len(id) > 256 || id == dynamicClientID {
		return false
	}
	for _, char := range id {
		if char > unicode.MaxASCII || !(unicode.IsLetter(char) || unicode.IsDigit(char) || char == '-' || char == '_') {
			return false
		}
	}
	return true
}

type tokenResponse struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	IDToken      string  `json:"id_token"`
	TokenType    string  `json:"token_type"`
	ExpiresIn    int64   `json:"expires_in"`
	Scope        *string `json:"scope"`
	Error        string  `json:"error"`
}

type tokenError struct {
	status int
	code   string
}

func (err *tokenError) Error() string {
	if err.status >= 500 {
		return "ChatGPT 授权服务暂不可用，请稍后再试"
	}
	return fmt.Sprintf("ChatGPT 授权服务拒绝了请求（HTTP %d），请重新连接", err.status)
}

func (err *tokenError) unusableRefresh() bool {
	switch err.code {
	case "invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused":
		return true
	default:
		return false
	}
}

func (client *Client) postForm(ctx context.Context, endpoint string, form url.Values) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	return client.http.Do(request)
}

func (client *Client) exchange(ctx context.Context, form url.Values) (tokenResponse, error) {
	form.Set("resource", APIBaseURL)
	response, err := client.postForm(ctx, tokenURL, form)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("无法连接 ChatGPT 授权服务: %w", err)
	}
	defer response.Body.Close()
	var tokens tokenResponse
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 128*1024)).Decode(&tokens)
	if response.StatusCode != http.StatusOK {
		return tokenResponse{}, &tokenError{status: response.StatusCode, code: tokens.Error}
	}
	if decodeErr != nil || tokens.AccessToken == "" || tokens.ExpiresIn <= 0 || tokens.ExpiresIn > int64((366*24*time.Hour)/time.Second) ||
		!strings.EqualFold(tokens.TokenType, "Bearer") {
		return tokenResponse{}, errors.New("ChatGPT 授权服务返回了不完整的凭据")
	}
	return tokens, nil
}

func (tokens tokenResponse) apply(value *credential) {
	value.AccessToken = tokens.AccessToken
	value.ExpiresAt = time.Now().UTC().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	if tokens.RefreshToken != "" {
		value.RefreshToken = tokens.RefreshToken
	}
	if tokens.Scope != nil {
		value.Scopes = *tokens.Scope
	}
}

func (client *Client) finishLogin(ctx context.Context, previous credential, issuedID, code, verifier, redirectURI, nonce string, stillAllowed func() bool) (Account, error) {
	tokens, err := client.exchange(ctx, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {issuedID}, "code": {code},
		"code_verifier": {verifier}, "redirect_uri": {redirectURI},
	})
	if err != nil {
		return Account{}, err
	}
	idToken, err := oidc.NewVerifier(issuer, client.keys, &oidc.Config{ClientID: issuedID}).Verify(ctx, tokens.IDToken)
	if err != nil || idToken.Subject == "" || idToken.Nonce != nonce ||
		(idToken.AccessTokenHash != "" && idToken.VerifyAccessToken(tokens.AccessToken) != nil) {
		return Account{}, errors.New("无法验证 ChatGPT 登录身份，请重新授权")
	}
	if previous.Subject != "" && previous.Subject != idToken.Subject {
		return Account{}, errors.New("ChatGPT 登录帐户不匹配，请选回原账号或添加新的连接")
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return Account{}, errors.New("无法读取 ChatGPT 登录身份")
	}
	value := credential{ClientID: issuedID, Subject: idToken.Subject, Email: claims.Email, IDToken: tokens.IDToken}
	tokens.apply(&value)

	unlock := client.lock(accountID(issuedID))
	defer unlock()
	if ctx.Err() != nil || (stillAllowed != nil && !stillAllowed()) {
		return Account{}, errors.New("Beaver 登录已失效，请重新登录后授权")
	}
	if err := client.save(value); err != nil {
		return Account{}, fmt.Errorf("保存 ChatGPT 授权: %w", err)
	}
	return value.account(), nil
}

func (client *Client) accessToken(ctx context.Context, id string) (string, error) {
	if len(id) != 32 {
		return "", ErrAccountMissing
	}
	unlock := client.lock(id)
	defer unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value, err := client.load(id)
	if err != nil {
		return "", err
	}
	if !value.account().SignedIn {
		return "", ErrSignInRequired
	}
	if !value.account().PlanAuthorized {
		return "", ErrPlanConsent
	}
	if time.Until(value.ExpiresAt) > 60*time.Second || (value.RefreshToken == "" && time.Now().Before(value.ExpiresAt)) {
		return value.AccessToken, nil
	}
	tokens, err := client.exchange(ctx, url.Values{
		"grant_type": {"refresh_token"}, "client_id": {value.ClientID}, "refresh_token": {value.RefreshToken},
	})
	if err != nil {
		var failure *tokenError
		if errors.As(err, &failure) && failure.unusableRefresh() {
			value.clearTokens()
			if saveErr := client.save(value); saveErr != nil {
				return "", fmt.Errorf("清除失效的 ChatGPT 授权: %w", saveErr)
			}
			return "", ErrSignInRequired
		}
		return "", err
	}
	tokens.apply(&value)
	// A refresh response can omit an ID token. Keep the verified identity from sign-in.
	if err := client.save(value); err != nil {
		return "", fmt.Errorf("保存更新后的 ChatGPT 授权: %w", err)
	}
	if !value.account().PlanAuthorized {
		return "", ErrPlanConsent
	}
	return value.AccessToken, nil
}

// SignOut clears local tokens even when ChatGPT cannot confirm remote revocation.
func (client *Client) SignOut(ctx context.Context, id string) (revoked bool, err error) {
	if len(id) != 32 {
		return false, ErrAccountMissing
	}
	unlock := client.lock(id)
	defer unlock()
	value, err := client.load(id)
	if err != nil {
		return false, err
	}
	revoked = value.RefreshToken == "" && value.AccessToken == ""
	if value.RefreshToken != "" {
		revoked = client.revoke(ctx, value)
	}
	value.clearTokens()
	return revoked, client.save(value)
}

func (client *Client) revoke(ctx context.Context, value credential) bool {
	form := url.Values{"client_id": {value.ClientID}, "token_type_hint": {"refresh_token"}, "token": {value.RefreshToken}}
	for attempt := 0; attempt < 2; attempt++ {
		response, err := client.postForm(ctx, revocationURL, form)
		if err == nil {
			response.Body.Close()
			if response.StatusCode < 500 {
				return response.StatusCode == http.StatusOK
			}
		}
		if attempt == 0 {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	return false
}

type Model struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

func (client *Client) Models(ctx context.Context, id string) ([]Model, error) {
	token, err := client.accessToken(ctx, id)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, APIBaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("无法读取 ChatGPT 模型列表: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return nil, ErrSignInRequired
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("无法读取此 ChatGPT 账号的模型列表（HTTP %d）", response.StatusCode)
	}
	var catalog struct {
		Models []struct {
			Model
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024)).Decode(&catalog); err != nil {
		return nil, errors.New("无法解析 ChatGPT 模型列表")
	}
	models := []Model{}
	for _, model := range catalog.Models {
		if model.Visibility == "list" && model.Slug != "" {
			models = append(models, model.Model)
		}
	}
	return models, nil
}

// InferenceClient sends a refreshed token only to the eligible official route.
func (client *Client) InferenceClient(id string, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: rejectRedirect, Transport: subscriptionTransport{client: client, id: id}}
}

type subscriptionTransport struct {
	client *Client
	id     string
}

func (transport subscriptionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodPost || request.URL.String() != APIBaseURL+"/responses" {
		return nil, errors.New("ChatGPT 订阅仅用于 OpenAI 官方 Responses API")
	}
	token, err := transport.client.accessToken(request.Context(), transport.id)
	if err != nil {
		return nil, err
	}
	outbound := request.Clone(request.Context())
	outbound.Header.Set("Authorization", "Bearer "+token)
	return http.DefaultTransport.RoundTrip(outbound)
}
