package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lakernote/beaver-agent/internal/appenv"
	"github.com/lakernote/beaver-agent/internal/store"
)

func TestCodexMemoryContextInjectsRelevantNotes(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "beaver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	environment, err := appenv.Open(appenv.Config{Home: filepath.Join(t.TempDir(), "home")})
	if err != nil {
		t.Fatal(err)
	}
	application := newTestServer(t, database, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, environment)
	defer application.Shutdown(context.Background())
	if err := application.memory.Save("", "personal", "部署环境使用 Docker Compose"); err != nil {
		t.Fatal(err)
	}
	session, err := database.CreateSession(store.CreateSessionParams{
		ID: "codex-memory-session", Title: "t", Runtime: store.RuntimeCodex, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := application.codexMemoryContext(session.ID, "部署环境使用什么")
	if !strings.Contains(got, "<beaver_memory>") || !strings.Contains(got, "Docker Compose") {
		t.Fatalf("应注入相关记忆: %q", got)
	}
	if application.codexMemoryContext(session.ID, "xyzzy") != "" {
		t.Fatal("无命中时不应注入")
	}
}
