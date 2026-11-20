package server

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/lakernote/beaver-agent/internal/store"
	"github.com/lakernote/beaver-agent/internal/weixin"
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

// deliverPending 等当前任务结束后处理一代结果；返回 true 表示应立即检查下一代。
func (manager *weixinManager) deliverPending(accountID string) bool {
	return manager.deliverPendingWithWait(accountID, manager.waitWithProgress)
}

func (manager *weixinManager) deliverPendingWithWait(accountID string, wait func(string, string)) bool {
	account, err := manager.server.store.GetWeixinAccount(accountID)
	if err != nil || account.PendingMessageID == 0 || account.PendingMessageID <= account.DeliveredMessageID || account.CurrentSessionID == "" {
		return false
	}
	pendingMessageID := account.PendingMessageID
	pendingSessionID := account.CurrentSessionID
	wait(account.ID, pendingSessionID)
	if manager.server.ctx.Err() != nil {
		return false
	}

	// 与 submit 共用成员级短锁：要么冻结这一代的终态文本，要么完整看到
	// 下一代的 session + pending 槽。锁只覆盖本地读取，绝不覆盖微信网络请求。
	gate := manager.accountDeliveryGate(accountID)
	gate.Lock()
	account, err = manager.server.store.GetWeixinAccount(accountID)
	if err != nil {
		gate.Unlock()
		return false
	}
	if account.PendingMessageID != pendingMessageID || account.CurrentSessionID != pendingSessionID {
		gate.Unlock()
		return true
	}
	if account.PendingMessageID <= account.DeliveredMessageID {
		gate.Unlock()
		return false
	}
	settings, _ := manager.server.store.GetWeixinSettings()
	if !settings.Enabled || !account.Enabled {
		gate.Unlock()
		return false
	}
	session, err := manager.server.store.LoadSessionWindow(pendingSessionID, 40, 1)
	if err != nil {
		gate.Unlock()
		return false
	}
	if isActiveSessionStatus(session.Status) {
		gate.Unlock()
		return false
	}
	answer := finalSessionText(session)
	if answer == "" {
		answer = "任务已结束，但没有可发送的文字结果。"
	}
	replyContextToken := account.ReplyContextToken
	// account、token 和 answer 从此都是这一代的不可变快照。
	gate.Unlock()

	if err := manager.sendChunks(account, account.UserID, replyContextToken, answer); err != nil {
		log.Printf("微信远程：发送最终结果失败 account=%s: %v", account.ID, err)
		return false
	}
	marked, err := manager.server.store.MarkWeixinDelivered(account.ID, pendingMessageID, time.Now())
	if err != nil {
		log.Printf("微信远程：标记最终结果送达失败 account=%s message=%d: %v", account.ID, pendingMessageID, err)
		return false
	}
	// 发送期间若新消息覆盖了 pending 槽，CAS 会失败；旧结果已经发出，但绝不
	// 清空新 token 或把新一代提前标成送达，立即进入下一轮即可。
	if !marked {
		return true
	}
	return true
}

// 长任务期间只做一次较早的主动进度提示。腾讯没有公开 iLink 的稳定限制
// 契约，社区既报告过回复次数限制，也报告过约 1.5–2 分钟的时效窗口；
// 进度消息不能被当成续期机制，只用于在凭据可能失效前提醒用户。
var (
	weixinProgressInterval = time.Minute
	weixinProgressMax      = 1
)

// weixinTypingInterval 是“正在输入”的保活间隔，与官方插件一致。
var weixinTypingInterval = 5 * time.Second

// startTyping 在任务运行期间让微信显示“对方正在输入…”，返回停止函数。
// 网关不支持、取不到 ticket 或连续失败 3 次时静默放弃，不影响任务和结果。
func (manager *weixinManager) startTyping(account store.WeixinAccount) (stop func()) {
	gateway, ok := manager.gateway.(weixin.TypingGateway)
	if !ok {
		return func() {}
	}
	ctx, cancel := context.WithCancel(manager.server.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		wire := gatewayAccount(account)
		ticket, err := gateway.TypingTicket(ctx, wire, account.UserID, account.ReplyContextToken)
		if err != nil {
			return
		}
		failures := 0
		for {
			if err := gateway.SendTyping(ctx, wire, account.UserID, ticket, true); err != nil {
				if failures++; failures >= 3 || ctx.Err() != nil {
					return
				}
			} else {
				failures = 0
			}
			if !waitContext(ctx, weixinTypingInterval) {
				break
			}
		}
		cancelCtx, cancelTimeout := context.WithTimeout(manager.server.ctx, 5*time.Second)
		defer cancelTimeout()
		_ = gateway.SendTyping(cancelCtx, wire, account.UserID, ticket, false)
	}()
	return func() {
		cancel()
		<-done
	}
}

// waitWithProgress 等任务结束，期间显示“正在输入”，并按固定间隔把当前进度推送给成员。
func (manager *weixinManager) waitWithProgress(accountID, sessionID string) {
	if account, err := manager.server.store.GetWeixinAccount(accountID); err == nil {
		defer manager.startTyping(account)()
	}
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
		text := fmt.Sprintf("⏳ 仍在执行（已 %d 分钟）\n🧭 进度：%s\n若稍后没有收到最终结果，请发送 /status 刷新回传凭据；发送 /stop 可中断。", int(time.Since(startedAt).Minutes()), progress)
		manager.send(account, account.UserID, account.ReplyContextToken, text)
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
	if isActiveSessionStatus(session.Status) {
		return ""
	}
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
