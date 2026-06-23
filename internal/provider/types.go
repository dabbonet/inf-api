package provider

import (
	"net/http"

	"orchids-api/internal/config"
	"orchids-api/internal/req"
	"orchids-api/internal/store"
	"orchids-api/internal/stream"
	"orchids-api/internal/upstream"
)

type Spec struct {
	Name          string
	PathPrefix    string
	ParseRequest  func(r *http.Request) (*req.Request, error)
	Hooks         []req.Hook
	Pipeline      *stream.Pipeline
	Passthrough   bool
	ClientFactory func(acc *store.Account, cfg *config.Config) upstream.UpstreamClient
}
