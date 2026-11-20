package store

import (
	"os"
	"path/filepath"
	"testing"
)

// testdata/legacy_ea_prefix.db 由带 ea_ 前缀的旧版本创建，里面有一条 settings 记录。
func TestOpenMigratesLegacyTableNames(t *testing.T) {
	data, err := os.ReadFile("testdata/legacy_ea_prefix.db")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "beaver.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ { // 第二次打开验证幂等
		s, err := Open(path)
		if err != nil {
			t.Fatalf("第 %d 次打开: %v", round+1, err)
		}
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key='marker'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("数据应随表保留: count=%d err=%v", count, err)
		}
		var legacy int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'ea\_%' ESCAPE '\' OR name LIKE 'idx\_ea\_%' ESCAPE '\'`).Scan(&legacy)
		if legacy != 0 {
			t.Fatalf("不应残留旧名字: %d", legacy)
		}
		s.Close()
	}
}
