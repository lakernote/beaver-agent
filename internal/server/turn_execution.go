package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/lakernote/beaver-agent/internal/store"
)

func (server *Server) executeSessionTurn(ctx context.Context, id string, settings store.ModelSettings, usage *store.Usage) error {
	session, err := server.store.RuntimeSession(id)
	if err != nil {
		return err
	}
	// 工作区属于会话，而不是服务进程：使用创建会话时保存的绝对目录。
	runEnvironment, err := server.env.WithWorkspace(session.Workspace)
	if err != nil {
		return fmt.Errorf("打开会话工作区: %w", err)
	}
	directories := []string{}
	if session.ProjectID != "" {
		project, projectErr := server.store.GetProject(session.ProjectID)
		if projectErr != nil && !errors.Is(projectErr, sql.ErrNoRows) {
			return fmt.Errorf("读取会话项目: %w", projectErr)
		}
		for _, directory := range project.Directories {
			if filepath.Clean(directory) != filepath.Clean(session.SourceWorkspace) && filepath.Clean(directory) != filepath.Clean(session.Workspace) {
				directories = append(directories, directory)
			}
		}
	}
	runEnvironment, err = runEnvironment.WithDirectories(directories)
	if err != nil {
		return fmt.Errorf("打开项目源文件夹: %w", err)
	}
	// 只有两种 Runtime，直接 switch 比注册表更容易读。
	switch session.Runtime {
	case store.RuntimeCodex:
		return server.runCodexTurn(ctx, session, settings, runEnvironment.Workspace(), runEnvironment.Directories(), usage)
	case store.RuntimeBeaver, "":
		return server.runBeaverTurn(ctx, id, session, settings, runEnvironment, usage)
	default:
		return fmt.Errorf("不支持的 Runtime: %s", session.Runtime)
	}
}
