package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lakernote/beaver-agent/internal/store"
)

func (server *Server) enqueueTurn(id, userMessage string, attachments []store.Attachment, model store.ModelSettings) error {
	if server.tasks.has(id) {
		return errors.New("上一条任务正在结束，请稍后再发送")
	}
	if err := server.requireVerifiedBeaver(model); err != nil {
		return err
	}
	now := time.Now()
	if err := server.sessionsRepository().EnqueueTurnWithSettings(id, model, store.Message{Role: "user", Content: userMessage, Attachments: attachments, ToolCalls: []store.ToolCall{}, CreatedAt: now}, now); err != nil {
		return err
	}
	return server.startQueuedTurn(id, model)
}

func (server *Server) startQueuedTurn(id string, model store.ModelSettings) error {
	if server.tasks.has(id) {
		return errors.New("任务已在当前进程队列中")
	}
	if !server.beginBackground() {
		return errors.New("服务正在停止")
	}
	taskContext, taskCancel := context.WithCancel(server.ctx)
	taskToken := newID()
	server.tasks.set(id, taskToken, taskCancel)
	go func() {
		defer server.completeBackground()
		defer server.tasks.clear(id, taskToken)
		defer taskCancel()
		session, loadErr := server.sessionsRepository().LoadSessionWindow(id, 1, 1)
		if loadErr != nil {
			_ = server.sessionsRepository().FailSession(id, loadErr, store.Usage{}, time.Now())
			server.recordAutomationSessionResult(id, store.AutomationFailed, loadErr.Error())
			return
		}
		resourceKeys := server.taskResourceKeys(session, model)
		if err := server.scheduler.acquire(taskContext, resourceKeys...); err != nil {
			// 服务停机时保留尚未开始的 queued 任务，下一次启动会恢复；用户
			// 主动停止则 CancelSession 已经把状态改成 canceled。
			if server.ctx.Err() == nil {
				_ = server.sessionsRepository().FailSession(id, err, store.Usage{}, time.Now())
				server.recordAutomationSessionResult(id, store.AutomationFailed, err.Error())
			}
			return
		}
		defer server.scheduler.release(resourceKeys...)
		if err := server.sessionsRepository().MarkRunning(id, time.Now()); err != nil {
			// 用户可能在任务刚获得执行槽时点击了停止，此时 canceled 状态应保留。
			_ = server.sessionsRepository().FailSession(id, err, store.Usage{}, time.Now())
			server.recordAutomationSessionResult(id, store.AutomationFailed, err.Error())
			return
		}
		server.recordAutomationSessionResult(id, store.AutomationRunning, "")
		server.tasks.setProgress(id, "正在准备运行时")
		usage := store.Usage{}
		runtimeSettings, settingsErr := server.store.GetRuntimeSettings()
		if settingsErr != nil {
			_ = server.sessionsRepository().FailSession(id, settingsErr, usage, time.Now())
			server.recordAutomationSessionResult(id, store.AutomationFailed, settingsErr.Error())
			return
		}
		// 整轮上限属于两个 Runtime 共用的调度层。Codex 仍会把同一个上限传给
		// app-server，以便它主动 interrupt；Beaver 则由这里取消整个循环。
		model.TurnTimeoutSeconds = runtimeSettings.TurnTimeoutSeconds
		turnContext, turnCancel := context.WithTimeout(taskContext, time.Duration(runtimeSettings.TurnTimeoutSeconds)*time.Second)
		defer turnCancel()
		if err := server.executeSessionTurn(turnContext, id, model, &usage); err != nil {
			if errors.Is(err, store.ErrSessionNotRunning) {
				current, _ := server.sessionsRepository().LoadSessionWindow(id, 1, 1)
				server.recordAutomationSessionResult(id, automationStatusForSession(current.Status), current.Error)
				return
			}
			if errors.Is(err, context.Canceled) && server.ctx.Err() != nil {
				err = errors.New("服务正在停止，任务已中断；恢复后请确认工作区状态，再重新发送")
			}
			if errors.Is(err, context.DeadlineExceeded) {
				err = errors.New("整轮任务超过配置的时间上限")
			}
			runtime := session.Runtime
			if runtime == "" {
				runtime = store.RuntimeBeaver
			}
			unknownReason := "本轮异常结束时工具没有返回确定终态；为避免重复副作用不会自动重放"
			if ledgerErr := server.sessionsRepository().MarkUnfinishedToolOperationsUnknown(id, runtime, session.UserTurnCount, unknownReason, time.Now()); ledgerErr != nil {
				err = fmt.Errorf("%v；收敛工具执行账本失败: %w", err, ledgerErr)
			}
			_ = server.sessionsRepository().FailSession(id, err, usage, time.Now())
			server.recordAutomationSessionResult(id, store.AutomationFailed, err.Error())
			return
		}
		server.recordAutomationSessionResult(id, store.AutomationCompleted, "")
	}()
	return nil
}

