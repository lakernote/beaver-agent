package server

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/lakernote/beaver-agent/internal/store"
	"github.com/lakernote/beaver-agent/internal/weixin"
)

func (manager *weixinManager) submit(account store.WeixinAccount, message weixin.Message, text string, attachments []store.Attachment, messageID string, createdAt time.Time) (string, error) {
	// 同一会话会被连续复用。提交必须与投递侧冻结上一轮结果互斥，否则新轮
	// 已写入历史但 pending 槽尚未更新时，投递侧可能把两代数据拼在一起。
	gate := manager.accountDeliveryGate(account.ID)
	gate.Lock()
	defer gate.Unlock()

	if current, err := manager.currentSession(account); err == nil && isActiveSessionStatus(current.Status) {
		return "", errors.New("上一条任务仍在执行，可发送 /status 查看或发送 /stop 中断")
	}
	now := time.Now()
	sessionID := account.CurrentSessionID
	var model store.ModelSettings
	if sessionID != "" {
		current, err := manager.server.store.LoadSessionWindow(sessionID, 1, 1)
		if err == nil {
			model, err = manager.server.store.GetModelSettingsByProfileID(current.ProfileID)
			if err != nil {
				return "", err
			}
			if err := manager.server.enqueueTurn(sessionID, text, attachments, model); err != nil {
				return "", err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		} else {
			sessionID = ""
		}
	}
	if sessionID == "" {
		var err error
		model, err = manager.server.store.GetModelSettings()
		if err != nil {
			return "", err
		}
		project, err := manager.weixinProject(account)
		if err != nil {
			return "", err
		}
		runEnvironment, err := manager.server.env.WithWorkspace(project.Directories[0])
		if err != nil {
			return "", err
		}
		sessionID = newID()
		runtimeSettings, err := manager.server.store.GetRuntimeSettings()
		if err != nil {
			return "", err
		}
		workspace := manager.server.prepareSessionWorkspace(manager.server.ctx, sessionID, runEnvironment.Workspace(), runtimeSettings)
		if _, err := manager.server.store.CreateSession(store.CreateSessionParams{
			ID: sessionID, Title: account.Label + "的对话", Runtime: model.Runtime,
			Channel:   store.ChannelWeixin,
			ProfileID: model.ProfileID, Model: model.Model, ProjectID: project.ID,
			Permissions: runtimeSettings.Permissions, Workspace: workspace.Execution, CreatedAt: now,
		}); err != nil {
			manager.server.discardPreparedWorkspace(workspace)
			return "", err
		}
		// 记录会话所属成员，个人记忆按此隔离。
		if err := manager.server.store.SetSessionOwner(sessionID, account.ID); err != nil {
			_ = manager.server.store.DeleteSession(sessionID)
			manager.server.discardPreparedWorkspace(workspace)
			return "", err
		}
		if err := manager.server.store.SetSessionWorkspace(sessionID, workspace.Execution, workspace.Source, workspace.Branch, workspace.Notice); err != nil {
			_ = manager.server.store.DeleteSession(sessionID)
			manager.server.discardPreparedWorkspace(workspace)
			return "", err
		}
		if err := manager.server.enqueueTurn(sessionID, text, attachments, model); err != nil {
			_ = manager.server.store.DeleteSession(sessionID)
			manager.server.discardPreparedWorkspace(workspace)
			return "", err
		}
	}
	if createdAt.IsZero() {
		createdAt = now
	}
	if err := manager.server.store.SetWeixinTask(account.ID, sessionID, messageID, message.ContextToken, createdAt, now); err != nil {
		return "", err
	}
	return sessionID, nil
}

func (manager *weixinManager) weixinProject(account store.WeixinAccount) (store.Project, error) {
	if strings.TrimSpace(account.ProjectID) != "" {
		if project, err := manager.server.store.GetProject(account.ProjectID); err == nil && len(project.Directories) > 0 {
			return project, nil
		}
	}
	if err := manager.server.ensureDefaultProject(); err != nil {
		return store.Project{}, err
	}
	return manager.server.store.DefaultProject()
}

func (manager *weixinManager) currentSession(account store.WeixinAccount) (store.Session, error) {
	if strings.TrimSpace(account.CurrentSessionID) == "" {
		return store.Session{}, sql.ErrNoRows
	}
	return manager.server.store.LoadSessionWindow(account.CurrentSessionID, 1, 1)
}
