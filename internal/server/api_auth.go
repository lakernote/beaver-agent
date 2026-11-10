package server

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	loginMaxFailures = 5
	loginWindow      = 10 * time.Minute
)

// loginLimiter 按来源 IP 限制连续失败次数，防止暴力猜测密码。
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

func (limiter *loginLimiter) prune(key string, now time.Time) []time.Time {
	kept := limiter.failures[key][:0]
	for _, at := range limiter.failures[key] {
		if now.Sub(at) < loginWindow {
			kept = append(kept, at)
		}
	}
	if len(kept) == 0 {
		delete(limiter.failures, key)
	} else {
		limiter.failures[key] = kept
	}
	return kept
}

// retryAfter 返回仍需等待的时间；0 表示允许尝试。
func (limiter *loginLimiter) retryAfter(key string, now time.Time) time.Duration {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	kept := limiter.prune(key, now)
	if len(kept) < loginMaxFailures {
		return 0
	}
	return loginWindow - now.Sub(kept[0])
}

func (limiter *loginLimiter) fail(key string, now time.Time) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.failures == nil {
		limiter.failures = map[string][]time.Time{}
	}
	limiter.prune(key, now)
	limiter.failures[key] = append(limiter.failures[key], now)
}

func (limiter *loginLimiter) reset(key string) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	delete(limiter.failures, key)
}

func clientKey(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return request.RemoteAddr
	}
	return host
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type passwordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (server *Server) login(response http.ResponseWriter, request *http.Request) {
	var input loginRequest
	if !decodeJSON(response, request, &input) {
		return
	}
	if strings.TrimSpace(input.Username) == "" || input.Password == "" {
		writeError(response, http.StatusBadRequest, "请输入用户名和密码")
		return
	}
	key := clientKey(request)
	if wait := server.logins.retryAfter(key, time.Now()); wait > 0 {
		response.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(response, http.StatusTooManyRequests, "登录失败次数过多，请 "+strconv.Itoa(int(wait.Minutes())+1)+" 分钟后再试")
		return
	}
	valid, err := server.store.Authenticate(input.Username, input.Password)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "登录校验失败")
		return
	}
	if !valid {
		server.logins.fail(key, time.Now())
		writeError(response, http.StatusUnauthorized, "用户名或密码不正确")
		return
	}
	server.logins.reset(key)
	if !server.createAuthSession(response, request) {
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"authenticated": true, "username": strings.TrimSpace(input.Username)})
}

func (server *Server) logout(response http.ResponseWriter, request *http.Request) {
	server.clearAuthSession(response, request)
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) me(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if !server.isAuthenticated(request) {
		server.clearAuthSession(response, request)
		writeJSON(response, http.StatusOK, map[string]any{"authenticated": false, "username": ""})
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"authenticated": true, "username": "admin", "passwordGenerated": server.store.PasswordIsGenerated()})
}

func (server *Server) changePassword(response http.ResponseWriter, request *http.Request) {
	var input passwordRequest
	if !decodeJSON(response, request, &input) {
		return
	}
	if len(input.NewPassword) < 8 {
		writeError(response, http.StatusBadRequest, "新密码至少需要 8 个字符")
		return
	}
	if err := server.store.ChangePassword("admin", input.CurrentPassword, input.NewPassword); err != nil {
		if err.Error() == "当前密码不正确" {
			writeError(response, http.StatusUnauthorized, err.Error())
			return
		}
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	server.authMu.Lock()
	server.sessions = make(map[string]authSession)
	server.authMu.Unlock()
	server.clearAuthSession(response, request)
	writeJSON(response, http.StatusOK, map[string]any{"authenticated": false, "message": "密码已修改，请重新登录"})
}
