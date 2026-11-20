package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lakernote/beaver-agent/internal/store"
	"github.com/lakernote/beaver-agent/internal/weixin"
)

func (manager *weixinManager) poll(ctx context.Context, id string) {
	failures := 0
	for ctx.Err() == nil {
		settings, err := manager.server.store.GetWeixinSettings()
		account, accountErr := manager.server.store.GetWeixinAccount(id)
		if err != nil || accountErr != nil || !settings.Enabled || !account.Enabled {
			return
		}
		updates, err := manager.gateway.GetUpdates(ctx, gatewayAccount(account), account.SyncBuffer)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			delay := 2 * time.Second
			if failures >= 5 {
				delay = 30 * time.Second
				failures = 0
			}
			if !waitContext(ctx, delay) {
				return
			}
			continue
		}
		failures = 0
		sort.SliceStable(updates.Messages, func(left, right int) bool { return updates.Messages[left].Sequence < updates.Messages[right].Sequence })
		lastSequence := account.LastSequence
		for _, message := range updates.Messages {
			if message.Sequence > lastSequence {
				lastSequence = message.Sequence
			}
			manager.handleMessage(ctx, account, settings, message)
			fresh, loadErr := manager.server.store.GetWeixinAccount(id)
			if loadErr == nil {
				account = fresh
			}
		}
		nextBuffer := account.SyncBuffer
		if updates.Buffer != "" {
			nextBuffer = updates.Buffer
		}
		// 每次成功拉取都刷新在线时间。否则没有新消息时，页面会把一个健康的
		// 长轮询连接显示成“很久没有活动”，让管理员误以为通道已经掉线。
		_ = manager.server.store.UpdateWeixinCursor(id, nextBuffer, lastSequence, time.Now())
	}
}

