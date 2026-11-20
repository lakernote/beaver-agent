package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWeixinAccountsPersistSecretsAndSuppressPendingDelivery(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "beaver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Now().UTC()
	if err := database.SaveWeixinAccount(WeixinAccount{ID: "bot-1", Label: "小王", UserID: "user-secret", Token: "token-secret", BaseURL: "https://ilinkai.weixin.qq.com", Enabled: true, ProjectID: "project-1", IgnoreBefore: now, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetWeixinTask("bot-1", "session-1", 42, "context-secret", now, now); err != nil {
		t.Fatal(err)
	}
	account, err := database.GetWeixinAccount("bot-1")
	if err != nil {
		t.Fatal(err)
	}
	if account.Token != "token-secret" || account.UserID != "user-secret" || account.ProjectID != "project-1" || account.PendingMessageID != 42 || account.PendingContextToken != "context-secret" {
		t.Fatalf("微信绑定没有完整持久化: %+v", account)
	}
	if err := database.RecordWeixinMessage("bot-1", now.Add(30*time.Second), now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	account, err = database.GetWeixinAccount("bot-1")
	if err != nil || account.PendingMessageID != 42 || account.DeliveredMessageID != 0 || account.PendingContextToken != "context-secret" {
		t.Fatalf("仅保存媒体不应改变待回传任务: %+v err=%v", account, err)
	}
	account, err = database.UpdateWeixinAccount("bot-1", "值班小王", false, "project-2", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if account.Enabled || account.ProjectID != "project-2" || account.DeliveredMessageID != 42 || account.PendingContextToken != "" {
		t.Fatalf("停用成员应抑制待发送结果: %+v", account)
	}
	reenabledAt := now.Add(2 * time.Minute)
	account, err = database.UpdateWeixinAccount("bot-1", "值班小王", true, "", reenabledAt)
	if err != nil || !account.IgnoreBefore.Equal(reenabledAt) {
		t.Fatalf("重新启用应从当前时间接收消息: %+v err=%v", account, err)
	}
	account, err = database.UpdateWeixinAccount("bot-1", "夜班小王", true, "", now.Add(3*time.Minute))
	if err != nil || !account.IgnoreBefore.Equal(reenabledAt) {
		t.Fatalf("只修改备注不应重置消息边界: %+v err=%v", account, err)
	}
}

func TestMarkWeixinDeliveredOnlyUpdatesMatchingPendingMessage(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "beaver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Now().UTC()
	if err := database.SaveWeixinAccount(WeixinAccount{ID: "bot-cas", UserID: "user-cas", Token: "token", BaseURL: "https://example.test", Enabled: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetWeixinTask("bot-cas", "session", 42, "context-42", now, now); err != nil {
		t.Fatal(err)
	}
	marked, err := database.MarkWeixinDelivered("bot-cas", 41, now.Add(time.Second))
	if err != nil || marked {
		t.Fatalf("stale delivery marked=%v err=%v", marked, err)
	}
	account, err := database.GetWeixinAccount("bot-cas")
	if err != nil || account.DeliveredMessageID != 0 || account.PendingContextToken != "context-42" {
		t.Fatalf("stale delivery changed current generation: %+v err=%v", account, err)
	}
	marked, err = database.MarkWeixinDelivered("bot-cas", 42, now.Add(2*time.Second))
	if err != nil || !marked {
		t.Fatalf("current delivery marked=%v err=%v", marked, err)
	}
	account, err = database.GetWeixinAccount("bot-cas")
	if err != nil || account.DeliveredMessageID != 42 || account.PendingContextToken != "" {
		t.Fatalf("current delivery was not committed: %+v err=%v", account, err)
	}
	if err := database.SetWeixinTask("bot-cas", "session", 43, "context-43", now, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	marked, err = database.MarkWeixinDelivered("bot-cas", 42, now.Add(4*time.Second))
	if err != nil || marked {
		t.Fatalf("old generation overwrote new pending message: marked=%v err=%v", marked, err)
	}
	account, err = database.GetWeixinAccount("bot-cas")
	if err != nil || account.DeliveredMessageID != 42 || account.PendingContextToken != "context-43" {
		t.Fatalf("new pending generation was cleared: %+v err=%v", account, err)
	}
}

func TestWeixinSettingsDefaultDisabledAndEnableTimestamp(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "beaver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	settings, err := database.GetWeixinSettings()
	if err != nil || settings.Enabled {
		t.Fatalf("微信远程应默认停用: %+v err=%v", settings, err)
	}
	now := time.Now().UTC()
	if _, err := database.SaveWeixinSettings(WeixinSettings{Enabled: true, IgnoreBefore: now}); err != nil {
		t.Fatal(err)
	}
	settings, err = database.GetWeixinSettings()
	if err != nil || !settings.Enabled || !settings.IgnoreBefore.Equal(now) {
		t.Fatalf("微信远程设置没有保存: %+v err=%v", settings, err)
	}
}
