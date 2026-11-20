package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lakernote/beaver-agent/internal/appenv"
	"github.com/lakernote/beaver-agent/internal/store"
	"github.com/lakernote/beaver-agent/internal/weixin"
)

type fakeWeixinGateway struct {
	mu            sync.Mutex
	count         int
	media         []byte
	downloadCalls int
}

func (gateway *fakeWeixinGateway) StartLogin(context.Context, []string) (weixin.QRStart, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.count++
	code := strconv.Itoa(gateway.count)
	return weixin.QRStart{Code: code, Content: "https://example.test/qr/" + code}, nil
}

func (gateway *fakeWeixinGateway) PollLogin(_ context.Context, _ string, code string, _ string) (weixin.QRStatus, error) {
	return weixin.QRStatus{Status: "confirmed", BotToken: "token-" + code, AccountID: "bot-" + code, UserID: "weixin-user-00000" + code, BaseURL: "ilinkai.weixin.qq.com"}, nil
}

func (gateway *fakeWeixinGateway) GetUpdates(ctx context.Context, _ weixin.Account, _ string) (weixin.Updates, error) {
	<-ctx.Done()
	return weixin.Updates{}, ctx.Err()
}

func (gateway *fakeWeixinGateway) SendText(context.Context, weixin.Account, string, string, string) error {
	return nil
}

func (gateway *fakeWeixinGateway) DownloadMedia(context.Context, weixin.CDNMedia, string) ([]byte, error) {
	gateway.downloadCalls++
	if gateway.media == nil {
		return nil, errors.New("fake gateway has no media")
	}
	return append([]byte(nil), gateway.media...), nil
}

func TestWeixinFinalTextNeverIncludesTrace(t *testing.T) {
	session := store.Session{Title: "检查服务", Status: "idle", Events: []store.Event{{Kind: "tool_end", Detail: "private trace"}}, Messages: []store.Message{{Role: "user", Content: "任务"}, {Role: "assistant", Content: "最终结果"}}}
	value := finalSessionText(session)
	if !strings.Contains(value, "✅ 任务完成 · 检查服务") || !strings.Contains(value, "最终结果") || strings.Contains(value, "private trace") {
		t.Fatalf("微信只应收到最终 assistant 文本: %q", value)
	}
	if value := finalSessionText(store.Session{Status: store.SessionQueued, Messages: session.Messages}); value != "" {
		t.Fatalf("运行中的新一轮不应复用旧答案: %q", value)
	}
}

func TestWeixinAcknowledgementDoesNotExposeTransientQueueStatus(t *testing.T) {
	value := weixinTaskAcknowledgement(" 小伙计 ")
	if !strings.Contains(value, "📥 任务已接收\n💬 会话：小伙计") || !strings.Contains(value, "发送 /status 刷新回传凭据") {
		t.Fatalf("接收确认缺少必要信息: %q", value)
	}
	if strings.Contains(value, "排队中") || strings.Contains(value, "状态：") {
		t.Fatalf("接收确认不应展示瞬时任务状态: %q", value)
	}
}

func TestWeixinMessageFilteringAndUnicodeChunks(t *testing.T) {
	message := weixin.Message{Items: []weixin.MessageItem{{Type: 2}, {Type: 1, TextItem: &weixin.TextItem{Text: "  执行任务  "}}, {Type: 3, VoiceItem: &weixin.VoiceItem{Text: "检查测试"}}}}
	if value := messageText(message); value != "执行任务\n检查测试" {
		t.Fatalf("文字提取错误: %q", value)
	}
	chunks := splitText("一二三四五", 2)
	if len(chunks) != 3 || chunks[0] != "一二" || chunks[2] != "五" {
		t.Fatalf("Unicode 分片错误: %#v", chunks)
	}
}

