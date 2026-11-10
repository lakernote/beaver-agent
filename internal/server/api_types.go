package server

import (
	"github.com/lakernote/beaver-agent/internal/httpdto"
	"github.com/lakernote/beaver-agent/internal/store"
)

// Keep short private aliases inside the HTTP application while the actual JSON
// contract and persistence conversion live in the dedicated transport package.
type sessionView = httpdto.Session
type messageView = httpdto.Message
type eventView = httpdto.Event
type codexRequestView = httpdto.CodexRequest
type modelSettingsView = httpdto.ModelSettings

func publicSession(value store.Session) sessionView {
	return httpdto.SessionFromStore(value, workspaceIsolationLabel(value))
}

func publicMessages(values []store.Message) []messageView {
	return httpdto.MessagesFromStore(values)
}

func publicEvents(values []store.Event) []eventView {
	return httpdto.EventsFromStore(values)
}

func (server *Server) sessionView(value store.Session) sessionView {
	view := publicSession(value)
	if pending := server.tasks.pending(value.ID); pending != nil {
		view.CodexRequest = &codexRequestView{ID: pending.ID, Method: pending.Method, Params: pending.Params}
	}
	return view
}

func (server *Server) sessionViews(values []store.Session) []sessionView {
	result := make([]sessionView, 0, len(values))
	for _, value := range values {
		result = append(result, server.sessionView(value))
	}
	return result
}
