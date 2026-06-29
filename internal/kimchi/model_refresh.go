package kimchi

import (
	"context"
	"fmt"
	"strings"

	"orchids-api/internal/config"
	"orchids-api/internal/store"
)

// RefreshModels pulls the live Kimchi model catalog for the given account
// and returns rows suitable for upserting into the local store.Model table.
//
// Models carry channel="Kimchi" + status=available + the upstream slug as
// ModelID. Display name falls back to the slug if upstream didn't populate
// display_name.
//
// Source URL: <base>/v1/models/metadata?include_in_cli=true
func RefreshModels(ctx context.Context, acc *store.Account, cfg *config.Config) ([]store.Model, error) {
	if acc == nil {
		return nil, fmt.Errorf("kimchi: nil account")
	}
	token := ResolveAuthToken(acc)
	if token == "" {
		return nil, fmt.Errorf("kimchi: no bearer token on account")
	}
	c := NewClient(token, cfg)
	infos, err := c.FetchModelMetadata(ctx)
	if err != nil {
		return nil, fmt.Errorf("kimchi: refresh models: %w", err)
	}
	out := make([]store.Model, 0, len(infos))
	for _, info := range infos {
		display := strings.TrimSpace(info.DisplayName)
		if display == "" {
			display = info.Slug
		}
		out = append(out, store.Model{
			ModelID: info.Slug,
			Name:    display,
			Channel: "Kimchi",
			Status:  store.ModelStatusAvailable,
		})
	}
	return out, nil
}
