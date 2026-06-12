package provider

import (
	"orchids-api/internal/config"
	"orchids-api/internal/store"
	"orchids-api/internal/zenmux"
)

type zenmuxProvider struct{}

func NewZenmuxProvider() Provider { return zenmuxProvider{} }

func (zenmuxProvider) Name() string { return "zenmux" }

func (zenmuxProvider) NewClient(acc *store.Account, cfg *config.Config) interface{} {
	return zenmux.NewFromAccount(acc, cfg)
}
