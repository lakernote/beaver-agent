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
		manager.delivery[accountID] = true
		manager.mu.Unlock()
		return
	}
	manager.delivery[accountID] = false
	manager.mu.Unlock()
	if !manager.server.beginBackground() {
		manager.mu.Lock()
		delete(manager.delivery, accountID)
		manager.mu.Unlock()
		return
	}
	go func() {
		defer manager.server.completeBackground()
		manager.runDelivery(accountID, manager.deliverPending)
	}()
}

// runDelivery 先投递到当前没有待发结果，再原子地处理“继续一轮”或释放所有权。
// resumeDelivery 与这里共用 manager.mu，因此新请求要么留下唤醒标记，要么在
// 旧协程删除标记后启动新协程，不会落在两者之间。
func (manager *weixinManager) runDelivery(accountID string, deliver func(string) bool) {
	for {
		for deliver(accountID) {
		}
		manager.mu.Lock()
		if manager.delivery[accountID] {
			manager.delivery[accountID] = false
			manager.mu.Unlock()
			continue
		}
		delete(manager.delivery, accountID)
		manager.mu.Unlock()
		return
	}
}

// deliverPending 等当前任务结束后，把最终结果发给成员；成功发送返回 true。
func (manager *weixinManager) deliverPending(accountID string) bool {
	account, err := manager.server.store.GetWeixinAccount(accountID)
	if err != nil || account.PendingMessageID == 0 || account.PendingMessageID <= account.DeliveredMessageID || account.CurrentSessionID == "" {
		return false
	}
	manager.waitWithProgress(account.ID, account.CurrentSessionID)
	if manager.server.ctx.Err() != nil {
		return false
	}
	account, err = manager.server.store.GetWeixinAccount(accountID)
	if err != nil || account.PendingMessageID <= account.DeliveredMessageID {
		return false
	}
	settings, _ := manager.server.store.GetWeixinSettings()
	if !settings.Enabled || !account.Enabled {
		return false
	}
	session, err := manager.server.store.LoadSessionWindow(account.CurrentSessionID, 40, 1)
	if err != nil {
		return false
	}
	answer := finalSessionText(session)
	if answer == "" {
		answer = "任务已结束，但没有可发送的文字结果。"
	}
	if err := manager.sendChunks(account, account.UserID, account.PendingContextToken, answer); err != nil {
		log.Printf("微信远程：发送最终结果失败 account=%s: %v", account.ID, err)
		return false
	}
	_ = manager.server.store.MarkWeixinDelivered(account.ID, account.PendingMessageID, time.Now())
	return true
}

// 长任务期间的主动进度：每隔 interval 推送一次，最多 max 条，进度没变化就不打扰。
// 条数有上限：iLink 对同一条入站消息的 context_token 只允许约 10 次回复（社区实测，见
// Tencent/openclaw-weixin issue #81，非官方文档），超出返回 ret=-2。
// 接收确认 1 条 + 进度最多 3 条，剩下的额度留给分段的最终结果。
var (
	weixinProgressInterval = 2 * time.Minute
	weixinProgressMax      = 3
)

// waitWithProgress 等任务结束，期间按固定间隔把当前进度推送给成员。
func (manager *weixinManager) waitWithProgress(accountID, sessionID string) {
	startedAt := time.Now()
	lastProgress := ""
	for sent := 0; !manager.server.tasks.waitFor(manager.server.ctx, sessionID, weixinProgressInterval); {
		if manager.server.ctx.Err() != nil {
			return
		}
		progress := manager.server.tasks.progress(sessionID)
		if sent >= weixinProgressMax || progress == "" || progress == lastProgress {
			continue
		}
		account, err := manager.server.store.GetWeixinAccount(accountID)
		if err != nil || !account.Enabled {
			continue
		}
		text := fmt.Sprintf("⏳ 仍在执行（已 %d 分钟）\n🧭 进度：%s\n发送 /stop 可中断。", int(time.Since(startedAt).Minutes()), progress)
		manager.send(account, account.UserID, account.PendingContextToken, text)
		lastProgress = progress
		sent++
	}
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
		return fmt.Sprintf("❌ 任务失败 · %s\n\n%s\n\n发送 /status 查看会话，或发送 /new 开始独立任务。", session.Title, strings.TrimSpace(session.Error))
	}
	if session.Status == store.SessionCanceled {
		return fmt.Sprintf("⏹️ 任务已停止 · %s\n\n可以继续发送消息，或发送 /new 开始独立任务。", session.Title)
	}
	if answer := lastAssistantText(session); answer != "" {
		return fmt.Sprintf("✅ 任务完成 · %s\n\n%s\n\n继续发送消息可在本会话追问；发送 /new 开始独立任务。", session.Title, answer)
	}
	return ""
}

// lastAssistantText 返回会话里最后一条有文字内容的助手消息。
func lastAssistantText(session store.Session) string {
	for index := len(session.Messages) - 1; index >= 0; index-- {
		if session.Messages[index].Role == "assistant" && strings.TrimSpace(session.Messages[index].Content) != "" {
			return strings.TrimSpace(session.Messages[index].Content)
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
