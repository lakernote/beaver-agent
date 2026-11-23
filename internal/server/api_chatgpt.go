package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lakernote/beaver-agent/internal/chatgpt"
	"github.com/lakernote/beaver-agent/internal/redact"
	"github.com/lakernote/beaver-agent/internal/store"
)

type pendingChatGPTLogin struct {
	login      *chatgpt.Login
	webSession string
	accountID  string
	provider   string
}

var errCodexLocalBrowser = errors.New("请在运行 Beaver 的电脑上通过 127.0.0.1 或 localhost 打开页面，使用 Codex 登录的订阅配置")

type chatGPTAccounts interface {
	Accounts() ([]chatgpt.Account, error)
	Account(string) (chatgpt.Account, error)
	Models(context.Context, string) ([]chatgpt.Model, error)
	SignOut(context.Context, string) (bool, error)
}

func chatGPTProvider(request *http.Request) string {
	if provider := request.URL.Query().Get("provider"); provider != "" {
		return provider
	}
	return store.ChatGPTSubscriptionProvider
}

func (server *Server) chatGPTAccounts(provider string) (chatGPTAccounts, error) {
	switch provider {
	case store.ChatGPTCodexProvider:
		if server.chatGPTCodex != nil {
			return server.chatGPTCodex, nil
		}
	case store.ChatGPTSubscriptionProvider:
		if server.chatGPT != nil {
			return server.chatGPT, nil
		}
	}
	return nil, chatgpt.ErrAccountMissing
}

type chatGPTLoginView struct {
	ID           string           `json:"id"`
	AuthorizeURL string           `json:"authorizeUrl,omitempty"`
	Status       string           `json:"status"`
	Account      *chatgpt.Account `json:"account,omitempty"`
	Message      string           `json:"message,omitempty"`
}

func (server *Server) listChatGPTAccounts(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	client, err := server.chatGPTAccounts(chatGPTProvider(request))
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	accounts, err := client.Accounts()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	for index := range accounts {
		accounts[index] = publicChatGPTAccount(accounts[index])
	}
	writeJSON(response, http.StatusOK, accounts)
}

func (server *Server) listChatGPTModels(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	client, err := server.chatGPTAccounts(chatGPTProvider(request))
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	models, err := client.Models(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(response, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, models)
}

func (server *Server) signOutChatGPT(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	provider := chatGPTProvider(request)
	client, err := server.chatGPTAccounts(provider)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	id := request.PathValue("id")
	server.chatGPTLoginMu.Lock()
	if flow := server.chatGPTLogin; flow != nil && flow.provider == provider && flow.accountID == id {
		flow.login.Cancel()
	}
	server.chatGPTLoginMu.Unlock()
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	revoked, err := client.SignOut(ctx, id)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"revoked": revoked})
}

