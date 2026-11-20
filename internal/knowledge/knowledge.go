// Package knowledge 是团队共享的知识库：用户上传文档，Agent 通过检索使用它们。
//
// 与 internal/memory 的区别：记忆是 Agent 在对话中写入的短事实；知识库是人上传的长文档，
// 需要先切块再检索。两者都复用 internal/rag 的 BM25，不依赖向量库。
//
// 文件直接保存在 <root>/ 下，文件名即文档名。目前只支持 .md 和 .txt。
package knowledge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lakernote/beaver-agent/internal/rag"
)

const (
	maxFileBytes = 2 << 20 // 单个文档上限 2MB
	maxFiles     = 200     // 文档数量上限，防止磁盘被占满
)

// Doc 是知识库中的一份文档。
type Doc struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Store 管理知识库目录。零值不可用，请用 New。
type Store struct{ root string }

// New 返回以 root 为根目录的知识库，目录在第一次写入时才创建。
func New(root string) *Store { return &Store{root: root} }

// Add 保存（或覆盖）一份文档，返回最终使用的文件名。
func (store *Store) Add(name string, content []byte) (string, error) {
	name, err := cleanName(name)
	if err != nil {
		return "", err
	}
	if len(content) == 0 {
		return "", errors.New("文档内容为空")
	}
	if len(content) > maxFileBytes {
		return "", fmt.Errorf("文档过大，最多 %d MB", maxFileBytes>>20)
	}
	if docs, _ := store.List(); len(docs) >= maxFiles && !contains(docs, name) {
		return "", fmt.Errorf("知识库最多 %d 份文档，请先删除不用的", maxFiles)
	}
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		return "", err
	}
	return name, os.WriteFile(filepath.Join(store.root, name), content, 0o600)
}

// List 按名称返回所有文档。
func (store *Store) List() ([]Doc, error) {
	entries, err := os.ReadDir(store.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var docs []Doc
	for _, entry := range entries {
		if entry.IsDir() || !supported(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		docs = append(docs, Doc{Name: entry.Name(), Size: info.Size(), UpdatedAt: info.ModTime()})
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Name < docs[j].Name })
	return docs, nil
}

// Delete 删除一份文档；文档不存在时不报错。
func (store *Store) Delete(name string) error {
	name, err := cleanName(name)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(store.root, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Search 在所有文档中检索最相关的 limit 个片段。
// 每次检索都重新建索引：个人/团队规模的知识库（几百份文档）只需几十毫秒，
// 换来的是没有缓存失效问题，代码也最简单。文档很多时再考虑缓存。
func (store *Store) Search(query string, limit int) ([]rag.Result, error) {
	if _, err := os.Stat(store.root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	index, err := rag.BuildFromDir(store.root)
	if err != nil {
		return nil, err
	}
	results := index.Search(query, limit)
	for i := range results {
		results[i].Source = filepath.Base(results[i].Source) // 只展示文件名，不泄露服务器路径
	}
	return results, nil
}

// cleanName 只允许“单层文件名 + 支持的扩展名”，拒绝路径分隔符，防止越界写入。
func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\`) {
		return "", errors.New("文件名无效")
	}
	if !supported(name) {
		return "", errors.New("目前只支持 .md 和 .txt 文档")
	}
	return name, nil
}

func supported(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".txt"
}

func contains(docs []Doc, name string) bool {
	for _, doc := range docs {
		if doc.Name == name {
			return true
		}
	}
	return false
}
