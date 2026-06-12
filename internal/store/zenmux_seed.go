package store

import "strconv"

// buildZenmuxSeedModels returns the fallback model list used when no
// zenmux accounts are configured yet.
func buildZenmuxSeedModels() []Model {
	modelIDs := []string{
		"moonshotai/kimi-k2.7-code-free",
	}

	models := make([]Model, 0, len(modelIDs))
	for i, modelID := range modelIDs {
		models = append(models, Model{
			ID:        strconv.Itoa(230 + i),
			Channel:   "Zenmux",
			ModelID:   modelID,
			Name:      "Kimi K2.7 Code Free",
			Status:    ModelStatusAvailable,
			IsDefault: true,
			SortOrder: i,
		})
	}
	return models
}
