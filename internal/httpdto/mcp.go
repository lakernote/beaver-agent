package httpdto

import "github.com/lakernote/easy-agent/internal/store"

// MCPConfig deliberately excludes token and password fields. Secret-bearing
// args, headers, and environment values must be redacted before conversion.
type MCPConfig struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Description       string            `json:"description,omitempty"`
	Enabled           bool              `json:"enabled"`
	Transport         string            `json:"transport"`
	Command           string            `json:"command,omitempty"`
	Args              []string          `json:"args"`
	Endpoint          string            `json:"endpoint,omitempty"`
	AuthType          string            `json:"authType,omitempty"`
	Username          string            `json:"username,omitempty"`
	Headers           map[string]string `json:"headers"`
	Environment       map[string]string `json:"environment"`
	OAuthClientID     string            `json:"oauthClientId,omitempty"`
	OAuthCallbackURL  string            `json:"oauthCallbackUrl,omitempty"`
	OAuthCallbackPort int               `json:"oauthCallbackPort,omitempty"`
	OAuthScopes       []string          `json:"oauthScopes,omitempty"`
	SecretConfigured  bool              `json:"secretConfigured,omitempty"`
}

func MCPConfigFromStore(value store.MCPConfig) MCPConfig {
	return MCPConfig{
		ID: value.ID, Name: value.Name, Description: value.Description, Enabled: value.Enabled,
		Transport: value.Transport, Command: value.Command, Args: append([]string(nil), value.Args...), Endpoint: value.Endpoint,
		AuthType: value.AuthType, Username: value.Username, Headers: cloneStrings(value.Headers), Environment: cloneStrings(value.Environment),
		OAuthClientID: value.OAuthClientID, OAuthCallbackURL: value.OAuthCallbackURL, OAuthCallbackPort: value.OAuthCallbackPort,
		OAuthScopes: append([]string(nil), value.OAuthScopes...), SecretConfigured: value.SecretConfigured,
	}
}

func cloneStrings(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
