// Package server 组装 Beaver 的 HTTP API、单 Agent 运行时和静态页面。
//
// 这里是应用层，不是 Agent 核心。它负责 SQLite、配置和进程生命周期；核心循环
// 位于 internal/agent，内置能力位于 internal/builtin，彼此通过很小的接口连接。
//
// 文件按前缀分组，找代码时先看前缀：
//
//	api_*.go            HTTP 接口（会话、模型、项目、MCP、微信、定时任务……）
//	routes.go           路由表
//	turn_*.go           一轮对话的排队（turn_queue）与执行分发（turn_execution）
//	beaver_runtime.go   自研 Runner 的一轮运行；codex_*.go 是 Codex 的对应实现
//	context_*.go        上下文：Token 预算（context_budget）、检查点压缩（context_compaction）、页面账本
//	trace_observer.go   把 Runner 事件写成 Trace
//	task_*.go           进程内任务状态（task_manager）与并发/资源调度（task_scheduler）
//	weixin_*.go         微信入口：收件、命令、提交任务、回复、登录、媒体
//	automation_*.go     定时任务；retention_scheduler.go 是数据清理
package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lakernote/beaver-agent/internal/agent/modeladapter"
	"github.com/lakernote/beaver-agent/internal/appenv"
	"github.com/lakernote/beaver-agent/internal/codexruntime"
	"github.com/lakernote/beaver-agent/internal/store"
	"github.com/lakernote/beaver-agent/internal/weixin"
)

type Server struct {
	store  *store.Store
	assets fs.FS
	env    *appenv.Environment
	mux    *http.ServeMux
	// modelAdapters is owned by this application instance. Keeping the registry
	// here avoids package-global provider state and makes server construction
	// explicit and testable.
	modelAdapters *modeladapter.Registry

	ctx          context.Context
	cancel       context.CancelFunc
	lifecycleMu  sync.Mutex
	stopping     bool
	wait         sync.WaitGroup
	scheduler    *taskScheduler
	tasks        *taskManager
	weixin       *weixinManager
	codexEnvMu   sync.RWMutex
	retentionMu  sync.Mutex
	codexEnv     map[string]string
	codexEnvErr  error
	authMu       sync.Mutex
	authSessions map[string]authSession
	logins       loginLimiter
	authEnabled  bool
	// externalCapabilitySync writes the shared catalog to the service user's
	// Codex config/discovery directories. Tests keep it off to remain hermetic.
	externalCapabilitySync bool
}

const authCookieName = "beaver_session"
const authenticationRequiredCode = "authentication_required"

type authSession struct {
	ExpiresAt time.Time
}

type serverOptions struct {
	authEnabled            bool
	externalCapabilitySync bool
	modelAdapters          *modeladapter.Registry
}

func New(database *store.Store, assets fs.FS, environment *appenv.Environment) (*Server, error) {
	return newServer(database, assets, environment, serverOptions{authEnabled: true, externalCapabilitySync: true})
}

func newServer(database *store.Store, assets fs.FS, environment *appenv.Environment, options serverOptions) (*Server, error) {
	ctx, cancel := context.WithCancel(context.Background())
	codexEnv := map[string]string{}
	var codexEnvErr error
	if options.externalCapabilitySync {
		codexEnv, codexEnvErr = codexruntime.LoadManagedEnvironment()
		if codexEnv == nil {
			codexEnv = map[string]string{}
		}
	}
	modelAdapters := options.modelAdapters
	if modelAdapters == nil {
		modelAdapters = modeladapter.NewRegistry()
	}
	runtimeSettings, err := database.GetRuntimeSettings()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("load runtime settings: %w", err)
	}
	server := &Server{
		store: database, assets: assets, env: environment, mux: http.NewServeMux(),
		modelAdapters: modelAdapters,
		ctx:           ctx, cancel: cancel, scheduler: newTaskScheduler(runtimeSettings.MaxConcurrentTasks),
		tasks: newTaskManager(), codexEnv: codexEnv, codexEnvErr: codexEnvErr, authSessions: make(map[string]authSession),
		authEnabled: options.authEnabled, externalCapabilitySync: options.externalCapabilitySync,
	}
	server.routes()
	if err := server.resumeQueuedSessions(); err != nil {
		cancel()
		return nil, fmt.Errorf("resume queued sessions: %w", err)
	}
	server.weixin = newWeixinManager(server, weixin.NewHTTPGateway(nil))
	server.weixin.start()
	server.startAutomationScheduler()
	server.startRetentionScheduler()
	return server, nil
}

