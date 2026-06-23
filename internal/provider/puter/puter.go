package puter

import (
	"orchids-api/internal/config"
	"orchids-api/internal/handler"
	"orchids-api/internal/provider"
	"orchids-api/internal/req"
	"orchids-api/internal/stream"
	"orchids-api/internal/store"

	pkg "orchids-api/internal/puter"
)

func Spec() provider.Spec {
	return provider.Spec{
		Name:         "puter",
		PathPrefix:   "/puter",
		ParseRequest: req.ParseAnthropic,
		Pipeline:     stream.NewPipeline(stream.SuppressTrailingStopEvents()),
		ClientFactory: func(acc *store.Account, cfg *config.Config) handler.UpstreamClient {
			return pkg.NewFromAccount(acc, cfg)
		},
	}
}
