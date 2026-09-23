package httpdto

import (
	"time"

	"github.com/lakernote/easy-agent/internal/store"
)

type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Directories []string  `json:"directories"`
	Default     bool      `json:"default"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func ProjectFromStore(value store.Project) Project {
	return Project{ID: value.ID, Name: value.Name, Directories: append([]string(nil), value.Directories...), Default: value.Default, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func ProjectsFromStore(values []store.Project) []Project {
	result := make([]Project, 0, len(values))
	for _, value := range values {
		result = append(result, ProjectFromStore(value))
	}
	return result
}
