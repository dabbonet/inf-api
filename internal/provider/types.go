package provider

import (
	"net/http"

	"orchids-api/internal/config"
	"orchids-api/internal/req"
	"orchids-api/internal/store"
	"orchids-api/internal/stream"
	"orchids-api/internal/upstream"
)

// ModeOptions captures per-provider request handling flags.
//
// Handler.HandleMessages drives off these fields. Zero-value Mode is
// the default "generic" mode. Providers like puter set Mode to enable
// send-through handling.
type ModeOptions struct {
	// UseRawModel skips the standard mapModel normalization.
	UseRawModel bool

	// KeepToolsOnFollowup keeps tools enabled on tool_result follow-ups
	// instead of disabling them and injecting a tool-gate message.
	KeepToolsOnFollowup bool

	// PromptProfile is the profile name used for prompt token accounting.
	// Empty => default "generic".
	PromptProfile string

	// SkipDefaultSanitize skips the default SanitizeSystemItems hook.
	// Set true when the provider needs its own sanitization regime.
	SkipDefaultSanitize bool
}

// Spec describes a single provider: how to parse requests, how to
// preprocess them, how to stream responses, and how to build upstream
// clients.
type Spec struct {
	Name          string
	PathPrefix    string
	ParseRequest  func(r *http.Request) (*req.Request, error)
	Hooks         []req.Hook
	Pipeline      *stream.Pipeline
	Passthrough   bool
	Mode          ModeOptions
	ClientFactory func(acc *store.Account, cfg *config.Config) upstream.UpstreamClient
}
