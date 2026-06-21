package provider

import (
	"orchids-api/internal/codebuff"
	"orchids-api/internal/config"
	"orchids-api/internal/store"
)

type codebuffProvider struct{}

func NewCodebuffProvider() Provider { return codebuffProvider{} }

func (codebuffProvider) Name() string { return "codebuff" }

func (codebuffProvider) NewClient(acc *store.Account, cfg *config.Config) interface{} {
	return codebuff.NewFromAccount(acc, cfg)
}
