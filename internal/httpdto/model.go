package httpdto

import "github.com/lakernote/beaver-agent/internal/store"

// ModelSettings 是对外展示的模型配置，刻意不包含 API Key 等凭据。
type ModelSettings struct {
	ProfileID                   string `json:"profileId,omitempty"`
	ProfileName                 string `json:"profileName,omitempty"`
	Runtime                     string `json:"runtime"`
	Provider                    string `json:"provider"`
	Protocol                    string `json:"protocol"`
	BaseURL                     string `json:"baseUrl"`
	Model                       string `json:"model"`
	ChatGPTAccountID            string `json:"chatgptAccountId,omitempty"`
	Thinking                    string `json:"thinking,omitempty"`
	MaxOutputTokens             int    `json:"maxOutputTokens,omitempty"`
	MaxSteps                    int    `json:"maxSteps,omitempty"`
	RequestTimeoutSeconds       int    `json:"requestTimeoutSeconds,omitempty"`
	TurnTimeoutSeconds          int    `json:"turnTimeoutSeconds,omitempty"`
	ContextWindowTokens         int    `json:"contextWindowTokens,omitempty"`
	CompressionThresholdPercent int    `json:"compressionThresholdPercent,omitempty"`
	SecretConfigured            bool   `json:"secretConfigured,omitempty"`
}

func ModelSettingsFromStore(value store.ModelSettings) ModelSettings {
	return ModelSettings{
		ProfileID: value.ProfileID, ProfileName: value.ProfileName, Runtime: string(value.Runtime),
		Provider: value.Provider, Protocol: value.Protocol, BaseURL: value.BaseURL, Model: value.Model, ChatGPTAccountID: value.ChatGPTAccountID,
		Thinking: value.Thinking, MaxOutputTokens: value.MaxOutputTokens, MaxSteps: value.MaxSteps,
		RequestTimeoutSeconds: value.RequestTimeoutSeconds, TurnTimeoutSeconds: value.TurnTimeoutSeconds,
		ContextWindowTokens: value.ContextWindowTokens, CompressionThresholdPercent: value.CompressionThresholdPercent,
		SecretConfigured: value.APIKey != "",
	}
}
