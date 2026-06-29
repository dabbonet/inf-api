// Package kimchiprov is the thin registry adapter that publishes Kimchi as
// a Provider inside inf-api's plugin system. There are no business logic
// here — just a Spec() describing how to parse + dispatch + handle kimchi
// requests, and a ClientFactory that defers to internal/kimchi for the
// heavy lifting.
package kimchiprov

import (
	"orchids-api/internal/config"
	"orchids-api/internal/kimchi"
	"orchids-api/internal/provider"
	"orchids-api/internal/req"
	"orchids-api/internal/store"
	"orchids-api/internal/upstream"
)

// Spec returns the provider.Spec for Kimchi. The handler resolves incoming
// /kimchi/* requests to this spec via provider.Registry.GetByPathPrefix.
//
// Mode choices (and why):
//
//   - ParseRequest: req.ParsePassthrough — handler.handlePassthroughProvider
//     parses only model/stream/messages/system. We forward RawBody upstream
//     untouched.
//   - Passthrough: true — the handler takes the passthrough fast path; no
//     ClaudeRequest / prompt.Message conversion is performed.
//   - Mono-model (orchestrator): Kimchi has no role-routing; single selected
//     model per request. We leave Mode zero-value.
func Spec() provider.Spec {
	return provider.Spec{
		Name:         "kimchi",
		PathPrefix:   "/kimchi",
		ParseRequest: req.ParsePassthrough,
		Passthrough:  true,
		ClientFactory: func(acc *store.Account, cfg *config.Config) upstream.UpstreamClient {
			return kimchi.NewFromAccount(acc, cfg)
		},
	}
}