func (manager *weixinManager) handleMessage(ctx context.Context, account store.WeixinAccount, settings store.WeixinSettings, message weixin.Message) {
	freshSettings, settingsErr := manager.server.store.GetWeixinSettings()
	freshAccount, accountErr := manager.server.store.GetWeixinAccount(account.ID)
	if settingsErr != nil || accountErr != nil || !freshSettings.Enabled || !freshAccount.Enabled {
		return
	}
	settings = freshSettings
	account = freshAccount
	if message.MessageType != 1 || message.FromUserID != account.UserID || (message.Sequence > 0 && message.Sequence <= account.LastSequence) {
		return
	}
	messageID := message.MessageID
	if compareWeixinMessageID(messageID, "") == 0 && message.Sequence > 0 {
		messageID = strconv.FormatInt(message.Sequence, 10)
	}
	if compareWeixinMessageID(messageID, "") == 0 {
		messageID = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	message.MessageID = messageID
	if compareWeixinMessageID(messageID, account.PendingMessageID) <= 0 || compareWeixinMessageID(messageID, account.DeliveredMessageID) <= 0 {
		return
	}
	createdAt := time.Time{}
	if message.CreatedAtMS > 0 {
		createdAt = time.UnixMilli(message.CreatedAtMS)
	}
	ignoreBefore := settings.IgnoreBefore
	if account.IgnoreBefore.After(ignoreBefore) {
		ignoreBefore = account.IgnoreBefore
	}
	if !ignoreBefore.IsZero() && (message.CreatedAtMS == 0 || createdAt.Before(ignoreBefore)) {
		return
	}
	// 每条有效入站都刷新独立的回复凭据，供普通结果与 Automation 共用；
	// 只有存在未送达结果时才唤醒投递。这里不改变 pending message ID，
	// 避免把命令或“任务仍在执行”这类消息误当成新的 Agent 轮次。
	if strings.TrimSpace(message.ContextToken) != "" {
		refreshed, refreshErr := manager.server.store.RefreshWeixinReplyContext(account.ID, message.ContextToken, createdAt, time.Now())
		if refreshErr == nil && refreshed {
			account.ReplyContextToken = message.ContextToken
			if compareWeixinMessageID(account.PendingMessageID, account.DeliveredMessageID) > 0 {
				// 先回复本条命令/提示，再唤醒最终结果，避免时间顺序颠倒。
				defer manager.resumeDelivery(account.ID)
			}
		}
	}
	text, attachments, err := manager.decodeWeixinMessage(ctx, account, message)
	if err != nil {
		if errors.Is(err, errWeixinVoiceNeedsText) {
			_ = manager.server.store.RecordWeixinMessage(account.ID, createdAt, time.Now())
			manager.send(ctx, account, message.FromUserID, message.ContextToken, "这条语音没有取得微信文字，因此没有启动 Agent。\n请补发文字说明后重试。")
			return
		}
		manager.send(ctx, account, message.FromUserID, message.ContextToken, "消息处理失败："+err.Error())
		return
	}
	if text == "" && len(attachments) == 0 {
		manager.send(ctx, account, message.FromUserID, message.ContextToken, "暂时无法识别这条消息；当前支持文字、带微信文字的语音、图片、PDF 和文本/代码文件。")
		return
	}
	// 停用成员会取消 poller context。附件解码可能耗时，
	// 所以在任何命令或任务副作用前再检查一次。
	if ctx.Err() != nil {
		return
	}
	if manager.handleCommand(ctx, account, message, text) {
		return
	}
	if text == "" {
		text = defaultWeixinMediaPrompt(attachments)
	}
	sessionID, err := manager.submit(account, message, text, attachments, messageID, createdAt)
	if err != nil {
		manager.send(ctx, account, message.FromUserID, message.ContextToken, "任务未提交："+err.Error())
		return
	}
	acknowledgement := weixinTaskAcknowledgement("")
	if session, loadErr := manager.server.store.LoadSessionWindow(sessionID, 1, 1); loadErr == nil {
		acknowledgement = weixinTaskAcknowledgement(session.Title)
	}
	// 任务因并发已满或工作区被占用而排队时，提前告知成员，而不是让人以为卡住了。
	if manager.waitsInQueue(sessionID) {
		active, limit := manager.server.scheduler.snapshot()
		acknowledgement += fmt.Sprintf("\n⏳ 当前 %d 个任务在运行（并发上限 %d），你的任务正在排队，轮到后会自动开始。", active, limit)
	}
	manager.send(ctx, account, message.FromUserID, message.ContextToken, acknowledgement)
	// 先发送接收确认，再启动结果等待，避免极短任务的最终结果先于确认消息到达。
	manager.resumeDelivery(account.ID)
}

// waitsInQueue 判断刚提交的任务是否在排队。任务协程拿到执行槽前状态仍是 queued，
// 所以最多短暂等待一会儿，直到它要么开始运行、要么被确认在等待。
func (manager *weixinManager) waitsInQueue(sessionID string) bool {
	for attempt := 0; attempt < 8; attempt++ {
		if manager.server.scheduler.isWaiting(sessionID) {
			return true
		}
		session, err := manager.server.store.LoadSessionWindow(sessionID, 1, 1)
		if err != nil || session.Status != store.SessionQueued {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

func weixinTaskAcknowledgement(title string) string {
	lines := []string{"📥 任务已接收"}
	if title = strings.TrimSpace(title); title != "" {
		lines = append(lines, "💬 会话："+title)
	}
	lines = append(lines, "", "完成后会在这里回复；任务较长时约 1 分钟后会提示一次进度。", "若最终结果没有送达，请发送 /status 刷新回传凭据；发送 /stop 可中断任务。")
	return strings.Join(lines, "\n")
}

func messageText(message weixin.Message) string {
	values := make([]string, 0, len(message.Items))
	for _, item := range message.Items {
		if item.Type == 1 && item.TextItem != nil {
			if value := strings.TrimSpace(item.TextItem.Text); value != "" {
				values = append(values, value)
			}
		}
		if item.Type == 3 && item.VoiceItem != nil {
			if value := strings.TrimSpace(item.VoiceItem.Text); value != "" {
				values = append(values, value)
			}
		}
	}
	return strings.Join(values, "\n")
}