func (server *Server) codexEnvironment() ([]string, error) {
	return server.codexEnvironmentWith(nil)
}

func (server *Server) codexEnvironmentWith(extra map[string]string) ([]string, error) {
	server.codexEnvMu.RLock()
	if server.codexEnvErr != nil {
		err := server.codexEnvErr
		server.codexEnvMu.RUnlock()
		return nil, fmt.Errorf("加载 Codex API Key: %w", err)
	}
	values := make(map[string]string, len(server.codexEnv)+len(extra))
	for key, value := range server.codexEnv {
		values[key] = value
	}
	server.codexEnvMu.RUnlock()
	for key, value := range extra {
		values[key] = value
	}
	return server.env.Environ(values), nil
}

func (server *Server) reloadCodexEnvironment() error {
	values, err := codexruntime.LoadManagedEnvironment()
	if err != nil {
		server.codexEnvMu.Lock()
		server.codexEnvErr = err
		server.codexEnvMu.Unlock()
		return err
	}
	server.codexEnvMu.Lock()
	server.codexEnv = values
	server.codexEnvErr = nil
	server.codexEnvMu.Unlock()
	return nil
}

func (server *Server) Handler() http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		response.Header().Set("Referrer-Policy", "no-referrer")
		if server.isStopping() {
			response.Header().Set("Connection", "close")
			writeError(response, http.StatusServiceUnavailable, "服务正在停止")
			return
		}
		if server.requiresAuthentication(request) && !server.isAuthenticated(request) {
			response.Header().Set("Cache-Control", "no-store")
			writeCodedError(response, http.StatusUnauthorized, authenticationRequiredCode, "需要登录")
			return
		}
		server.mux.ServeHTTP(response, request)
	})
}

func (server *Server) requiresAuthentication(request *http.Request) bool {
	if !server.authEnabled {
		return false
	}
	if request.URL.Path == "/api/v1/auth/login" || request.URL.Path == "/api/v1/auth/logout" || request.URL.Path == "/api/v1/auth/me" || request.URL.Path == "/api/v1/health" {
		return false
	}
	return strings.HasPrefix(request.URL.Path, "/api/v1/")
}

func (server *Server) isAuthenticated(request *http.Request) bool {
	cookie, err := request.Cookie(authCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	server.authMu.Lock()
	defer server.authMu.Unlock()
	session, ok := server.authSessions[cookie.Value]
	if !ok || time.Now().After(session.ExpiresAt) {
		delete(server.authSessions, cookie.Value)
		return false
	}
	return true
}

func (server *Server) createAuthSession(response http.ResponseWriter, request *http.Request) bool {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		writeError(response, http.StatusInternalServerError, "无法创建登录会话")
		return false
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	expiresAt := time.Now().Add(12 * time.Hour)
	server.authMu.Lock()
	now := time.Now()
	for value, session := range server.authSessions {
		if now.After(session.ExpiresAt) {
			delete(server.authSessions, value)
		}
	}
	server.authSessions[token] = authSession{ExpiresAt: expiresAt}
	server.authMu.Unlock()
	http.SetCookie(response, &http.Cookie{Name: authCookieName, Value: token, Path: "/", Expires: expiresAt, MaxAge: 12 * 60 * 60, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: request.TLS != nil})
	return true
}

func (server *Server) clearAuthSession(response http.ResponseWriter, request *http.Request) {
	if cookie, err := request.Cookie(authCookieName); err == nil {
		server.authMu.Lock()
		delete(server.authSessions, cookie.Value)
		server.authMu.Unlock()
	}
	http.SetCookie(response, &http.Cookie{Name: authCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: request.TLS != nil})
}

func (server *Server) beginBackground() bool {
	server.lifecycleMu.Lock()
	defer server.lifecycleMu.Unlock()
	if server.stopping {
		return false
	}
	server.wait.Add(1)
	return true
}

func (server *Server) completeBackground() {
	server.wait.Done()
}

func (server *Server) isStopping() bool {
	server.lifecycleMu.Lock()
	defer server.lifecycleMu.Unlock()
	return server.stopping
}

// BeginShutdown rejects new work and broadcasts cancellation without waiting.
func (server *Server) BeginShutdown() {
	server.lifecycleMu.Lock()
	if !server.stopping {
		server.stopping = true
		server.cancel()
	}
	server.lifecycleMu.Unlock()
}

func (server *Server) Shutdown(ctx context.Context) error {
	server.BeginShutdown()
	done := make(chan struct{})
	go func() { server.wait.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
