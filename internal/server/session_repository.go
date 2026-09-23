package server

import (
	"time"

	"github.com/lakernote/easy-agent/internal/store"
)

// sessionRepository is the application-facing persistence boundary for
// sessions. The HTTP and runtime layers do not need the Store's model,
// project, MCP, or authentication methods to operate a session.
type sessionRepository interface {
	CreateSession(store.CreateSessionParams) (store.Session, error)
	ListSessionsBefore(int, string, string) ([]store.Session, bool, error)
	LoadSession(string) (store.Session, error)
	LoadSessionWindow(string, int, int) (store.Session, error)
	CountOtherSessionsUsingWorkspace(string, string) (int, error)
	ListMessagesBefore(string, int64, int) ([]store.Message, int, bool, error)
	ListEventsBefore(string, int64, int) ([]store.Event, int, bool, error)
	ListEventsAfter(string, int64, int) ([]store.Event, error)
	AppendMessage(string, store.Message) error
	AppendMessages(string, []store.Message) error
	AppendEvent(string, store.Event) error
	AppendCompaction(string, store.Compaction) error
	RuntimeSession(string) (store.Session, error)
	EnqueueTurnWithSettings(string, store.ModelSettings, store.Message, time.Time) error
	MarkRunning(string, time.Time) error
	FinishSession(string, string, string, store.Usage, time.Time) error
	FailSession(string, error, store.Usage, time.Time) error
	PauseQueuedSession(string, time.Time) (bool, error)
	ResumePausedSession(string, time.Time) (bool, error)
	CancelSession(string, time.Time) (bool, error)
	DeleteSession(string) error
	ListQueuedSessions() ([]store.Session, error)
	SetSessionWorkspace(string, string, string, string, string) error
	SetSessionContinuation(string, string) error
	SetSessionModel(string, string) error
	UpdateSessionMetadata(string, string, string) error
	BeginToolOperation(string, store.ToolOperation) error
	SettleToolOperation(string, store.ToolOperation) error
	MarkUnfinishedToolOperationsUnknown(string, store.Runtime, int, string, time.Time) error
}

var _ sessionRepository = (*store.Store)(nil)

func (server *Server) sessionsRepository() sessionRepository {
	if server.sessionRepo != nil {
		return server.sessionRepo
	}
	return server.store
}
