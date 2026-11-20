package server

import (
	"net/http"
	"path/filepath"
	"sync"

	"github.com/lakernote/beaver-agent/internal/knowledge"
)

// 知识库文档存放在 ~/.beaver/knowledge，团队共享。
func (server *Server) knowledgeStore() *knowledge.Store {
	return knowledge.New(filepath.Join(server.env.Home(), "knowledge"))
}

// 写入文档需要串行化，避免并发上传时“数量上限”检查被绕过。
var knowledgeWriteMu sync.Mutex

func (server *Server) listKnowledge(response http.ResponseWriter, _ *http.Request) {
	docs, err := server.knowledgeStore().List()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	if docs == nil {
		docs = []knowledge.Doc{}
	}
	writeJSON(response, http.StatusOK, docs)
}

// uploadKnowledge 接收文本文档：前端用 FileReader 读出内容后以 JSON 提交，无需 multipart。
func (server *Server) uploadKnowledge(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	knowledgeWriteMu.Lock()
	defer knowledgeWriteMu.Unlock()
	name, err := server.knowledgeStore().Add(input.Name, []byte(input.Content))
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"name": name})
}

func (server *Server) deleteKnowledge(response http.ResponseWriter, request *http.Request) {
	if err := server.knowledgeStore().Delete(request.PathValue("name")); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	response.WriteHeader(http.StatusNoContent)
}
