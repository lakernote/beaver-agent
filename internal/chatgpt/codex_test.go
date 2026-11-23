package chatgpt

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lakernote/beaver-agent/internal/codexruntime"
)

func TestCodexLoginCommitsOnlyToSelectedAccount(t *testing.T) {
	client := &CodexClient{root: t.TempDir()}
	ctx := context.Background()
	identity := codexruntime.ChatGPTAccount{Type: "chatgpt", Email: "member@example.test", PlanType: "enterprise"}
	stage := func(content string) string {
		dir, err := os.MkdirTemp(client.root, ".login-")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	readAuth := func(account Account) string {
		data, err := os.ReadFile(filepath.Join(client.root, account.ID, "auth.json"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	first, err := client.saveLogin(ctx, stage("first"), randomSecret(), Account{}, identity, nil)
	if err != nil || !first.SignedIn {
		t.Fatalf("first login: %v", err)
	}
	second, err := client.saveLogin(ctx, stage("second"), randomSecret(), Account{}, identity, nil)
	if err != nil || !second.SignedIn {
		t.Fatalf("second login: %v", err)
	}
	personal := identity
	personal.PlanType = "free"
	if _, err := client.saveLogin(ctx, stage("wrong-workspace"), first.ID, first, personal, nil); err == nil {
		t.Fatal("reauthorization unexpectedly replaced the selected workspace")
	}
	if got := readAuth(first); got != "first" {
		t.Fatalf("selected account changed after failed login: %q", got)
	}
	if _, err := client.saveLogin(ctx, stage("reauthorized"), first.ID, first, identity, nil); err != nil {
		t.Fatal(err)
	}
	if got := readAuth(first); got != "reauthorized" || readAuth(second) != "second" {
		t.Fatal("reauthorization must only replace credentials for the selected account")
	}
	if _, err := client.SignOut(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if account, err := client.Account(first.ID); err != nil || account.SignedIn || readAuth(second) != "second" {
		t.Fatalf("sign-out must only clear the selected account: %v", err)
	}
}
