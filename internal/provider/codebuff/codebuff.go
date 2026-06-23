package codebuff

import (
	"orchids-api/internal/config"
	"orchids-api/internal/handler"
	"orchids-api/internal/provider"
	"orchids-api/internal/req"
	"orchids-api/internal/store"
)

func Spec() provider.Spec {
	return provider.Spec{
		Name:         "codebuff",
		PathPrefix:   "/codebuff",
		ParseRequest: req.ParsePassthrough,
		Passthrough:  true,
		ClientFactory: func(acc *store.Account, cfg *config.Config) handler.UpstreamClient {
			return nil
		},
	}
}
