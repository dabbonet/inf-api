package codebuff

// ModelConfig holds the mapping between user-facing model IDs and the
// upstream codebuff.com identifiers used for sessions, agents, and chat.
type ModelConfig struct {
	ID              string
	AgentID         string
	OwnedBy         string
	UpstreamModelID string // model sent to /api/v1/chat/completions
	SessionModelID  string // model sent to /api/v1/freebuff/session
	ParentAgentID   string // optional; set for Gemini variants
}

// UpstreamID returns the model ID to send upstream for chat completions.
func (m *ModelConfig) UpstreamID() string {
	if m.UpstreamModelID != "" {
		return m.UpstreamModelID
	}
	return m.ID
}

// SessionID returns the model ID to use for session creation/validation.
func (m *ModelConfig) SessionID() string {
	if m.SessionModelID != "" {
		return m.SessionModelID
	}
	return m.UpstreamID()
}

var (
	// FREEBUFF_MODELS are the base models that do not require a parent run.
	FREEBUFF_MODELS = []ModelConfig{
		{ID: "deepseek/deepseek-v4-flash", AgentID: "base2-free-deepseek-flash", OwnedBy: "freebuff"},
		{ID: "deepseek/deepseek-v4-pro", AgentID: "base2-free-deepseek", OwnedBy: "freebuff"},
		{ID: "moonshotai/kimi-k2.6", AgentID: "base2-free-kimi", OwnedBy: "freebuff"},
		{ID: "minimax/minimax-m2.7", AgentID: "base2-free", OwnedBy: "freebuff"},
		{ID: "minimax/minimax-m3", AgentID: "base2-free-minimax-m3", OwnedBy: "freebuff"},
		{ID: "mimo/mimo-v2.5", AgentID: "base2-free-mimo", OwnedBy: "freebuff"},
		{ID: "mimo/mimo-v2.5-pro", AgentID: "base2-free-mimo-pro", OwnedBy: "freebuff"},
	}

	DEFAULT_MODEL = FREEBUFF_MODELS[0]

	// Gemini variants require a parent run with a specific parent agent.
	GEMINI_FREE_MODELS = []ModelConfig{
		{
			ID:              "google/gemini-2.5-flash-lite",
			AgentID:         "file-picker",
			OwnedBy:         "google",
			SessionModelID:  DEFAULT_MODEL.ID,
			ParentAgentID:   DEFAULT_MODEL.AgentID,
		},
		{
			ID:              "google/gemini-3.1-flash-lite-preview",
			AgentID:         "file-picker-max",
			OwnedBy:         "google",
			SessionModelID:  DEFAULT_MODEL.ID,
			ParentAgentID:   DEFAULT_MODEL.AgentID,
		},
		{
			ID:              "google/gemini-3.1-pro-preview",
			AgentID:         "thinker-with-files-gemini",
			OwnedBy:         "google",
			SessionModelID:  "moonshotai/kimi-k2.6",
			ParentAgentID:   "base2-free-kimi",
		},
	}

	ALL_MODELS = append(FREEBUFF_MODELS, GEMINI_FREE_MODELS...)

	// Special agent IDs used by the run bookkeeping layer.
	ContextPrunerAgentID = "context-pruner"
)

func init() {
	// Ensure model registry lookups are populated.
	for _, m := range ALL_MODELS {
		_modelByID[m.ID] = m
	}
}

var _modelByID = make(map[string]ModelConfig)

// ResolveModel returns the ModelConfig for a user-facing model ID.
// If requested is empty, the default model is returned.
func ResolveModel(requested string) (*ModelConfig, error) {
	if requested == "" {
		m := DEFAULT_MODEL
		return &m, nil
	}
	m, ok := _modelByID[requested]
	if !ok {
		return nil, &UnsupportedModelError{Requested: requested}
	}
	return &m, nil
}

// ModelsResponse returns the OpenAI-compatible /v1/models response.
func ModelsResponse() map[string]any {
	data := make([]map[string]any, len(ALL_MODELS))
	for i, m := range ALL_MODELS {
		data[i] = map[string]any{
			"id":       m.ID,
			"object":   "model",
			"created":  0,
			"owned_by": m.OwnedBy,
		}
	}
	return map[string]any{
		"object": "list",
		"data":   data,
	}
}

// AgentValidationPayload returns the payload for POST /api/agents/validate.
func AgentValidationPayload() map[string]any {
	modelsByAgent := make(map[string]ModelConfig)
	spawnableByAgent := make(map[string][]string)
	for _, model := range ALL_MODELS {
		modelsByAgent[model.AgentID] = model
		spawnableByAgent[model.AgentID] = append(spawnableByAgent[model.AgentID], ContextPrunerAgentID)
		if model.ParentAgentID != "" {
			spawnableByAgent[model.ParentAgentID] = append(spawnableByAgent[model.ParentAgentID], model.AgentID)
		}
	}

	definitions := make([]map[string]any, 0, len(modelsByAgent)+1)
	for _, model := range modelsByAgent {
		definitions = append(definitions, agentDefinition(
			model.AgentID,
			model.UpstreamID(),
			"Freebuff "+model.UpstreamID(),
			spawnableByAgent[model.AgentID],
		))
	}
	definitions = append(definitions, agentDefinition(
		ContextPrunerAgentID,
		DEFAULT_MODEL.ID,
		"Context Pruner",
		[]string{},
	))

	return map[string]any{"agentDefinitions": definitions}
}

func agentDefinition(agentID, modelID, displayName string, spawnableAgents []string) map[string]any {
	toolNames := []string{}
	if len(spawnableAgents) > 0 {
		toolNames = []string{"spawn_agents"}
	}
	return map[string]any{
		"id":                    agentID,
		"publisher":             "codebuff",
		"model":                 modelID,
		"displayName":           displayName,
		"spawnerPrompt":         "Freebuff OpenAI-compatible orchestrator",
		"inputSchema":           map[string]any{"prompt": map[string]any{"type": "string", "description": "A coding task to complete"}, "params": map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
		"outputMode":            "last_message",
		"includeMessageHistory": true,
		"toolNames":             toolNames,
		"spawnableAgents":       spawnableAgents,
		"systemPrompt":          "Act as a helpful coding assistant.",
	}
}

// UnsupportedModelError is returned when a requested model is not in the registry.
type UnsupportedModelError struct {
	Requested string
}

func (e *UnsupportedModelError) Error() string {
	return "Unsupported Freebuff model: " + e.Requested
}