func TestDecodeWeixinImageCreatesSharedAttachment(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0, 'I', 'H', 'D', 'R'}
	gateway := &fakeWeixinGateway{media: png}
	manager := &weixinManager{gateway: gateway}
	text, attachments, err := manager.decodeWeixinMessage(context.Background(), store.WeixinAccount{}, weixin.Message{MessageID: "42", Items: []weixin.MessageItem{{Type: 2, ImageItem: &weixin.ImageItem{Media: &weixin.CDNMedia{FullURL: "https://novac2c.cdn.weixin.qq.com/image"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if text != "" || len(attachments) != 1 || attachments[0].Kind != "image" || attachments[0].Name != "weixin-image-42.png" || gateway.downloadCalls != 1 {
		t.Fatalf("unexpected image decode: text=%q attachments=%+v calls=%d", text, attachments, gateway.downloadCalls)
	}
}

func TestNativeWeixinVoiceTextDoesNotDownloadAudio(t *testing.T) {
	gateway := &fakeWeixinGateway{}
	manager := &weixinManager{gateway: gateway}
	text, attachments, err := manager.decodeWeixinMessage(context.Background(), store.WeixinAccount{}, weixin.Message{Items: []weixin.MessageItem{{Type: 3, VoiceItem: &weixin.VoiceItem{Text: "运行全部测试", Media: &weixin.CDNMedia{FullURL: "https://novac2c.cdn.weixin.qq.com/voice"}}}}})
	if err != nil || text != "运行全部测试" || len(attachments) != 0 || gateway.downloadCalls != 0 {
		t.Fatalf("unexpected native voice decode: text=%q attachments=%+v calls=%d err=%v", text, attachments, gateway.downloadCalls, err)
	}
}

func TestUntranscribedWeixinVoiceRequestsTextWithoutDownload(t *testing.T) {
	gateway := &fakeWeixinGateway{}
	manager := &weixinManager{gateway: gateway}
	text, attachments, err := manager.decodeWeixinMessage(context.Background(), store.WeixinAccount{}, weixin.Message{Items: []weixin.MessageItem{{Type: 3, VoiceItem: &weixin.VoiceItem{Media: &weixin.CDNMedia{FullURL: "https://novac2c.cdn.weixin.qq.com/voice"}}}}})
	if !errors.Is(err, errWeixinVoiceNeedsText) || text != "" || len(attachments) != 0 || gateway.downloadCalls != 0 {
		t.Fatalf("unexpected untranscribed voice decode: text=%q attachments=%+v calls=%d err=%v", text, attachments, gateway.downloadCalls, err)
	}
}

func TestBeaverUsesNativeVoiceTextWithoutBinaryAudio(t *testing.T) {
	message := toCoreMessage(store.Message{Role: "user", Content: "运行全部测试", Attachments: []store.Attachment{
		{Name: "voice.wav", MIMEType: "audio/wav", Kind: "audio", Data: []byte("RIFF")},
		{Name: "notes.txt", MIMEType: "text/plain", Kind: "text", Data: []byte("notes")},
	}})
	if message.Content != "运行全部测试" || len(message.Attachments) != 1 || message.Attachments[0].Kind != "text" {
		t.Fatalf("Beaver voice mapping leaked binary audio: %+v", message)
	}
}

func TestWeixinFailedAndCanceledFinalText(t *testing.T) {
	if value := finalSessionText(store.Session{Title: "失败任务", Status: "failed", Error: "模型错误"}); !strings.Contains(value, "❌ 任务失败 · 失败任务") || !strings.Contains(value, "模型错误") {
		t.Fatalf("失败结果错误: %q", value)
	}
	if value := finalSessionText(store.Session{Title: "停止任务", Status: "canceled", UpdatedAt: time.Now()}); !strings.Contains(value, "⏹️ 任务已停止 · 停止任务") {
		t.Fatalf("停止结果错误: %q", value)
	}
}

func TestWeixinSessionStatusUsesScannableEmoji(t *testing.T) {
	tests := map[string]string{
		"queued":   "🟡 排队中",
		"running":  "🔵 运行中",
		"paused":   "⏸️ 已暂停",
		"failed":   "❌ 失败",
		"canceled": "⏹️ 已停止",
		"idle":     "✅ 已完成",
	}
	for status, expected := range tests {
		if actual := weixinSessionStatus(store.SessionStatus(status)); actual != expected {
			t.Fatalf("状态 %q 显示为 %q，期望 %q", status, actual, expected)
		}
	}
}

func TestWeixinSlashCommands(t *testing.T) {
	tests := map[string]string{
		"/new":                    "new",
		" /Status ":               "status",
		"/stop":                   "stop",
		"/help":                   "help",
		"/projects":               "projects",
		"/project":                "current_project",
		"/project Beaver":         "project",
		"/stop now":               "unknown",
		"/stpo":                   "unknown",
		"/usr/bin/env 怎么用":        "",
		"创建新会话":                   "",
		"停止当前任务":                  "",
		"please stop the service": "",
	}
	for input, expected := range tests {
		if actual := parseWeixinCommand(input).Name; actual != expected {
			t.Fatalf("命令 %q 解析为 %q，期望 %q", input, actual, expected)
		}
	}
	if arg := parseWeixinCommand("/project Beaver Web").Argument; arg != "Beaver Web" {
		t.Fatalf("项目名解析错误: %q", arg)
	}
}

func TestWeixinDeliveryStatus(t *testing.T) {
	account := store.WeixinAccount{PendingMessageID: "8"}
	if actual := weixinDeliveryStatus(account, "running", false); actual != "processing" {
		t.Fatalf("运行中的任务状态错误: %q", actual)
	}
	if actual := weixinDeliveryStatus(account, "idle", true); actual != "sending" {
		t.Fatalf("回传中的任务状态错误: %q", actual)
	}
	if actual := weixinDeliveryStatus(account, "idle", false); actual != "pending" {
		t.Fatalf("待重试状态错误: %q", actual)
	}
	account.DeliveredMessageID = "8"
	if actual := weixinDeliveryStatus(account, "idle", false); actual != "delivered" {
		t.Fatalf("已送达状态错误: %q", actual)
	}
}

func TestWeixinDeliveryWakeupDuringExitStartsAnotherPass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	current := &weixinDelivery{ctx: ctx, cancel: cancel, generation: 1}
	manager := &weixinManager{
		delivery:           map[string]*weixinDelivery{"account": current},
		deliveryGeneration: map[string]uint64{"account": 1},
		deliveryStopped:    make(map[string]bool),
	}
	firstPass := make(chan struct{})
	releaseFirstPass := make(chan struct{})
	done := make(chan struct{})
	passes := 0
	go func() {
		manager.runDelivery(ctx, "account", 1, func(context.Context, string, uint64) bool {
			passes++
			if passes == 1 {
				close(firstPass)
				<-releaseFirstPass
			}
			return false
		})
		close(done)
	}()

	<-firstPass
	// 旧协程还占有 account 时到达的新请求，必须留下唤醒标记。
	manager.resumeDelivery("account")
	close(releaseFirstPass)
	<-done

	if passes != 2 {
		t.Fatalf("delivery passes = %d, want 2", passes)
	}
	manager.mu.Lock()
	_, exists := manager.delivery["account"]
	manager.mu.Unlock()
	if exists {
		t.Fatal("delivery ownership was not released")
	}
}

func TestWeixinDeliveryExitReleasesOwnershipWithWakeDecision(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	current := &weixinDelivery{ctx: ctx, cancel: cancel, generation: 7}
	manager := &weixinManager{
		delivery:           map[string]*weixinDelivery{"account": current},
		deliveryGeneration: map[string]uint64{"account": 7},
		deliveryStopped:    make(map[string]bool),
	}
	if manager.consumeDeliveryWakeOrRelease(ctx, "account", 7) {
		t.Fatal("worker without a wake request should exit")
	}
	manager.mu.Lock()
	_, exists := manager.delivery["account"]
	manager.mu.Unlock()
	if exists {
		t.Fatal("exit decision returned before releasing delivery ownership")
	}
	if ctx.Err() == nil {
		t.Fatal("released delivery context was not canceled")
	}
}

func TestWeixinAPIBindsMultipleAccountsWithoutExposingSecrets(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "beaver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	environment, err := appenv.Open(appenv.Config{Home: filepath.Join(t.TempDir(), "home")})
	if err != nil {
		t.Fatal(err)
	}
	application := newTestServer(t, database, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, environment)
	application.weixin.gateway = &fakeWeixinGateway{}
	defer application.Shutdown(context.Background())
	httpServer := httptest.NewServer(application.Handler())
	defer httpServer.Close()

	for _, label := range []string{"小王", "值班同学"} {
		response, err := http.Post(httpServer.URL+"/api/v1/channels/weixin/login", "application/json", bytes.NewBufferString(`{"label":"`+label+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("开始扫码失败: %d", response.StatusCode)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		accounts, loadErr := database.ListWeixinAccounts()
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if len(accounts) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("多人绑定没有完成: %+v", accounts)
		}
		time.Sleep(10 * time.Millisecond)
	}
	response, err := http.Get(httpServer.URL + "/api/v1/channels/weixin")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var state weixinStateView
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(state)
	if len(state.Accounts) != 2 || bytes.Contains(encoded, []byte("token-")) || bytes.Contains(encoded, []byte("weixin-user-000001")) || bytes.Contains(encoded, []byte("lastMessageAt")) {
		t.Fatalf("绑定列表数量或脱敏错误: %s", encoded)
	}
}

func TestSavingEnabledWeixinChannelDoesNotSuppressPendingResult(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "beaver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	environment, err := appenv.Open(appenv.Config{Home: filepath.Join(t.TempDir(), "home")})
	if err != nil {
		t.Fatal(err)
	}
	application := newTestServer(t, database, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, environment)
	application.weixin.gateway = &fakeWeixinGateway{}
	defer application.Shutdown(context.Background())
	httpServer := httptest.NewServer(application.Handler())
	defer httpServer.Close()

	now := time.Now().UTC()
	if _, err := database.SaveWeixinSettings(store.WeixinSettings{Enabled: true, IgnoreBefore: now}); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveWeixinAccount(store.WeixinAccount{ID: "bot-pending", Label: "小王", UserID: "user-pending", Token: "token-pending", BaseURL: weixin.DefaultBaseURL, Enabled: true, IgnoreBefore: now, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetWeixinTask("bot-pending", "session-pending", "88", "context-pending", now, now); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPut, httpServer.URL+"/api/v1/channels/weixin", bytes.NewBufferString(`{"enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("重复保存启用状态失败: %d", response.StatusCode)
	}
	account, err := database.GetWeixinAccount("bot-pending")
	if err != nil {
		t.Fatal(err)
	}
	if account.DeliveredMessageID != "" || account.ReplyContextToken != "context-pending" {
		t.Fatalf("重复保存启用状态不应吞掉待回传结果: %+v", account)
	}
}

func TestConcurrentWeixinChannelTogglesKeepLifecycleInSync(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "beaver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	environment, err := appenv.Open(appenv.Config{Home: filepath.Join(t.TempDir(), "home")})
	if err != nil {
		t.Fatal(err)
	}
	application := newTestServer(t, database, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, environment)
	defer application.Shutdown(context.Background())

	var workers sync.WaitGroup
	for index := 0; index < 20; index++ {
		for _, enabled := range []bool{true, false} {
			workers.Add(1)
			go func(enabled bool) {
				defer workers.Done()
				if _, err := application.weixin.setEnabled(enabled, time.Now()); err != nil {
					t.Errorf("set enabled=%v: %v", enabled, err)
				}
			}(enabled)
		}
	}
	workers.Wait()
	settings, err := database.GetWeixinSettings()
	if err != nil {
		t.Fatal(err)
	}
	application.weixin.mu.Lock()
	stopped := application.weixin.channelStopped
	application.weixin.mu.Unlock()
	if stopped == settings.Enabled {
		t.Fatalf("persisted enabled=%v but channelStopped=%v", settings.Enabled, stopped)
	}
}

func TestWeixinAPIShowsCurrentTaskAndRetriesDelivery(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "beaver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	environment, err := appenv.Open(appenv.Config{Home: filepath.Join(t.TempDir(), "home")})
	if err != nil {
		t.Fatal(err)
	}
	application := newTestServer(t, database, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, environment)
	application.weixin.gateway = &fakeWeixinGateway{}
	defer application.Shutdown(context.Background())
	httpServer := httptest.NewServer(application.Handler())
	defer httpServer.Close()

	now := time.Now().UTC()
	if _, err := database.SaveWeixinSettings(store.WeixinSettings{Enabled: true, IgnoreBefore: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateSession(store.CreateSessionParams{ID: "session-current", Title: "检查发布状态", Runtime: store.RuntimeCodex, Model: "gpt-5", Workspace: t.TempDir(), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveWeixinAccount(store.WeixinAccount{ID: "bot-current", Label: "值班同学", UserID: "user-current", Token: "token-current", BaseURL: weixin.DefaultBaseURL, Enabled: true, IgnoreBefore: now, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetWeixinTask("bot-current", "session-current", "99", "context-current", now, now); err != nil {
		t.Fatal(err)
	}

	response, err := http.Get(httpServer.URL + "/api/v1/channels/weixin")
	if err != nil {
		t.Fatal(err)
	}
	var before weixinStateView
	if err := json.NewDecoder(response.Body).Decode(&before); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if len(before.Accounts) != 1 || before.Accounts[0].CurrentSession == nil || before.Accounts[0].CurrentSession.Title != "检查发布状态" || before.Accounts[0].CurrentSession.Runtime != string(store.RuntimeCodex) || before.Accounts[0].DeliveryStatus != "pending" {
		t.Fatalf("当前任务视图错误: %+v", before.Accounts)
	}

	retryResponse, err := http.Post(httpServer.URL+"/api/v1/channels/weixin/accounts/bot-current/retry", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	retryResponse.Body.Close()
	if retryResponse.StatusCode != http.StatusOK {
		t.Fatalf("重试回传失败: %d", retryResponse.StatusCode)
	}
	deadline := time.Now().Add(time.Second)
	for {
		account, loadErr := database.GetWeixinAccount("bot-current")
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if account.DeliveredMessageID == "99" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("结果没有在重试后标记为送达: %+v", account)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWeixinMemberIDIsStableAcrossRebinding(t *testing.T) {
	if weixinMemberID("user-a") != weixinMemberID("user-a") || weixinMemberID("user-a") == weixinMemberID("user-b") {
		t.Fatal("成员 ID 应由微信用户 ID 稳定派生")
	}
}

func TestAutomationResultText(t *testing.T) {
	task := store.AutomationTask{Name: "每日巡检"}
	done := automationResultText(task, store.Session{Status: store.SessionIdle, Messages: []store.Message{{Role: "assistant", Content: "一切正常"}}})
	if !strings.Contains(done, "每日巡检") || !strings.Contains(done, "一切正常") {
		t.Fatalf("完成通知缺少任务名或结果: %q", done)
	}
	failed := automationResultText(task, store.Session{Status: store.SessionFailed, Error: "模型不可用"})
	if !strings.Contains(failed, "❌") || !strings.Contains(failed, "模型不可用") {
		t.Fatalf("失败通知缺少原因: %q", failed)
	}
}

type recordingGateway struct {
	fakeWeixinGateway
	sent   chan string
	tokens chan string
}

func (gateway *recordingGateway) SendText(_ context.Context, _ weixin.Account, userID, contextToken, text string) error {
	gateway.sent <- userID + "|" + text
	if gateway.tokens != nil {
		gateway.tokens <- contextToken
	}
	return nil
}

type blockingRecordingGateway struct {
	fakeWeixinGateway
	started chan struct{}
	release chan struct{}
	sent    chan string
	once    sync.Once
}

func (gateway *blockingRecordingGateway) SendText(ctx context.Context, _ weixin.Account, userID, _ string, text string) error {
	gateway.once.Do(func() { close(gateway.started) })
	select {
	case <-gateway.release:
		gateway.sent <- userID + "|" + text
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type typingRecordingGateway struct {
	fakeWeixinGateway
	tokens   chan string
	statuses chan bool
}

func (gateway *typingRecordingGateway) TypingTicket(_ context.Context, _ weixin.Account, _ string, contextToken string) (string, error) {
	gateway.tokens <- contextToken
	return "typing-ticket", nil
}

func (gateway *typingRecordingGateway) SendTyping(_ context.Context, _ weixin.Account, _ string, ticket string, typing bool) error {
	if ticket != "typing-ticket" {
		return errors.New("unexpected typing ticket")
	}
	gateway.statuses <- typing
	return nil
}

type lifecycleTypingGateway struct {
	fakeWeixinGateway
	typingStarted  chan struct{}
	typingCanceled chan struct{}
	typingStopped  chan struct{}
	sent           chan string
}

func (gateway *lifecycleTypingGateway) TypingTicket(context.Context, weixin.Account, string, string) (string, error) {
	return "typing-ticket", nil
}

func (gateway *lifecycleTypingGateway) SendTyping(ctx context.Context, _ weixin.Account, _ string, _ string, typing bool) error {
	if !typing {
		gateway.typingStopped <- struct{}{}
		return nil
	}
	gateway.typingStarted <- struct{}{}
	<-ctx.Done()
	gateway.typingCanceled <- struct{}{}
	return ctx.Err()
}

func (gateway *lifecycleTypingGateway) SendText(_ context.Context, _ weixin.Account, _ string, _ string, text string) error {
	gateway.sent <- text
	return nil
}

func waitWeixinDelivery(t *testing.T, manager *weixinManager, accountID string, after uint64) *weixinDelivery {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		manager.mu.Lock()
		current := manager.delivery[accountID]
		manager.mu.Unlock()
		if current != nil && current.generation > after {
			return current
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivery generation after %d did not start", after)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitWeixinDeliveryDone(t *testing.T, current *weixinDelivery) {
	t.Helper()
	select {
	case <-current.done:
	case <-time.After(2 * time.Second):
		t.Fatal("delivery generation did not stop")
	}
}

func newWeixinDeliveryFixture(t *testing.T) (*Server, *store.Store, string, string) {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "beaver.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	environment, err := appenv.Open(appenv.Config{Home: filepath.Join(t.TempDir(), "home")})
	if err != nil {
		t.Fatal(err)
	}
	application := newTestServer(t, database, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, environment)
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })
	now := time.Now().UTC()
	if _, err := database.SaveWeixinSettings(store.WeixinSettings{Enabled: true, IgnoreBefore: now}); err != nil {
		t.Fatal(err)
	}
	const sessionID = "delivery-session"
	if _, err := database.CreateSession(store.CreateSessionParams{ID: sessionID, Title: "连续追问", Runtime: store.RuntimeBeaver, Model: "fixture", Workspace: t.TempDir(), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := database.AppendMessage(sessionID, store.Message{Role: "assistant", Content: "A 的最终答案", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	const accountID = "delivery-account"
	if err := database.SaveWeixinAccount(store.WeixinAccount{ID: accountID, Label: "成员", UserID: "delivery-user", Token: "token", BaseURL: "https://example.test", Enabled: true, IgnoreBefore: now, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetWeixinTask(accountID, sessionID, "42", "context-A", now, now); err != nil {
		t.Fatal(err)
	}
	return application, database, accountID, sessionID
}

func TestWeixinDeliverySkipsSupersededPendingGeneration(t *testing.T) {
	application, database, accountID, sessionID := newWeixinDeliveryFixture(t)
	gateway := &recordingGateway{sent: make(chan string, 2)}
	application.weixin.gateway = gateway
	now := time.Now().UTC()
	retry := application.weixin.deliverPendingWithWait(accountID, func(string, string) {
		if err := database.SetWeixinTask(accountID, sessionID, "43", "context-B", now, now); err != nil {
			t.Fatal(err)
		}
	})
	if !retry {
		t.Fatal("superseded generation should trigger another delivery pass")
	}
	select {
	case got := <-gateway.sent:
		t.Fatalf("superseded generation sent stale result: %q", got)
	default:
	}
	account, err := database.GetWeixinAccount(accountID)
	if err != nil || account.PendingMessageID != "43" || account.DeliveredMessageID != "" || account.ReplyContextToken != "context-B" {
		t.Fatalf("new pending generation changed: %+v err=%v", account, err)
	}
}

func TestWeixinDeliveryCASPreservesMessageArrivingDuringSend(t *testing.T) {
	application, database, accountID, sessionID := newWeixinDeliveryFixture(t)
	gateway := &blockingRecordingGateway{started: make(chan struct{}), release: make(chan struct{}), sent: make(chan string, 2)}
	application.weixin.gateway = gateway
	done := make(chan bool, 1)
	go func() {
		done <- application.weixin.deliverPendingWithWait(accountID, func(string, string) {})
	}()
	select {
	case <-gateway.started:
	case <-time.After(2 * time.Second):
		t.Fatal("delivery did not reach gateway")
	}
	now := time.Now().UTC()
	if err := database.SetWeixinTask(accountID, sessionID, "43", "context-B", now, now); err != nil {
		t.Fatal(err)
	}
	close(gateway.release)
	select {
	case retry := <-done:
		if !retry {
			t.Fatal("new generation should trigger another delivery pass")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("delivery did not finish")
	}
	select {
	case got := <-gateway.sent:
		if !strings.Contains(got, "A 的最终答案") {
			t.Fatalf("unexpected frozen result: %q", got)
		}
	default:
		t.Fatal("frozen result was not sent")
	}
	account, err := database.GetWeixinAccount(accountID)
	if err != nil || account.PendingMessageID != "43" || account.DeliveredMessageID != "" || account.ReplyContextToken != "context-B" {
		t.Fatalf("old delivery cleared new generation: %+v err=%v", account, err)
	}
}

func TestWeixinNewWaitsForPendingFinalResult(t *testing.T) {
	application, database, accountID, sessionID := newWeixinDeliveryFixture(t)
	gateway := &recordingGateway{sent: make(chan string, 4)}
	application.weixin.gateway = gateway
	account, err := database.GetWeixinAccount(accountID)
	if err != nil {
		t.Fatal(err)
	}
	message := weixin.Message{FromUserID: account.UserID, ContextToken: "context-new"}
	if !application.weixin.handleCommand(context.Background(), account, message, "/new") {
		t.Fatal("/new was not handled")
	}
	select {
	case got := <-gateway.sent:
		if !strings.Contains(got, "结果正在回传") {
			t.Fatalf("unexpected /new reply: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("/new did not reply")
	}
	account, err = database.GetWeixinAccount(accountID)
	if err != nil || account.CurrentSessionID != sessionID {
		t.Fatalf("/new cleared a session with pending result: %+v err=%v", account, err)
	}
}

func TestWeixinInboundCommandRefreshesReplyContextWithoutPendingTask(t *testing.T) {
	application, database, accountID, _ := newWeixinDeliveryFixture(t)
	gateway := &recordingGateway{sent: make(chan string, 2)}
	application.weixin.gateway = gateway
	marked, err := database.MarkWeixinDelivered(accountID, "42", time.Now())
	if err != nil || !marked {
		t.Fatalf("mark initial delivery: marked=%v err=%v", marked, err)
	}
	account, err := database.GetWeixinAccount(accountID)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := database.GetWeixinSettings()
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().Add(time.Second)
	application.weixin.handleMessage(context.Background(), account, settings, weixin.Message{
		MessageID: "43", MessageType: 1, FromUserID: account.UserID, ContextToken: "context-help",
		CreatedAtMS: createdAt.UnixMilli(), Items: []weixin.MessageItem{{Type: 1, TextItem: &weixin.TextItem{Text: "/help"}}},
	})
	account, err = database.GetWeixinAccount(accountID)
	if err != nil || account.PendingMessageID != "42" || account.DeliveredMessageID != "42" || account.ReplyContextToken != "context-help" {
		t.Fatalf("inbound command did not refresh independent reply context: %+v err=%v", account, err)
	}
}

func TestWeixinInboundWithoutContextTokenPreservesLatestReplyContext(t *testing.T) {
	application, database, accountID, _ := newWeixinDeliveryFixture(t)
	gateway := &recordingGateway{sent: make(chan string, 2)}
	application.weixin.gateway = gateway
	// Keep the submitted turn queued so delivery cannot race the state assertion.
	application.scheduler.mu.Lock()
	application.scheduler.active = application.scheduler.limit
	application.scheduler.mu.Unlock()
	now := time.Now().UTC()
	const sessionID = "empty-context-session"
	if _, err := database.CreateSession(store.CreateSessionParams{
		ID: sessionID, Title: "空回复凭据", Runtime: store.RuntimeCodex,
		ProfileID: store.DefaultCodexProfileID, Workspace: t.TempDir(), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetWeixinCurrentSession(accountID, sessionID, now); err != nil {
		t.Fatal(err)
	}
	account, err := database.GetWeixinAccount(accountID)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := database.GetWeixinSettings()
	if err != nil {
		t.Fatal(err)
	}
	const messageID = "18446744073709551615"
	application.weixin.handleMessage(context.Background(), account, settings, weixin.Message{
		MessageID: messageID, MessageType: 1, FromUserID: account.UserID,
		CreatedAtMS: now.Add(time.Second).UnixMilli(),
		Items:       []weixin.MessageItem{{Type: 1, TextItem: &weixin.TextItem{Text: "继续检查"}}},
	})
	account, err = database.GetWeixinAccount(accountID)
	if err != nil || account.PendingMessageID != messageID || account.ReplyContextToken != "context-A" {
		t.Fatalf("empty inbound token erased latest reply context: %+v err=%v", account, err)
	}
}

func TestWeixinWaitShowsAndStopsTyping(t *testing.T) {
	application, _, accountID, sessionID := newWeixinDeliveryFixture(t)
	gateway := &typingRecordingGateway{tokens: make(chan string, 2), statuses: make(chan bool, 4)}
	application.weixin.gateway = gateway
	application.tasks.set(sessionID, "typing-test", func() {})
	done := make(chan struct{})
	go func() {
		application.weixin.waitWithProgress(accountID, sessionID)
		close(done)
	}()
	select {
	case token := <-gateway.tokens:
		if token != "context-A" {
			t.Fatalf("typing used context token %q", token)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("typing ticket was not requested")
	}
	select {
	case typing := <-gateway.statuses:
		if !typing {
			t.Fatal("typing stopped before it started")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("typing did not start")
	}
	application.tasks.clear(sessionID, "typing-test")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waitWithProgress did not stop")
	}
	select {
	case typing := <-gateway.statuses:
		if typing {
			t.Fatal("typing did not send stop status")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("typing stop status was not sent")
	}
}

func TestStoppingWeixinAccountCancelsFinalSendAndOldGeneration(t *testing.T) {
	application, database, accountID, _ := newWeixinDeliveryFixture(t)
	gateway := &blockingRecordingGateway{started: make(chan struct{}), release: make(chan struct{}), sent: make(chan string, 2)}
	application.weixin.gateway = gateway
	application.weixin.resumeDelivery(accountID)
	first := waitWeixinDelivery(t, application.weixin, accountID, 0)
	select {
	case <-gateway.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first delivery did not reach the gateway")
	}

	application.weixin.stopAccount(accountID)
	waitWeixinDeliveryDone(t, first)
	select {
	case value := <-gateway.sent:
		t.Fatalf("canceled generation sent a final result: %q", value)
	default:
	}
	account, err := database.GetWeixinAccount(accountID)
	if err != nil || compareWeixinMessageID(account.DeliveredMessageID, account.PendingMessageID) >= 0 {
		t.Fatalf("canceled generation marked the result delivered: %+v err=%v", account, err)
	}

	// 同一成员重新启用后会创建新代。旧代的 defer 只能观察自己的
	// generation + ctx，既不会借用新 ctx 发送，也不会删除新代所有权。
	application.weixin.restartAccount(accountID)
	second := waitWeixinDelivery(t, application.weixin, accountID, first.generation)
	close(gateway.release)
	select {
	case value := <-gateway.sent:
		if !strings.Contains(value, "A 的最终答案") {
			t.Fatalf("new generation sent an unexpected result: %q", value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new delivery generation did not send the pending result")
	}
	waitWeixinDeliveryDone(t, second)
	account, err = database.GetWeixinAccount(accountID)
	if err != nil || account.DeliveredMessageID != account.PendingMessageID {
		t.Fatalf("new generation did not commit delivery: %+v err=%v", account, err)
	}
	select {
	case value := <-gateway.sent:
		t.Fatalf("old generation was revived by the new context: %q", value)
	default:
	}
}

func TestWeixinLifecycleStopsTypingForDisableGlobalStopAndDelete(t *testing.T) {
	tests := []struct {
		name string
		stop func(*testing.T, *Server, *store.Store, string)
	}{
		{
			name: "disable member",
			stop: func(t *testing.T, application *Server, database *store.Store, accountID string) {
				t.Helper()
				account, err := database.GetWeixinAccount(accountID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := database.UpdateWeixinAccount(accountID, account.Label, false, account.ProjectID, time.Now()); err != nil {
					t.Fatal(err)
				}
				application.weixin.restartAccount(accountID)
			},
		},
		{
			name: "disable channel",
			stop: func(t *testing.T, application *Server, database *store.Store, _ string) {
				t.Helper()
				if _, err := database.SaveWeixinSettings(store.WeixinSettings{Enabled: false}); err != nil {
					t.Fatal(err)
				}
				application.weixin.stopAll()
			},
		},
		{
			name: "delete member",
			stop: func(t *testing.T, application *Server, database *store.Store, accountID string) {
				t.Helper()
				application.weixin.stopAccount(accountID)
				if err := database.DeleteWeixinAccount(accountID); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			application, database, accountID, sessionID := newWeixinDeliveryFixture(t)
			gateway := &lifecycleTypingGateway{
				typingStarted: make(chan struct{}, 2), typingCanceled: make(chan struct{}, 2),
				typingStopped: make(chan struct{}, 2), sent: make(chan string, 2),
			}
			application.weixin.gateway = gateway
			application.tasks.set(sessionID, "typing-lifecycle", func() {})
			application.weixin.resumeDelivery(accountID)
			current := waitWeixinDelivery(t, application.weixin, accountID, 0)
			select {
			case <-gateway.typingStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("typing did not start")
			}

			test.stop(t, application, database, accountID)
			select {
			case <-gateway.typingCanceled:
			case <-time.After(2 * time.Second):
				t.Fatal("typing request did not observe account cancellation")
			}
			waitWeixinDeliveryDone(t, current)
			application.tasks.clear(sessionID, "typing-lifecycle")
			select {
			case text := <-gateway.sent:
				t.Fatalf("stopped account sent a final result: %q", text)
			default:
			}
			select {
			case <-gateway.typingStopped:
				t.Fatal("old generation sent typing cleanup after it was invalidated")
			default:
			}
		})
	}
}

func TestStoppingWeixinAccountCancelsAutomationSend(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "beaver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	environment, err := appenv.Open(appenv.Config{Home: filepath.Join(t.TempDir(), "home")})
	if err != nil {
		t.Fatal(err)
	}
	application := newTestServer(t, database, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, environment)
	defer application.Shutdown(context.Background())
	now := time.Now().UTC()
	const accountID = "automation-cancel-account"
	if err := database.SaveWeixinAccount(store.WeixinAccount{
		ID: accountID, Label: "成员", UserID: "automation-user", Token: "token",
		BaseURL: "https://example.test", Enabled: true, ReplyContextToken: "reply-token",
		IgnoreBefore: now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SaveWeixinSettings(store.WeixinSettings{Enabled: true, IgnoreBefore: now}); err != nil {
		t.Fatal(err)
	}
	gateway := &blockingRecordingGateway{started: make(chan struct{}), release: make(chan struct{}), sent: make(chan string, 1)}
	application.weixin.gateway = gateway
	application.weixin.start()
	account, err := database.GetWeixinAccount(accountID)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- application.weixin.sendChunks(account, account.UserID, account.ReplyContextToken, "automation result")
	}()
	select {
	case <-gateway.started:
	case <-time.After(2 * time.Second):
		t.Fatal("automation send did not reach the gateway")
	}
	application.weixin.stopAccount(accountID)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("automation cancellation error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("automation send did not observe account cancellation")
	}
	select {
	case text := <-gateway.sent:
		t.Fatalf("stopped account sent automation text: %q", text)
	default:
	}

	// QR 重新绑定会直接把持久化开关改回启用，然后只重启该成员。
	// 内存中的 channelStopped 不能让这个合法重启永久失效。
	application.weixin.stopAll()
	application.weixin.restartAccount(accountID)
	if _, ok := application.weixin.outboundContext(accountID); !ok {
		t.Fatal("persisted enabled channel did not restart the account outbound context")
	}
}

func TestAutomationResultIsPushedToSelectedMembers(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "beaver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	environment, err := appenv.Open(appenv.Config{Home: filepath.Join(t.TempDir(), "home")})
	if err != nil {
		t.Fatal(err)
	}
	application := newTestServer(t, database, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, environment)
	gateway := &recordingGateway{sent: make(chan string, 4), tokens: make(chan string, 4)}
	application.weixin.gateway = gateway
	defer application.Shutdown(context.Background())
	now := time.Now()
	for _, user := range []string{"user-a", "user-b"} {
		if err := database.SaveWeixinAccount(store.WeixinAccount{ID: weixinMemberID(user), Label: user, UserID: user, Token: "t", BaseURL: "https://x", Enabled: true, ReplyContextToken: "reply-" + user, IgnoreBefore: now, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.SaveWeixinSettings(store.WeixinSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	application.weixin.start()
	task := store.AutomationTask{Name: "每日巡检", NotifyWeixin: []string{weixinMemberID("user-a")}}
	application.notifyAutomationMembers(task, "⏰ 定时任务完成 · 每日巡检\n\n一切正常")
	select {
	case got := <-gateway.sent:
		if !strings.HasPrefix(got, "user-a|") || !strings.Contains(got, "一切正常") {
			t.Fatalf("通知内容或对象错误: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("没有收到定时任务通知")
	}
	select {
	case token := <-gateway.tokens:
		if token != "reply-user-a" {
			t.Fatalf("定时任务没有使用成员最近的回复凭据: %q", token)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("没有记录定时任务回复凭据")
	}
	select {
	case got := <-gateway.sent:
		t.Fatalf("未勾选的成员不应收到通知: %q", got)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestLongTaskPushesProgressWithoutRepeating(t *testing.T) {
	oldInterval, oldMax := weixinProgressInterval, weixinProgressMax
	weixinProgressInterval, weixinProgressMax = 30*time.Millisecond, 2
	defer func() { weixinProgressInterval, weixinProgressMax = oldInterval, oldMax }()
	database, err := store.Open(filepath.Join(t.TempDir(), "beaver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	environment, err := appenv.Open(appenv.Config{Home: filepath.Join(t.TempDir(), "home")})
	if err != nil {
		t.Fatal(err)
	}
	application := newTestServer(t, database, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, environment)
	gateway := &recordingGateway{sent: make(chan string, 8)}
	application.weixin.gateway = gateway
	defer application.Shutdown(context.Background())
	now := time.Now()
	accountID := weixinMemberID("user-a")
	if err := database.SaveWeixinAccount(store.WeixinAccount{ID: accountID, Label: "a", UserID: "user-a", Token: "t", BaseURL: "https://x", Enabled: true, IgnoreBefore: now, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	application.tasks.set("s1", "tok", func() {})
	application.tasks.setProgress("s1", "Codex · 运行命令")
	done := make(chan struct{})
	go func() {
		application.weixin.waitWithProgress(accountID, "s1")
		close(done)
	}()
	select {
	case got := <-gateway.sent:
		if !strings.Contains(got, "运行命令") {
			t.Fatalf("进度内容错误: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("没有收到进度推送")
	}
	select {
	case got := <-gateway.sent:
		t.Fatalf("进度没变化不应重复推送: %q", got)
	case <-time.After(150 * time.Millisecond):
	}
	application.tasks.setProgress("s1", "Codex · 编辑文件")
	select {
	case got := <-gateway.sent:
		if !strings.Contains(got, "编辑文件") {
			t.Fatalf("进度内容错误: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("进度变化后应再次推送")
	}
	application.tasks.clear("s1", "tok")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("任务结束后应停止等待")
	}
}
