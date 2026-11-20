package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/lakernote/beaver-agent/internal/agent"
	"github.com/lakernote/beaver-agent/internal/memory"
	"github.com/lakernote/beaver-agent/internal/permissions"
)

func runMemoryTool(t *testing.T, tools []agent.Tool, name, args string) string {
	t.Helper()
	for _, tool := range tools {
		if tool.Spec.Name == name {
			out, err := tool.Run(context.Background(), []byte(args))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return out
		}
	}
	t.Fatalf("缺少工具 %s", name)
	return ""
}

func TestMemoryToolsAreBoundToOwner(t *testing.T) {
	store := memory.New(t.TempDir())
	alice := memoryTools(Memory{Store: store, Owner: "alice"})
	bob := memoryTools(Memory{Store: store, Owner: "bob"})

	runMemoryTool(t, alice, "memory_save", `{"text":"Alice 的服务器在上海机房"}`)
	if got := runMemoryTool(t, alice, "memory_search", `{"query":"服务器 机房"}`); !strings.Contains(got, "上海") {
		t.Fatalf("alice 应能检索到: %q", got)
	}
	if got := runMemoryTool(t, bob, "memory_search", `{"query":"服务器 机房"}`); !strings.Contains(got, "没有找到") {
		t.Fatalf("bob 不应看到 alice 的个人记忆: %q", got)
	}
	if got := runMemoryTool(t, alice, "memory_forget", `{"keyword":"上海"}`); !strings.Contains(got, "1") {
		t.Fatalf("应删除 1 条: %q", got)
	}
}

func TestCatalogIncludesMemoryGroupOnlyWhenProvided(t *testing.T) {
	env := testEnvironment(t, t.TempDir())
	has := func(tools []agent.Tool) bool {
		for _, tool := range tools {
			if tool.Spec.Name == "memory_save" && tool.Spec.Group == groupMemory {
				return true
			}
		}
		return false
	}
	if has(CatalogWithPermissions(env, nil, permissions.Default())) {
		t.Fatal("未提供记忆存储时不应暴露记忆工具")
	}
	withMemory := CatalogWithExtras(env, nil, permissions.Default(), Extras{Memory: &Memory{Store: memory.New(t.TempDir()), Owner: "a"}})
	if !has(withMemory) {
		t.Fatal("提供记忆存储后应暴露记忆工具")
	}
}
