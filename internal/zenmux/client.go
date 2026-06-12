package zenmux

import (
	"orchids-api/internal/config"
	"orchids-api/internal/openai"
	"orchids-api/internal/store"
)

// BaseURL is the zenmux OpenAI-compatible root.
const BaseURL = "https://zenmux.ai/api/v1"

// DefaultModel is used when a caller does not supply a model field.
const DefaultModel = "moonshotai/kimi-k2.7-code-free"

// Client is the zenmux channel client.
// Zenmux is pure chat-completions; no image endpoint, no extra logic needed.
type Client struct {
	*openai.Client
}

// NewFromAccount builds a zenmux client from a stored account.
func NewFromAccount(acc *store.Account, cfg *config.Config) *Client {
	return &Client{
		Client: openai.NewClient("zenmux", BaseURL, DefaultModel, acc, cfg),
	}
}

// ResolveAPIKey is a thin re-export so account code can call zenmux.ResolveAPIKey
// without depending on the openai package directly.
func ResolveAPIKey(acc *store.Account) string {
	return openai.ResolveAPIKey(acc)
}
