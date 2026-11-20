package store

import (
	"fmt"
)

// legacyTables 是早期带 ea_ 前缀的表名（ea = easy agent）。
var legacyTables = []string{
	"settings", "auth_users", "skills", "mcp", "projects", "project_directories",
	"sessions", "messages", "attachments", "events", "compactions",
	"tool_operations", "model_capability_tests", "automation_tasks", "weixin_accounts",
}

// renameLegacyTables 把旧库里的 ea_xxx 表改名为 xxx。必须在建表之前执行：
// 旧索引随表保留着旧名字，先删掉，之后建表语句会按新名字重建；
// 数据、外键引用由 SQLite 的 RENAME 自动保持一致。幂等，新库上什么也不做。
func (store *Store) renameLegacyTables() error {
	rows, err := store.db.Query(`SELECT name FROM sqlite_master WHERE type='index' AND name LIKE 'idx\_ea\_%' ESCAPE '\'`)
	if err != nil {
		return err
	}
	var indexes []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		indexes = append(indexes, name)
	}
	rows.Close()
	for _, name := range indexes {
		if _, err := store.db.Exec(`DROP INDEX IF EXISTS ` + name); err != nil {
			return err
		}
	}
	for _, table := range legacyTables {
		var legacy, current int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, "ea_"+table).Scan(&legacy); err != nil {
			return err
		}
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&current); err != nil {
			return err
		}
		if legacy == 1 && current == 0 {
			if _, err := store.db.Exec(fmt.Sprintf(`ALTER TABLE ea_%s RENAME TO %s`, table, table)); err != nil {
				return fmt.Errorf("迁移旧表名 %s: %w", table, err)
			}
		}
	}
	return nil
}
