package server

import (
	"net/http"

	"github.com/lakernote/beaver-agent/internal/builtin/prompt"
	builtintools "github.com/lakernote/beaver-agent/internal/builtin/tools"
	"github.com/lakernote/beaver-agent/internal/codexruntime"
	"github.com/lakernote/beaver-agent/internal/httpdto"
	mcppresets "github.com/lakernote/beaver-agent/internal/mcp/presets"
	"github.com/lakernote/beaver-agent/internal/store"
)

type bootstrapPayload struct {
	Sessions             []sessionView               `json:"sessions"`
	Projects             []httpdto.Project           `json:"projects"`
	SessionsHasMore      bool                        `json:"sessionsHasMore,omitempty"`
	Model                modelSettingsView           `json:"model"`
	ModelProfiles        []modelProfileView          `json:"modelProfiles"`
	ActiveModelProfileID string                      `json:"activeModelProfileId"`
	Skills               []store.SkillOverride       `json:"skills"`
	BuiltinTools         []builtintools.Info         `json:"builtinTools"`
	MCPPresets           []mcppresets.Preset         `json:"mcpPresets"`
	ModelRules           modelRulesPayload           `json:"modelRules"`
	MCPs                 []httpdto.MCPConfig         `json:"mcps"`
	SystemPrompt         string                      `json:"systemPrompt"`
	Ollama               ollamaStatus                `json:"ollama"`
	Codex                codexRuntimeStatus          `json:"codex"`
	CodexConfig          codexruntime.ProviderConfig `json:"codexConfig"`
	Runtime              runtimeInfoPayload          `json:"runtime"`
	RuntimeSettings      store.RuntimeSettings       `json:"runtimeSettings"`
	ResearchSettings     researchSettingsView        `json:"researchSettings"`
}

type modelProfileView struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Settings modelSettingsView `json:"settings"`
	Verified bool              `json:"verified"`
}

type runtimeInfoPayload struct {
	Home      string `json:"home"`
	Workspace string `json:"workspace"`
	Runtime   string `json:"runtime"`
}

type modelRulesPayload struct {
	DefaultMaxOutputTokens             int `json:"defaultMaxOutputTokens"`
	DefaultMaxSteps                    int `json:"defaultMaxSteps"`
	MinMaxSteps                        int `json:"minMaxSteps"`
	MaxMaxSteps                        int `json:"maxMaxSteps"`
	DefaultRequestTimeoutSeconds       int `json:"defaultRequestTimeoutSeconds"`
	MinRequestTimeoutSeconds           int `json:"minRequestTimeoutSeconds"`
	MaxRequestTimeoutSeconds           int `json:"maxRequestTimeoutSeconds"`
	DefaultCodexTurnTimeoutSeconds     int `json:"defaultCodexTurnTimeoutSeconds"`
	MinCodexTurnTimeoutSeconds         int `json:"minCodexTurnTimeoutSeconds"`
	MaxCodexTurnTimeoutSeconds         int `json:"maxCodexTurnTimeoutSeconds"`
	DefaultCompressionThresholdPercent int `json:"defaultCompressionThresholdPercent"`
	MinCompressionThresholdPercent     int `json:"minCompressionThresholdPercent"`
	MaxCompressionThresholdPercent     int `json:"maxCompressionThresholdPercent"`
}

func (server *Server) bootstrap(response http.ResponseWriter, request *http.Request) {
	projects, err := server.projects()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	sessions, sessionsHasMore, err := server.sessionsRepository().ListSessionsBefore(30, "", "")
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	model, err := server.modelsRepository().GetModelSettings()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	profiles, activeProfileID, err := server.modelsRepository().ListModelProfiles()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	publicProfiles, err := server.modelProfileViews(profiles)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	// A fresh database starts with an Beaver profile, but Codex's official
	// ChatGPT login is a valid runtime configuration even without a third-party
	// provider entry. Expose a virtual profile so the chat composer can select
	// Codex and create a session before the user adds a custom provider.
	if !hasCodexProfile(profiles) {
		publicProfiles = append(publicProfiles, modelProfileView{
			ID: store.DefaultCodexProfileID, Name: "Codex 默认配置",
			Settings: publicModel(store.DefaultCodexModelSettings()), Verified: true,
		})
	}
	catalog, err := loadSkillCatalog(server.store)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	mcps, err := server.store.ListMCPConfigs()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	detectedModel := enrichOllamaContextWindow(request.Context(), model)
	model = detectedModel
	runtimeSettings, err := server.store.GetRuntimeSettings()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	toolInfo := builtintools.InfoListWithPermissions(server.env, catalog, runtimeSettings.Permissions)
	for _, config := range mcps {
		if config.Enabled {
			toolInfo = append(toolInfo, builtintools.Info{Name: "search_mcp_tools", Description: "按任务语义搜索已启用的 MCP Server，一次只加载少量相关工具。", Source: "运行时"})
			break
		}
	}
	researchSettings, err := server.store.GetResearchSettings()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, bootstrapPayload{
		Sessions: server.sessionViews(sessions), Projects: httpdto.ProjectsFromStore(projects), SessionsHasMore: sessionsHasMore, Model: publicModel(model), ModelProfiles: publicProfiles, ActiveModelProfileID: activeProfileID, Skills: catalog.All(),
		BuiltinTools: toolInfo, MCPPresets: mcppresets.Catalog(), ModelRules: modelRules(),
		MCPs: publicMCPs(mcps), SystemPrompt: prompt.Template(), Ollama: server.detectOllama(request.Context()),
		Runtime:          runtimeInfoPayload{Home: server.env.Home(), Workspace: server.env.Workspace(), Runtime: server.env.Runtime()},
		RuntimeSettings:  runtimeSettings,
		ResearchSettings: publicResearchSettings(researchSettings),
		Codex:            server.detectCodex(request.Context()),
		CodexConfig:      server.loadCodexConfig(),
	})
}

func hasCodexProfile(profiles []store.ModelProfile) bool {
	for _, profile := range profiles {
		if profile.Settings.Runtime == store.RuntimeCodex {
			return true
		}
	}
	return false
}

func (server *Server) modelProfileViews(profiles []store.ModelProfile) ([]modelProfileView, error) {
	views := make([]modelProfileView, 0, len(profiles))
	for _, profile := range profiles {
		verified := profile.Settings.Runtime == store.RuntimeCodex
		if !verified {
			var err error
			verified, err = server.modelsRepository().HasModelCapabilityTest(modelCapabilityFingerprint(profile.Settings))
			if err != nil {
				return nil, err
			}
		}
		views = append(views, modelProfileView{
			ID: profile.ID, Name: profile.Name, Settings: publicModel(profile.Settings), Verified: verified,
		})
	}
	return views, nil
}

func (server *Server) loadCodexConfig() codexruntime.ProviderConfig {
	config, err := codexruntime.LoadProviderConfig()
	if err != nil {
		return codexruntime.ProviderConfig{}
	}
	return config
}
