package server

import (
	"fmt"
	"log"
	"strings"

	"github.com/lakernote/beaver-agent/internal/store"
)

// notifyAutomationResult 在定时任务的会话结束后，把结果推送给任务勾选的微信成员。
// 非定时任务的会话在这里查不到任务，直接忽略。
func (server *Server) notifyAutomationResult(sessionID string, status store.AutomationStatus) {
	if status != store.AutomationCompleted && status != store.AutomationFailed && status != store.AutomationCanceled {
		return
	}
	task, err := server.store.GetAutomationTaskBySession(sessionID)
	if err != nil || len(task.NotifyWeixin) == 0 {
		return
	}
	session, err := server.store.LoadSessionWindow(sessionID, 40, 1)
	if err != nil {
		return
	}
	server.notifyAutomationMembers(task, automationResultText(task, session))
}

// notifyAutomationFailure 用于任务根本没能启动（没有会话）的情况，例如模型配置失效。
func (server *Server) notifyAutomationFailure(task store.AutomationTask, cause error) {
	server.notifyAutomationMembers(task, fmt.Sprintf("❌ 定时任务启动失败 · %s\n\n%v", task.Name, cause))
}

func (server *Server) notifyAutomationMembers(task store.AutomationTask, text string) {
	if len(task.NotifyWeixin) == 0 || !server.beginBackground() {
		return
	}
	go func() {
		defer server.completeBackground()
		settings, err := server.store.GetWeixinSettings()
		if err != nil || !settings.Enabled {
			return
		}
		for _, id := range task.NotifyWeixin {
			account, err := server.store.GetWeixinAccount(id)
			if err != nil || !account.Enabled {
				continue
			}
			if strings.TrimSpace(account.ReplyContextToken) == "" {
				log.Printf("定时任务通知微信跳过 task=%s member=%s: 成员尚无可用回复凭据", task.ID, account.Label)
				continue
			}
			// 主动推送沿用成员最近一条消息的 context_token；token 过期时微信可能拒收，
			// 此时让成员给机器人发任意一条消息即可刷新。
			if err := server.weixin.sendChunks(account, account.UserID, account.ReplyContextToken, text); err != nil {
				log.Printf("定时任务通知微信失败 task=%s member=%s: %v（成员一段时间没给机器人发消息时 context_token 会失效，发任意一条消息即可恢复）", task.ID, account.Label, err)
			}
		}
	}()
}

func automationResultText(task store.AutomationTask, session store.Session) string {
	switch session.Status {
	case store.SessionFailed:
		return fmt.Sprintf("❌ 定时任务失败 · %s\n\n%s", task.Name, strings.TrimSpace(session.Error))
	case store.SessionCanceled:
		return fmt.Sprintf("⏹️ 定时任务已停止 · %s", task.Name)
	}
	answer := lastAssistantText(session)
	if answer == "" {
		answer = "任务已结束，但没有可发送的文字结果。"
	}
	return fmt.Sprintf("⏰ 定时任务完成 · %s\n\n%s", task.Name, answer)
}
