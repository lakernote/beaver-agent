package search

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTokenizeMixedLanguages(t *testing.T) {
	got := Tokenize("Beaver 定时任务 v2")
	want := []string{"beaver", "定时", "时任", "任务", "v2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSplitTextKeepsLinesAndSize(t *testing.T) {
	text := "第一段\n\n" + strings.Repeat("长", 700) + "\n\n" + strings.Repeat("尾", 300)
	chunks := SplitText("a.md", text)
	if len(chunks) < 2 {
		t.Fatalf("应当被切成多块: %d", len(chunks))
	}
	if chunks[0].Line != 1 {
		t.Fatalf("首块行号应为 1: %d", chunks[0].Line)
	}
	for _, chunk := range chunks {
		if n := len([]rune(chunk.Text)); n > maxChunkRunes+100 {
			t.Fatalf("块过大: %d", n)
		}
	}
}

func TestSearchRanksRelevantChunkFirst(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"cron.md":   "# 定时任务\n\n定时任务按 cron 表达式触发，结果会推送到微信。",
		"weixin.md": "# 微信\n\n扫码绑定后即可远程下发任务。",
		"note.txt":  "完全无关的内容：今天天气不错。",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	index, err := BuildFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	results := index.Search("定时任务怎么触发", 2)
	if len(results) == 0 || filepath.Base(results[0].Source) != "cron.md" {
		t.Fatalf("应命中 cron.md: %+v", results)
	}
	if got := index.Search("zzzz不存在", 3); len(got) != 0 {
		t.Fatalf("无关查询不应有结果: %+v", got)
	}
}
