package knowledge

import (
	"strings"
	"testing"
)

func TestAddSearchDelete(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.Add("deploy.md", []byte("# 部署\n\n生产环境使用 docker compose 部署，数据目录挂载到 /data。")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("cron.txt", []byte("定时任务按 cron 表达式触发。")); err != nil {
		t.Fatal(err)
	}
	docs, _ := store.List()
	if len(docs) != 2 {
		t.Fatalf("docs=%+v", docs)
	}
	results, err := store.Search("docker 部署 数据目录", 3)
	if err != nil || len(results) == 0 || results[0].Source != "deploy.md" {
		t.Fatalf("应命中 deploy.md（且只暴露文件名）: %+v err=%v", results, err)
	}
	if err := store.Delete("deploy.md"); err != nil {
		t.Fatal(err)
	}
	if results, _ := store.Search("docker 部署", 3); len(results) != 0 {
		t.Fatalf("删除后不应再命中: %+v", results)
	}
}

func TestAddRejectsUnsafeOrUnsupported(t *testing.T) {
	store := New(t.TempDir())
	for _, name := range []string{"../x.md", "a/b.md", ".hidden.md", "", "doc.pdf", `a\b.md`} {
		if _, err := store.Add(name, []byte("x")); err == nil {
			t.Fatalf("文件名 %q 应被拒绝", name)
		}
	}
	if _, err := store.Add("a.md", nil); err == nil {
		t.Fatal("空内容应被拒绝")
	}
	if _, err := store.Add("big.md", []byte(strings.Repeat("x", maxFileBytes+1))); err == nil {
		t.Fatal("超大文档应被拒绝")
	}
}

func TestSearchOnMissingDirectory(t *testing.T) {
	results, err := New(t.TempDir()+"/none").Search("x", 3)
	if err != nil || len(results) != 0 {
		t.Fatalf("目录不存在应返回空结果: %v %v", results, err)
	}
}
