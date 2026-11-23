package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const callbackPath = "/auth/callback"

// Login represents one browser-based authorization, bound to a loopback
// listener and a fresh PKCE verifier. No credentials appear in its result.
type Login struct {
	ID  string
	URL string

	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once
	result Account
	err    error
}

func (login *Login) Done() <-chan struct{} { return login.done }
func (login *Login) Cancel()               { login.cancel() }

func (login *Login) Result() (Account, error) {
	<-login.done
	return login.result, login.err
}

func (login *Login) complete(account Account, err error) {
	login.once.Do(func() {
		login.result, login.err = account, err
		close(login.done)
	})
}

type challenge struct {
	state, nonce, verifier string
}

func randomSecret() string {
	var value [32]byte
	rand.Read(value[:])
	return base64.RawURLEncoding.EncodeToString(value[:])
}

func newChallenge() challenge {
	return challenge{state: randomSecret(), nonce: randomSecret(), verifier: randomSecret()}
}

func authorizationURL(hostID, redirectURI, clientID string, value challenge, askConsent bool) string {
	sum := sha256.Sum256([]byte(value.verifier))
	query := url.Values{
		"client_id": {clientID}, "ext_agent_host_id": {hostID}, "response_type": {"code"},
		"redirect_uri": {redirectURI}, "resource": {APIBaseURL}, "scope": {scopes},
		"state": {value.state}, "nonce": {value.nonce}, "code_challenge_method": {"S256"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])},
	}
	if clientID == dynamicClientID {
		query.Set("agent_name_hint", "Beaver")
	} else if askConsent {
		query.Set("prompt", "consent")
	}
	return authorizeURL + "?" + query.Encode()
}

func (client *Client) StartLogin(parent context.Context, id string, stillAllowed func() bool) (*Login, error) {
	var previous credential
	if id != "" {
		var err error
		previous, err = client.load(id)
		if err != nil {
			return nil, err
		}
	}
	hostID, err := client.hostID()
	if err != nil {
		return nil, fmt.Errorf("准备 ChatGPT 登录: %w", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("启动 ChatGPT 本机授权回调: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	host := listener.Addr().String()
	redirectURI := "http://" + host + callbackPath
	login := &Login{ID: randomSecret(), done: make(chan struct{}), cancel: cancel}
	clientID := previous.ClientID
	if clientID == "" {
		clientID = dynamicClientID
	}
	current := newChallenge()
	login.URL = authorizationURL(hostID, redirectURI, clientID, current, !previous.account().PlanAuthorized)

	var mu sync.Mutex
	var handling, retried bool
	callback := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if request.Method != http.MethodGet || request.Host != host || request.URL.Path != callbackPath {
			http.NotFound(response, request)
			return
		}
		query, parseErr := url.ParseQuery(request.URL.RawQuery)
		mu.Lock()
		if parseErr != nil || len(query["state"]) != 1 || subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(current.state)) != 1 {
			mu.Unlock()
			http.Error(response, "ChatGPT 登录验证已失效，请回到 Beaver。", http.StatusBadRequest)
			return
		}
		if handling || ctx.Err() != nil {
			mu.Unlock()
			http.Error(response, "这次授权已处理，请回到 Beaver。", http.StatusConflict)
			return
		}
		handling = true
		selected, expected := clientID, current
		mu.Unlock()

		account, issued, err := client.handleCallback(ctx, query, selected, previous, expected, redirectURI, stillAllowed)
		var failure *tokenError
		if errors.As(err, &failure) && failure.code == "invalid_grant" && validClientID(issued) {
			mu.Lock()
			if !retried && ctx.Err() == nil {
				retried, handling, clientID, current = true, false, issued, newChallenge()
				next := authorizationURL(hostID, redirectURI, issued, current, false)
				mu.Unlock()
				http.Redirect(response, request, next, http.StatusSeeOther)
				return
			}
			mu.Unlock()
		}
		if err != nil {
			http.Error(response, "ChatGPT 授权未完成，请回到 Beaver 查看详情。", http.StatusBadRequest)
		} else {
			fmt.Fprintln(response, "ChatGPT 登录已完成，可以关闭此页面并返回 Beaver。")
		}
		login.complete(account, err)
	})
	server := &http.Server{Handler: callback, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, MaxHeaderBytes: 32 * 1024}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			login.complete(Account{}, errors.New("ChatGPT 本机授权回调已停止"))
		}
	}()
	go func() {
		defer cancel()
		select {
		case <-ctx.Done():
			login.complete(Account{}, errors.New("ChatGPT 登录已取消或超时，请重新开始"))
		case <-login.Done():
		}
		shutdown, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}()
	return login, nil
}

func (client *Client) handleCallback(ctx context.Context, query url.Values, selected string, previous credential, expected challenge, redirectURI string, stillAllowed func() bool) (Account, string, error) {
	if query.Get("error") != "" {
		return Account{}, "", errors.New("ChatGPT 授权未获同意，请重新授权")
	}
	issued := query.Get("client_id")
	if selected != dynamicClientID {
		if issued != "" && issued != selected {
			return Account{}, "", errors.New("ChatGPT 返回的授权客户端与请求不匹配")
		}
		issued = selected
	}
	code := query.Get("code")
	if !validClientID(issued) || len(query["client_id"]) > 1 || code == "" || len(query["code"]) != 1 {
		return Account{}, "", errors.New("ChatGPT 返回的授权信息不完整，请重新授权")
	}
	account, err := client.finishLogin(ctx, previous, issued, code, expected.verifier, redirectURI, expected.nonce, stillAllowed)
	return account, issued, err
}
