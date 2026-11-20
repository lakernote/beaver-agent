package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/lakernote/beaver-agent/internal/knowledge"
)

func TestKnowledgeSearchTool(t *testing.T) {
	store := knowledge.New(t.TempDir())
	if _, err := store.Add("deploy.md", []byte("# 发布流程\n发布前必须先跑回归测试，然后灰度发布。")); err != nil {
		t.Fatal(err)
	}
	tool := knowledgeSearchTool(store)
	out, err := tool.Run(context.Background(), []byte(`{"query":"回归测试"}`))
	if err != nil || !strings.Contains(out, "deploy.md") {
		t.Fatalf("应带出处: %q %v", out, err)
	}
	out, _ = tool.Run(context.Background(), []byte(`{"query":"zzzz"}`))
	if !strings.Contains(out, "没有找到") {
		t.Fatalf("无命中应明说: %q", out)
	}
}
