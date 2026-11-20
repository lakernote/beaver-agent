package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersonalMemoryIsIsolatedAndTeamIsShared(t *testing.T) {
	store := New(t.TempDir())
	if err := store.Save("alice", ScopePersonal, "Alice 喜欢用中文回复，并且偏好简短"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("bob", ScopePersonal, "Bob 的部署环境是 staging"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("alice", ScopeTeam, "团队约定：发布前必须跑 make test"); err != nil {
		t.Fatal(err)
	}

	notes, err := store.Search("alice", "回复 中文", 5)
	if err != nil || len(notes) == 0 || notes[0].Scope != ScopePersonal {
		t.Fatalf("alice 应读到自己的记忆: %+v err=%v", notes, err)
	}
	notes, _ = store.Search("bob", "回复 中文", 5)
	if len(notes) != 0 {
		t.Fatalf("bob 不应读到 alice 的个人记忆: %+v", notes)
	}
	notes, _ = store.Search("bob", "发布 make test", 5)
	if len(notes) == 0 || notes[0].Scope != ScopeTeam {
		t.Fatalf("团队记忆应对所有人可见: %+v", notes)
	}
}

func TestSaveRejectsBadInputAndDeduplicates(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	if err := store.Save("a", ScopePersonal, "  "); err == nil {
		t.Fatal("空记忆应被拒绝")
	}
	if err := store.Save("a", ScopePersonal, strings.Repeat("长", maxNoteRunes+1)); err == nil {
		t.Fatal("超长记忆应被拒绝")
	}
	if err := store.Save("a", "other", "x"); err == nil {
		t.Fatal("未知范围应被拒绝")
	}
	for i := 0; i < 2; i++ {
		if err := store.Save("a", ScopePersonal, "重复的事实"); err != nil {
			t.Fatal(err)
		}
	}
	notes, _ := readNotes(filepath.Join(root, "people", "a", "notes.md"))
	if len(notes) != 1 {
		t.Fatalf("相同记忆不应重复保存: %v", notes)
	}
}

func TestOwnerCannotEscapeDirectory(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	if err := store.Save("../../evil", ScopePersonal, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "people", "______evil", "notes.md")); err != nil {
		t.Fatalf("owner 应被净化为安全目录名: %v", err)
	}
	if err := store.Save("", ScopePersonal, "管理员的记忆"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "people", "admin", "notes.md")); err != nil {
		t.Fatalf("空 owner 应归入 admin: %v", err)
	}
}

func TestForget(t *testing.T) {
	store := New(t.TempDir())
	_ = store.Save("a", ScopePersonal, "旧的服务器地址是 10.0.0.1")
	_ = store.Save("a", ScopePersonal, "偏好深色主题")
	removed, err := store.Forget("a", ScopePersonal, "10.0.0.1")
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if notes, _ := store.Search("a", "服务器地址", 5); len(notes) != 0 {
		t.Fatalf("已遗忘的记忆不应再被检索到: %+v", notes)
	}
	if _, err := store.Forget("a", ScopePersonal, " "); err == nil {
		t.Fatal("空关键词应被拒绝")
	}
}
