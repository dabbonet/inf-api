package store

import "strconv"

// buildAihubmixSeedModels returns the fallback model list used when no
// aihubmix accounts are configured yet. These are the free models exposed
// by aihubmix at the time of writing.
func buildAihubmixSeedModels() []Model {
	modelIDs := []string{
		"gpt-5.5-free",
		"gpt-image-2-free",
		"coding-glm-5.1-free",
	}

	models := make([]Model, 0, len(modelIDs))
	for i, modelID := range modelIDs {
		models = append(models, Model{
			ID:        strconv.Itoa(220 + i),
			Channel:   "Aihubmix",
			ModelID:   modelID,
			Name:      aihubmixDisplayName(modelID),
			Status:    ModelStatusAvailable,
			IsDefault: modelID == "gpt-5.5-free",
			SortOrder: i,
		})
	}
	return models
}

func aihubmixDisplayName(modelID string) string {
	switch modelID {
	case "gpt-5.5-free":
		return "GPT-5.5 Free"
	case "gpt-image-2-free":
		return "GPT Image 2 Free"
	case "coding-glm-5.1-free":
		return "Coding GLM 5.1 Free"
	}
	return modelID
}