func (server *Server) recordAutomationSessionResult(sessionID string, status store.AutomationStatus, errorMessage string) {
	_ = server.store.RecordAutomationSessionResult(sessionID, status, errorMessage, time.Now())
}

func automationStatusForSession(status store.SessionStatus) store.AutomationStatus {
	switch status {
	case store.SessionQueued:
		return store.AutomationQueued
	case store.SessionRunning:
		return store.AutomationRunning
	case store.SessionPaused:
		return store.AutomationPaused
	case store.SessionCanceled:
		return store.AutomationCanceled
	case store.SessionIdle:
		return store.AutomationCompleted
	default:
		return store.AutomationFailed
	}
}

// taskConflictKey keeps worktrees parallel when the project only exposes the
// isolated repository. If a project adds any shared source folder, all of its
// sessions serialize because those extra roots are not worktree-isolated.
func (server *Server) taskConflictKey(session store.Session) string {
	if session.ProjectID == "" {
		return session.Workspace
	}
	project, err := server.store.GetProject(session.ProjectID)
	if err != nil {
		return session.Workspace
	}
	for _, directory := range project.Directories {
		path := filepath.Clean(directory)
		if path != filepath.Clean(session.SourceWorkspace) && path != filepath.Clean(session.Workspace) {
			return "project:" + session.ProjectID
		}
	}
	return session.Workspace
}

// taskResourceKeys 同时保护文件系统和本地推理资源。远程 Provider/Codex 仍受
// 全局并发控制但可以并行；同一个 Ollama 端点默认串行，避免单卡并发导致显存
// 抖动和首 Token 延迟恶化。
func (server *Server) taskResourceKeys(session store.Session, model store.ModelSettings) []string {
	keys := []string{server.taskConflictKey(session)}
	if model.Runtime != store.RuntimeCodex && model.IsOllama() {
		endpoint := strings.TrimRight(strings.ToLower(strings.TrimSpace(model.BaseURL)), "/")
		if endpoint == "" {
			endpoint = store.DefaultOllamaBaseURL
		}
		keys = append(keys, "model:ollama:"+endpoint)
	}
	return uniqueResourceKeys(keys)
}

func (server *Server) resumeQueuedSessions() error {
	queued, err := server.sessionsRepository().ListQueuedSessions()
	if err != nil {
		return err
	}
	for _, session := range queued {
		model, snapshot, err := session.QueuedModelSettings()
		if err == nil && !snapshot {
			model, err = server.modelsRepository().GetModelSettingsByProfileID(session.ProfileID)
		}
		if err != nil {
			_ = server.sessionsRepository().FailSession(session.ID, err, store.Usage{}, time.Now())
			continue
		}
		model = pinCodexSessionModel(session, model)
		_ = server.startQueuedTurn(session.ID, model)
	}
	return nil
}

func isActiveSessionStatus(status store.SessionStatus) bool {
	return status == store.SessionQueued || status == store.SessionRunning || status == store.SessionPaused
}
