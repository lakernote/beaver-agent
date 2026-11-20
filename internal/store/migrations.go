package store

import "fmt"

func (store *Store) init() error {
	schema := `
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value_json BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS auth_users (
  username TEXT PRIMARY KEY,
  password_salt BLOB NOT NULL,
  password_hash BLOB NOT NULL,
  password_iterations INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS skills (
  name TEXT PRIMARY KEY,
  description TEXT NOT NULL,
  content TEXT NOT NULL,
  enabled INTEGER NOT NULL,
  builtin INTEGER NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS mcp (
  id TEXT PRIMARY KEY,
  config_json BLOB NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  is_default INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS project_directories (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  path TEXT NOT NULL,
  position INTEGER NOT NULL,
  PRIMARY KEY(project_id, path)
);
CREATE TABLE IF NOT EXISTS automation_tasks (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  prompt TEXT NOT NULL,
  project_id TEXT NOT NULL,
  workspace TEXT NOT NULL,
  profile_id TEXT NOT NULL DEFAULT '',
  trigger_type TEXT NOT NULL DEFAULT 'manual',
  interval_minutes INTEGER NOT NULL DEFAULT 0,
  repeat TEXT NOT NULL DEFAULT 'interval',
  schedule_time TEXT NOT NULL DEFAULT '',
  schedule_weekday INTEGER NOT NULL DEFAULT 1,
  schedule_enabled INTEGER NOT NULL DEFAULT 0,
  cron TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  next_run_at TEXT NOT NULL DEFAULT '',
  last_run_at TEXT NOT NULL DEFAULT '',
  last_status TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  last_session_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
	  project_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
	  error TEXT NOT NULL,
	  runtime TEXT NOT NULL DEFAULT 'beaver',
	  channel TEXT NOT NULL DEFAULT 'web',
  profile_id TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL,
  reasoning_effort TEXT NOT NULL DEFAULT '',
  pending_model_json BLOB NOT NULL DEFAULT X'',
  permissions_json BLOB NOT NULL DEFAULT '{}',
  workspace TEXT NOT NULL DEFAULT '',
  source_workspace TEXT NOT NULL DEFAULT '',
  worktree_branch TEXT NOT NULL DEFAULT '',
  workspace_notice TEXT NOT NULL DEFAULT '',
  owner_id TEXT NOT NULL DEFAULT '', -- 会话所属成员（微信账号 ID），个人记忆按此隔离
  response_id TEXT NOT NULL,
  provider_key TEXT NOT NULL,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cached_tokens INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens INTEGER NOT NULL DEFAULT 0,
  model_duration_ms INTEGER NOT NULL DEFAULT 0,
  tool_duration_ms INTEGER NOT NULL DEFAULT 0,
  model_calls INTEGER NOT NULL DEFAULT 0,
  tool_calls INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  tool_calls_json BLOB NOT NULL,
	tool_result_json BLOB NOT NULL DEFAULT '',
  tool_call_id TEXT NOT NULL,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(session_id, seq)
);
CREATE TABLE IF NOT EXISTS attachments (
  id TEXT PRIMARY KEY,
  message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  mime_type TEXT NOT NULL,
  kind TEXT NOT NULL,
  size INTEGER NOT NULL,
  data BLOB NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  event_json BLOB NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(session_id, seq)
);
CREATE TABLE IF NOT EXISTS tool_operations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  runtime TEXT NOT NULL,
  turn INTEGER NOT NULL,
  step INTEGER NOT NULL DEFAULT 0,
  activity_id TEXT NOT NULL,
  name TEXT NOT NULL,
  activity_kind TEXT NOT NULL DEFAULT '',
  activity_source TEXT NOT NULL DEFAULT '',
  display_name TEXT NOT NULL DEFAULT '',
  input TEXT NOT NULL DEFAULT '',
  output TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  guarantee TEXT NOT NULL DEFAULT 'observed',
  error TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL,
  completed_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  UNIQUE(session_id, runtime, turn, activity_id)
);
CREATE TABLE IF NOT EXISTS model_capability_tests (
  fingerprint TEXT PRIMARY KEY,
  tested_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS compactions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  summary TEXT NOT NULL,
  through_message_id INTEGER NOT NULL,
  split_turn INTEGER NOT NULL DEFAULT 0,
  source_messages INTEGER NOT NULL,
  compacted_messages INTEGER NOT NULL,
  usage_json BLOB NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(session_id, seq)
);
CREATE TABLE IF NOT EXISTS weixin_accounts (
  id TEXT PRIMARY KEY,
  label TEXT NOT NULL,
  user_id TEXT NOT NULL UNIQUE,
  token TEXT NOT NULL,
  base_url TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  sync_buf TEXT NOT NULL DEFAULT '',
  current_session_id TEXT NOT NULL DEFAULT '',
  project_id TEXT NOT NULL DEFAULT '',
  ignore_before TEXT NOT NULL,
  last_seen_at TEXT NOT NULL DEFAULT '',
  last_message_at TEXT NOT NULL DEFAULT '',
  last_sequence INTEGER NOT NULL DEFAULT 0,
  pending_message_id INTEGER NOT NULL DEFAULT 0,
  delivered_message_id INTEGER NOT NULL DEFAULT 0,
  pending_context_token TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
	CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_projects_default ON projects(is_default DESC, name);
CREATE INDEX IF NOT EXISTS idx_automation_due ON automation_tasks(enabled, trigger_type, next_run_at);
	CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, seq);
	CREATE INDEX IF NOT EXISTS idx_messages_session_id ON messages(session_id, id);
	CREATE INDEX IF NOT EXISTS idx_attachments_message ON attachments(message_id);
	CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id, seq);
	CREATE INDEX IF NOT EXISTS idx_events_session_id ON events(session_id, id);
CREATE INDEX IF NOT EXISTS idx_events_created ON events(created_at);
CREATE INDEX IF NOT EXISTS idx_tool_operations_session ON tool_operations(session_id, turn, id);
CREATE INDEX IF NOT EXISTS idx_tool_operations_status ON tool_operations(status, updated_at);
CREATE INDEX IF NOT EXISTS idx_model_capability_tests_tested ON model_capability_tests(tested_at);
CREATE INDEX IF NOT EXISTS idx_compactions_session ON compactions(session_id, seq);
CREATE INDEX IF NOT EXISTS idx_weixin_accounts_enabled ON weixin_accounts(enabled, updated_at);
`
	if _, err := store.db.Exec(schema); err != nil {
		return fmt.Errorf("初始化 SQLite: %w", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key='model'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if err := store.SaveModelSettings(DefaultModelSettings()); err != nil {
			return err
		}
	}
	return store.EnsureAdmin()
}
