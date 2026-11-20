// Package search 是一个用于学习的最小 RAG（检索增强生成）实现。
//
// RAG 的本质只有三步：
//
//  1. 切块（Chunk）：把长文档切成模型读得动的小段；
//  2. 检索（Search）：根据问题找出最相关的几段；
//  3. 增强（Augment）：把这几段连同出处放进提示词，让模型据此回答。
//
// 这里的检索用 BM25（经典的关键词打分算法），不需要 Embedding 服务，纯标准库即可运行。
// 想升级成“语义检索”，只需把 Search 里的打分换成向量余弦相似度，其余流程完全不变，
// 详见 docs/learn-rag.md。
package search

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Chunk 是检索的最小单位。Source 和 Line 用于给回答附上可核对的出处。
type Chunk struct {
	Source string // 文件路径
	Line   int    // 该块在文件中的起始行号（从 1 开始）
	Text   string

	terms map[string]int // 词 -> 出现次数，建索引时预先算好
	size  int            // 该块的总词数
}

// Result 是一次检索命中。
type Result struct {
	Chunk
	Score float64
}

// Index 是内存中的倒排统计，足够支撑几千个文件的个人知识库。
type Index struct {
	chunks    []Chunk
	docFreq   map[string]int // 词 -> 有多少个块包含它
	avgLength float64
}

// maxChunkRunes 控制块大小：太大会稀释相关性，太小会丢失上下文。
const maxChunkRunes = 800

// BuildFromDir 递归读取目录下的 .md / .txt 文件并建立索引。
func BuildFromDir(root string) (*Index, error) {
	var chunks []Chunk
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".md" && ext != ".txt" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil // 单个文件读不了不应让整个索引失败
		}
		chunks = append(chunks, SplitText(path, string(data))...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return NewIndex(chunks), nil
}

// SplitText 按空行切成段落，再把相邻段落合并到不超过 maxChunkRunes。
// 按段落而不是固定字数切，可以避免把一句话拦腰截断。
func SplitText(source, text string) []Chunk {
	var chunks []Chunk
	var current strings.Builder
	currentLine, line := 1, 1

	flush := func() {
		if body := strings.TrimSpace(current.String()); body != "" {
			chunks = append(chunks, Chunk{Source: source, Line: currentLine, Text: body})
		}
		current.Reset()
	}
	for _, paragraph := range strings.Split(text, "\n\n") {
		if current.Len() == 0 {
			currentLine = line
		}
		if len([]rune(current.String()))+len([]rune(paragraph)) > maxChunkRunes {
			flush()
			currentLine = line
		}
		current.WriteString(paragraph)
		current.WriteString("\n\n")
		line += strings.Count(paragraph, "\n") + 2
	}
	flush()
	return chunks
}

// NewIndex 统计每个块的词频和全局文档频率。
func NewIndex(chunks []Chunk) *Index {
	index := &Index{chunks: make([]Chunk, len(chunks)), docFreq: map[string]int{}}
	total := 0
	for i, chunk := range chunks {
		chunk.terms = map[string]int{}
		for _, term := range Tokenize(chunk.Text) {
			chunk.terms[term]++
			chunk.size++
		}
		for term := range chunk.terms {
			index.docFreq[term]++
		}
		total += chunk.size
		index.chunks[i] = chunk
	}
	if len(chunks) > 0 {
		index.avgLength = float64(total) / float64(len(chunks))
	}
	return index
}

// Search 返回得分最高的 limit 个块。
//
// BM25 的直觉：
//   - 词在某块里出现越多越相关（但有边际递减，由 k1 控制）；
//   - 词在越少的块里出现越“稀有”越有区分度（IDF）；
//   - 太长的块要打折（由 b 控制），否则长文占便宜。
func (index *Index) Search(query string, limit int) []Result {
	const k1, b = 1.5, 0.75
	if limit <= 0 {
		limit = 5
	}
	queryTerms := Tokenize(query)
	n := float64(len(index.chunks))
	var results []Result
	for _, chunk := range index.chunks {
		score := 0.0
		for _, term := range queryTerms {
			tf := float64(chunk.terms[term])
			if tf == 0 {
				continue
			}
			df := float64(index.docFreq[term])
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			score += idf * tf * (k1 + 1) / (tf + k1*(1-b+b*float64(chunk.size)/index.avgLength))
		}
		if score > 0 {
			results = append(results, Result{Chunk: chunk, Score: score})
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

// Tokenize 把文本拆成检索用的词：英文/数字按单词（小写），中文按相邻两个字（bigram）。
// 中文没有空格分词，用 bigram 是不依赖词典的朴素做法，对检索已经够用。
func Tokenize(text string) []string {
	var terms []string
	var word []rune
	var han []rune
	flushWord := func() {
		if len(word) > 0 {
			terms = append(terms, strings.ToLower(string(word)))
			word = word[:0]
		}
	}
	flushHan := func() {
		switch {
		case len(han) == 1:
			terms = append(terms, string(han))
		case len(han) > 1:
			for i := 0; i+1 < len(han); i++ {
				terms = append(terms, string(han[i:i+2]))
			}
		}
		han = han[:0]
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			han = append(han, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushHan()
			word = append(word, r)
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()
	return terms
}
