package puter

import (
	"orchids-api/internal/config"
	"orchids-api/internal/provider"
	"orchids-api/internal/req"
	"orchids-api/internal/stream"
	"orchids-api/internal/store"
	"orchids-api/internal/upstream"

	pkg "orchids-api/internal/puter"
)

// Puter-style send-through: parse normally then apply puter-specific
// sanitization (no message hook wiring required — the handler binds its
// own SanitizeSystemItemsPuter/SanitizePuterMessages at call time when
// Mode.SkipDefaultSanitize is true).
func Spec() provider.Spec {
	return provider.Spec{
		Name:         "puter",
		PathPrefix:   "/puter",
		ParseRequest: req.ParseAnthropic,
		Pipeline:     stream.NewPipeline(stream.SuppressTrailingStopEvents()),
		Mode: provider.ModeOptions{
			UseRawModel:         true,
			KeepToolsOnFollowup: true,
			PromptProfile:       "puter",
			SkipDefaultSanitize: true,
		},
		ClientFactory: func(acc *store.Account, cfg *config.Config) upstream.UpstreamClient {
			return pkg.NewFromAccount(acc, cfg)
		},
	}
}
