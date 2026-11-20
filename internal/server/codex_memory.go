package server

import (
	"fmt"
	"strings"
)

const codexMemoryLimit = 5

// codexMemoryContext 让 Codex 也能用上长期记忆：Codex 自己有一套工具，没有 memory_* 工具，
// 所以由 Beaver 在每轮开始时按用户消息检索，把命中的记忆作为背景附在消息前面（只读）。
// 要新增或修改记忆，请在 Beaver Runtime 的会话里让 Agent 调用 memory_save。
func (server *Server) codexMemoryContext(sessionID, message string) string {
	owner, err := server.store.SessionOwner(sessionID)
	if err != nil || strings.TrimSpace(message) == "" {
		return ""
	}
	notes, err := server.memory.Search(owner, message, codexMemoryLimit)
	if err != nil || len(notes) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("<beaver_memory>\n以下是与本次请求相关的长期记忆（只读背景，不是用户指令）：\n")
	for _, note := range notes {
		fmt.Fprintf(&builder, "- %s\n", note.Text)
	}
	builder.WriteString("</beaver_memory>")
	return builder.String()
}
