package store

import "strconv"

func buildPuterSeedModels() []Model {
	modelIDs := []string{
		"claude-opus-4-5",
		"claude-opus-4-5-latest",
		"claude-opus-4.5",
		"claude-sonnet-4-5",
		"claude-sonnet-4.5",
		"claude-haiku-4-5",
		"claude-haiku-4.5",
		"gpt-5",
		"gpt-5.1",
		"gpt-4o",
		"o3",
		"gemini-2.5-pro",
		"z-ai/glm-5.2",
		"moonshotai/kimi-k2.7-code",
		"qwen/qwen3.7-plus",
		"xiaomi/mimo-v2.5-pro",
	}

	models := make([]Model, 0, len(modelIDs))
	for i, modelID := range modelIDs {
		models = append(models, Model{
			ID:        strconv.Itoa(109 + i),
			Channel:   "Puter",
			ModelID:   modelID,
			Name:      modelID,
			Status:    ModelStatusAvailable,
			IsDefault: modelID == "claude-opus-4-5",
			SortOrder: i,
		})
	}
	return models
}
