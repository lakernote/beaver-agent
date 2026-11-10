package httpdto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lakernote/beaver-agent/internal/store"
)

func TestSessionFromStoreOmitsPersistenceOnlyData(t *testing.T) {
	value := store.Session{
		ID: "session", Status: store.SessionRunning, Runtime: store.RuntimeBeaver,
		PendingModel: json.RawMessage(`{"apiKey":"SECRET_PENDING"}`),
		ProviderKey:  "SECRET_PROVIDER", ResponseID: "SECRET_RESPONSE",
		Messages: []store.Message{{
			Role: "user", Content: "hello",
			Attachments: []store.Attachment{{ID: "attachment", Name: "note.txt", Data: []byte("SECRET_DATA")}},
		}},
	}
	encoded, err := json.Marshal(SessionFromStore(value, "direct"))
	if err != nil {
		t.Fatal(err)
	}
	payload := string(encoded)
	for _, secret := range []string{"SECRET_PENDING", "SECRET_PROVIDER", "SECRET_RESPONSE", "SECRET_DATA"} {
		if strings.Contains(payload, secret) {
			t.Fatalf("HTTP DTO leaked persistence-only value %q: %s", secret, payload)
		}
	}
	if !strings.Contains(payload, `"workspaceIsolation":"direct"`) || !strings.Contains(payload, `"attachments":[{"id":"attachment"`) {
		t.Fatalf("public session fields were not preserved: %s", payload)
	}
}

func TestModelSettingsFromStoreExposesOnlySecretPresence(t *testing.T) {
	encoded, err := json.Marshal(ModelSettingsFromStore(store.ModelSettings{
		Runtime: store.RuntimeBeaver, Provider: "fixture", APIKey: "SECRET_API_KEY", APIKeyEnv: "SECRET_ENV_NAME",
	}))
	if err != nil {
		t.Fatal(err)
	}
	payload := string(encoded)
	if strings.Contains(payload, "SECRET_API_KEY") || strings.Contains(payload, "SECRET_ENV_NAME") {
		t.Fatalf("model DTO leaked credential material: %s", payload)
	}
	if !strings.Contains(payload, `"secretConfigured":true`) {
		t.Fatalf("model DTO did not report configured secret: %s", payload)
	}
}
