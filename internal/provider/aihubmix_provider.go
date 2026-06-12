package provider

import (
	"orchids-api/internal/aihubmix"
	"orchids-api/internal/config"
	"orchids-api/internal/store"
)

type aihubmixProvider struct{}

func NewAihubmixProvider() Provider { return aihubmixProvider{} }

func (aihubmixProvider) Name() string { return "aihubmix" }

func (aihubmixProvider) NewClient(acc *store.Account, cfg *config.Config) interface{} {
	return aihubmix.NewFromAccount(acc, cfg)
}
