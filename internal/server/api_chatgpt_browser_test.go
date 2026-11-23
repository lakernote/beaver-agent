package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lakernote/beaver-agent/internal/chatgpt"
	"github.com/lakernote/beaver-agent/internal/store"
)

func TestChatGPTAccountDisplayMasksEmail(t *testing.T) {
	account := chatgpt.Account{ID: "fixture", Email: "member@example.test", SignedIn: true}
	view := publicChatGPTAccount(account)
	if view.Email != "m…@e…" || view.ID != account.ID || !view.SignedIn || account.Email != "member@example.test" {
		t.Fatal("public account should contain a masked label and preserve the account reference")
	}
}

func TestCodexSubscriptionAcceptsLoopbackOnWildcardListener(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "beaver.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	app := newTestApplication(t, database, fstest.MapFS{})
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/bootstrap", "/api/v1/model/chatgpt/accounts":
			handler.ServeHTTP(response, request)
		default:
			http.NotFound(response, request)
		}
	}))
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + net.JoinHostPort("127.0.0.1", port)

	bootstrap, err := server.Client().Get(base + "/api/v1/bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Body.Close()
	var data struct {
		Local bool `json:"codexSubscriptionLocal"`
	}
	if err := json.NewDecoder(bootstrap.Body).Decode(&data); err != nil || bootstrap.StatusCode != http.StatusOK || !data.Local {
		t.Fatalf("wildcard listener rejected local bootstrap: HTTP %d, local=%t, err=%v", bootstrap.StatusCode, data.Local, err)
	}

	path := "/api/v1/model/chatgpt/accounts?provider=" + store.ChatGPTCodexProvider
	accounts, err := server.Client().Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	_ = accounts.Body.Close()
	if accounts.StatusCode != http.StatusOK {
		t.Fatalf("loopback account list returned HTTP %d", accounts.StatusCode)
	}

	for _, test := range []struct {
		name, remote, target, forwarded string
	}{
		{name: "remote peer with loopback Host", remote: "192.0.2.2:43100", target: base + path},
		{name: "non-loopback Host", remote: "127.0.0.1:43100", target: "http://192.0.2.1" + path},
		{name: "loopback proxy hop", remote: "127.0.0.1:43100", target: base + path, forwarded: "192.0.2.2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			request.RemoteAddr = test.remote
			if test.forwarded != "" {
				request.Header.Set("X-Forwarded-For", test.forwarded)
			}
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("account list returned HTTP %d, want 403", response.Code)
			}
		})
	}

	request := httptest.NewRequest(http.MethodPost, base+"/api/v1/model/test", strings.NewReader(`{"provider":"chatgpt-codex"}`))
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote model test returned HTTP %d, want 403", response.Code)
	}
}
