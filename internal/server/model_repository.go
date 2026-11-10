package server

import (
	"time"

	"github.com/lakernote/beaver-agent/internal/store"
)

// modelRepository is the persistence surface required by model configuration
// and capability verification. The HTTP/application layer should not need to
// know that these methods currently happen to live on the SQLite Store.
type modelRepository interface {
	GetModelSettings() (store.ModelSettings, error)
	SaveModelSettings(store.ModelSettings) error
	SaveModelProfile(store.ModelSettings) error
	GetModelSettingsByProfileID(string) (store.ModelSettings, error)
	ListModelProfiles() ([]store.ModelProfile, string, error)
	SetActiveModelProfile(string) (store.ModelSettings, error)
	DeleteModelProfile(string) error
	ClearCodexProvider(string) error
	RecordModelCapabilityTest(string, time.Time) error
	HasModelCapabilityTest(string) (bool, error)
}

var _ modelRepository = (*store.Store)(nil)

func (server *Server) modelsRepository() modelRepository {
	if server.models != nil {
		return server.models
	}
	return server.store
}
