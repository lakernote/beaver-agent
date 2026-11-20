// Package memory 是 Agent 的长期记忆：把值得记住的事实存成 Markdown，需要时再检索。
//
// 设计刻意保持朴素：
//   - 一条记忆 = 文件里的一个段落（以空行分隔），人可以直接打开、编辑、删除；
//   - 检索复用 internal/rag（BM25），不依赖向量库；
//   - 按成员隔离：每个成员一个文件，另有一份团队共享文件。
//
// 目录结构：
//
//	<root>/team/notes.md            团队共享记忆，所有成员可读可写
//	<root>/people/<owner>/notes.md  个人记忆，只有该成员的会话能读写
//
// owner 是会话所属成员（微信账号 ID）；Web 和定时任务没有 owner，统一归入 "admin"。
package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lakernote/beaver-agent/internal/rag"
)

// 作用域：个人记忆或团队记忆。
const (
	ScopePersonal = "personal"
	ScopeTeam     = "team"
)

const (
	adminOwner   = "admin"
	maxNoteRunes = 500       // 单条记忆上限：记忆应该是简短事实，不是文档
	maxFileBytes = 256 << 10 // 单个记忆文件上限，防止被无限写入
)

// Store 管理磁盘上的记忆文件。零值不可用，请用 New。
type Store struct {
	root string
	mu   sync.Mutex // 记忆写入频率很低，一把锁足够
}

// Note 是一次检索命中的记忆。
type Note struct {
	Scope string
	Text  string
	Score float64
}

// New 返回以 root 为根目录的记忆存储。目录在第一次写入时才创建。
func New(root string) *Store { return &Store{root: root} }

// Save 追加一条记忆，返回实际写入的文本。相同文本不会重复保存。
func (store *Store) Save(owner, scope, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("记忆内容不能为空")
	}
	if len([]rune(text)) > maxNoteRunes {
		return fmt.Errorf("单条记忆最多 %d 个字，请提炼成简短事实", maxNoteRunes)
	}
	path, err := store.file(owner, scope)
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	notes, err := readNotes(path)
	if err != nil {
		return err
	}
	for _, note := range notes {
		if stripDate(note) == text {
			return nil
		}
	}
	line := time.Now().Format("2006-01-02") + " " + strings.ReplaceAll(text, "\n\n", "\n")
	notes = append(notes, line)
	return writeNotes(path, notes)
}

// Search 在个人记忆和团队记忆中检索，返回最相关的 limit 条。
func (store *Store) Search(owner, query string, limit int) ([]Note, error) {
	var chunks []rag.Chunk
	for _, scope := range []string{ScopePersonal, ScopeTeam} {
		path, err := store.file(owner, scope)
		if err != nil {
			return nil, err
		}
		notes, err := readNotes(path)
		if err != nil {
			return nil, err
		}
		for _, note := range notes {
			chunks = append(chunks, rag.Chunk{Source: scope, Text: note})
		}
	}
	var result []Note
	for _, hit := range rag.NewIndex(chunks).Search(query, limit) {
		result = append(result, Note{Scope: hit.Source, Text: hit.Text, Score: hit.Score})
	}
	return result, nil
}

// Forget 删除包含 keyword 的记忆，返回删除条数。用于纠正过时或错误的记忆。
func (store *Store) Forget(owner, scope, keyword string) (int, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return 0, errors.New("需要提供要删除的记忆关键词")
	}
	path, err := store.file(owner, scope)
	if err != nil {
		return 0, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	notes, err := readNotes(path)
	if err != nil {
		return 0, err
	}
	kept := notes[:0:0]
	for _, note := range notes {
		if !strings.Contains(note, keyword) {
			kept = append(kept, note)
		}
	}
	removed := len(notes) - len(kept)
	if removed == 0 {
		return 0, nil
	}
	return removed, writeNotes(path, kept)
}

// file 返回某个作用域对应的记忆文件路径。owner 会被限制为安全的目录名，防止 ../ 越界。
func (store *Store) file(owner, scope string) (string, error) {
	switch scope {
	case ScopeTeam:
		return filepath.Join(store.root, "team", "notes.md"), nil
	case ScopePersonal, "":
		return filepath.Join(store.root, "people", safeName(owner), "notes.md"), nil
	default:
		return "", fmt.Errorf("未知的记忆范围 %q，应为 personal 或 team", scope)
	}
}

// safeName 只保留字母、数字、连字符和下划线；空值归入管理员。
func safeName(owner string) string {
	var out strings.Builder
	for _, r := range owner {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out.WriteRune(r)
		default:
			out.WriteRune('_')
		}
	}
	if out.Len() == 0 {
		return adminOwner
	}
	return out.String()
}

func readNotes(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var notes []string
	for _, paragraph := range strings.Split(string(data), "\n\n") {
		if paragraph = strings.TrimSpace(paragraph); paragraph != "" {
			notes = append(notes, paragraph)
		}
	}
	return notes, nil
}

func writeNotes(path string, notes []string) error {
	body := strings.Join(notes, "\n\n")
	if len(body) > maxFileBytes {
		return errors.New("记忆已满，请先让 Agent 遗忘一些过时的内容")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body+"\n"), 0o600)
}

// stripDate 去掉保存时加的 "YYYY-MM-DD " 前缀，用于判断是否重复。
func stripDate(note string) string {
	if len(note) > 11 && note[4] == '-' && note[7] == '-' && note[10] == ' ' {
		return note[11:]
	}
	return note
}
