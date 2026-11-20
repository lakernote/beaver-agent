package server

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/lakernote/beaver-agent/internal/store"
)

func (manager *weixinManager) resumeDelivery(accountID string) {
	manager.mu.Lock()
	if _, exists := manager.delivery[accountID]; exists {
		manager.mu.Unlock()
		return
	}
	manager.delivery[accountID] = struct{}{}
	manager.mu.Unlock()
	if !manager.server.beginBackground() {
		manager.mu.Lock()
		delete(manager.delivery, accountID)
		manager.mu.Unlock()
		return
	}
	go func() {
		defer manager.server.completeBackground()
		defer func() {
			manager.mu.Lock()
			delete(manager.delivery, accountID)
			manager.mu.Unlock()
		}()
		account, err := manager.server.store.GetWeixinAccount(accountID)
		if err != nil || account.PendingMessageID == 0 || account.PendingMessageID <= account.DeliveredMessageID || account.CurrentSessionID == "" {
			return
		}
		_ = manager.server.tasks.wait(manager.server.ctx, account.CurrentSessionID)
		if manager.server.ctx.Err() != nil {
			return
		}
		account, err = manager.server.store.GetWeixinAccount(accountID)
		if err != nil || account.PendingMessageID <= account.DeliveredMessageID {
			return
		}
		settings, _ := manager.server.store.GetWeixinSettings()
		if !settings.Enabled || !account.Enabled {
			return
		}
		session, err := manager.server.sessionsRepository().LoadSessionWindow(account.CurrentSessionID, 40, 1)
		if err != nil {
			return
		}
		answer := finalSessionText(session)
		if answer == "" {
			answer = "任务已结束，但没有可发送的文字结果。"
		}
		if err := manager.sendChunks(account, account.UserID, account.PendingContextToken, answer); err != nil {
			log.Printf("微信远程：发送最终结果失败 account=%s: %v", account.ID, err)
			return
		}
		_ = manager.server.store.MarkWeixinDelivered(account.ID, account.PendingMessageID, time.Now())
	}()
}

func (manager *weixinManager) send(account store.WeixinAccount, userID, contextToken, text string) {
	ctx, cancel := context.WithTimeout(manager.server.ctx, 20*time.Second)
	defer cancel()
	if err := manager.gateway.SendText(ctx, gatewayAccount(account), userID, contextToken, text); err != nil && ctx.Err() == nil {
		log.Printf("微信远程：发送消息失败 account=%s: %v", account.ID, err)
	}
}

func (manager *weixinManager) sendChunks(account store.WeixinAccount, userID, contextToken, text string) error {
	for _, chunk := range splitText(text, 3500) {
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			ctx, cancel := context.WithTimeout(manager.server.ctx, 20*time.Second)
			err = manager.gateway.SendText(ctx, gatewayAccount(account), userID, contextToken, chunk)
			cancel()
			if err == nil {
				break
			}
			if manager.server.ctx.Err() != nil || !waitContext(manager.server.ctx, time.Duration(attempt+1)*time.Second) {
				return err
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func finalSessionText(session store.Session) string {
	if session.Status == store.SessionFailed {
		return fmt.Sprintf("❌ 任务失败 · %s\n\n%s\n\n发送 /status查看会话，或发送 /new开始独立任务。", session.Title, strings.TrimSpace(session.Error))
	}
	if session.Status == store.SessionCanceled {
		return fmt.Sprintf("⏹️ 任务已停止 · %s\n\n可以继续发送消息，或发送 /new开始独立任务。", session.Title)
	}
	for index := len(session.Messages) - 1; index >= 0; index-- {
		if session.Messages[index].Role == "assistant" && strings.TrimSpace(session.Messages[index].Content) != "" {
			return fmt.Sprintf("✅ 任务完成 · %s\n\n%s\n\n继续发送消息可在本会话追问；发送 /new开始独立任务。", session.Title, strings.TrimSpace(session.Messages[index].Content))
		}
	}
	return ""
}

func weixinSessionStatus(status store.SessionStatus) string {
	switch status {
	case store.SessionQueued:
		return "🟡 排队中"
	case store.SessionRunning:
		return "🔵 运行中"
	case store.SessionPaused:
		return "⏸️ 已暂停"
	case store.SessionFailed:
		return "❌ 失败"
	case store.SessionCanceled:
		return "⏹️ 已停止"
	default:
		return "✅ 已完成"
	}
}

func weixinRuntimeLabel(runtime store.Runtime) string {
	if runtime == store.RuntimeCodex {
		return "Codex Runtime"
	}
	return "Beaver Runtime"
}

func splitText(value string, limit int) []string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) == 0 {
		return nil
	}
	result := []string{}
	for len(runes) > 0 {
		end := limit
		if len(runes) < end {
			end = len(runes)
		}
		result = append(result, string(runes[:end]))
		runes = runes[end:]
	}
	return result
}
