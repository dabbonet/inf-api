package aihubmix

import (
	"strings"

	"orchids-api/internal/config"
	"orchids-api/internal/openai"
	"orchids-api/internal/store"
)

// BaseURL is the aihubmix OpenAI-compatible root.
const BaseURL = "https://aihubmix.com/v1"

// PublicModelsURL is a separate aihubmix endpoint that exposes the public
// model catalog (no authentication required). Used by the model-discovery job
// when no accounts are configured yet.
const PublicModelsURL = "https://aihubmix.com/api/v1/models"

// DefaultModel is used when a caller does not supply a model field.
const DefaultModel = "gpt-5.5-free"

// IsImageModel reports whether the given model name should be routed to the
// OpenAI image-generation endpoint rather than chat completions.
// Aihubmix exposes image-capable models under the gpt-image-* family.
func IsImageModel(modelID string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	return strings.HasPrefix(id, "gpt-image-")
}

// Client is the aihubmix channel client.
// It embeds openai.Client and only adds image-model dispatch.
type Client struct {
	*openai.Client
}

// NewFromAccount builds an aihubmix client from a stored account.
func NewFromAccount(acc *store.Account, cfg *config.Config) *Client {
	return &Client{
		Client: openai.NewClient("aihubmix", BaseURL, DefaultModel, acc, cfg),
	}
}

// ResolveAPIKey is a thin re-export so account code can call aihubmix.ResolveAPIKey
// without depending on the openai package directly.
func ResolveAPIKey(acc *store.Account) string {
	return openai.ResolveAPIKey(acc)
}