func (server *Server) startChatGPTLogin(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	// 由同一台机器上的浏览器访问 OpenAI OAuth 的 loopback 回调。
	if !localChatGPTBrowser(request) {
		writeError(response, http.StatusBadRequest, "请在运行 Beaver 的机器上用浏览器打开本机地址，再连接 ChatGPT")
		return
	}
	provider := chatGPTProvider(request)
	if _, err := server.chatGPTAccounts(provider); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	var input struct {
		AccountID string `json:"accountId"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	server.chatGPTLoginMu.Lock()
	defer server.chatGPTLoginMu.Unlock()
	id := strings.TrimSpace(input.AccountID)
	if flow := server.chatGPTLogin; flow != nil {
		flow.login.Cancel()
		select {
		case <-flow.login.Done():
		case <-request.Context().Done():
			return
		}
	}
	stillAllowed := func() bool {
		return !server.authEnabled || server.isAuthenticated(request)
	}
	var login *chatgpt.Login
	var err error
	if provider == store.ChatGPTCodexProvider {
		login, err = server.chatGPTCodex.StartLogin(request.Context(), server.ctx, id, stillAllowed)
	} else {
		login, err = server.chatGPT.StartLogin(server.ctx, id, stillAllowed)
	}
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	server.chatGPTLogin = &pendingChatGPTLogin{login: login, webSession: chatGPTWebSession(request), accountID: id, provider: provider}
	writeJSON(response, http.StatusAccepted, chatGPTLoginView{ID: login.ID, Status: "pending", AuthorizeURL: login.URL})
}

func (server *Server) pendingChatGPTLogin(request *http.Request) *chatgpt.Login {
	server.chatGPTLoginMu.Lock()
	defer server.chatGPTLoginMu.Unlock()
	flow := server.chatGPTLogin
	if flow == nil || flow.provider != chatGPTProvider(request) || flow.login.ID != request.PathValue("id") || flow.webSession != chatGPTWebSession(request) {
		return nil
	}
	return flow.login
}

func (server *Server) getChatGPTLogin(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	login := server.pendingChatGPTLogin(request)
	if login == nil {
		writeError(response, http.StatusNotFound, "找不到这次 ChatGPT 授权，请重新开始")
		return
	}
	view := chatGPTLoginView{ID: login.ID, Status: "pending"}
	select {
	case <-login.Done():
		account, err := login.Result()
		if err == nil {
			account = publicChatGPTAccount(account)
			view.Status, view.Account = "complete", &account
		} else {
			view.Status, view.Message = "failed", redact.Sanitize(err.Error())
		}
	default:
	}
	writeJSON(response, http.StatusOK, view)
}

func publicChatGPTAccount(account chatgpt.Account) chatgpt.Account {
	name, domain, valid := strings.Cut(strings.TrimSpace(account.Email), "@")
	account.Email = ""
	if valid && name != "" && domain != "" {
		first, _ := utf8.DecodeRuneInString(name)
		host, _ := utf8.DecodeRuneInString(domain)
		account.Email = string(first) + "…@" + string(host) + "…"
	}
	return account
}

func (server *Server) cancelChatGPTLogin(response http.ResponseWriter, request *http.Request) {
	if login := server.pendingChatGPTLogin(request); login != nil {
		login.Cancel()
	}
	response.WriteHeader(http.StatusNoContent)
}

func chatGPTWebSession(request *http.Request) string {
	if cookie, err := request.Cookie(authCookieName); err == nil {
		return cookie.Value
	}
	return ""
}

func localChatGPTBrowser(request *http.Request) bool {
	remote, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil || !net.ParseIP(remote).IsLoopback() {
		return false
	}
	host, _, err := net.SplitHostPort(request.Host)
	if err != nil {
		host = strings.Trim(request.Host, "[]")
	}
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func localCodexBrowser(request *http.Request) bool {
	// A wildcard listener also accepts loopback clients. A proxy's loopback
	// hop does not make its forwarded request local to Beaver.
	return localChatGPTBrowser(request) &&
		len(request.Header.Values("Forwarded")) == 0 &&
		len(request.Header.Values("X-Forwarded-For")) == 0 &&
		len(request.Header.Values("X-Forwarded-Host")) == 0 &&
		len(request.Header.Values("X-Real-IP")) == 0
}

func allowCodexBrowser(response http.ResponseWriter, request *http.Request, usesCodexSubscription bool) bool {
	if usesCodexSubscription && !localCodexBrowser(request) {
		response.Header().Set("Cache-Control", "no-store")
		writeError(response, http.StatusForbidden, errCodexLocalBrowser.Error())
		return false
	}
	return true
}

func withChatGPTAccountAccess(handler http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if allowCodexBrowser(response, request, chatGPTProvider(request) == store.ChatGPTCodexProvider) {
			handler(response, request)
		}
	}
}

func (server *Server) requireChatGPTAccount(settings store.ModelSettings) error {
	if !settings.IsChatGPTSubscription() && !settings.IsChatGPTCodex() {
		return nil
	}
	client, err := server.chatGPTAccounts(strings.ToLower(strings.TrimSpace(settings.Provider)))
	if err != nil {
		return err
	}
	account, err := client.Account(settings.ChatGPTAccountID)
	if err != nil {
		return err
	}
	if !account.SignedIn {
		return chatgpt.ErrSignInRequired
	}
	if !account.PlanAuthorized {
		return chatgpt.ErrPlanConsent
	}
	return nil
}

func checkChatGPTChannel(settings store.ModelSettings, channel store.Channel) error {
	if settings.IsChatGPTSubscription() && channel == store.ChannelWeixin {
		return errors.New("微信会话需要独立的模型配置。请在 Beaver 里将新会话默认设为其他模型")
	}
	return nil
}
