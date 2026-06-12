package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"orchids-api/internal/config"
	"orchids-api/internal/openai"
	"orchids-api/internal/store"
	"orchids-api/internal/zenmux"
	"orchids-api/internal/util"
)

type zenmuxModelChoice struct {
	ID   string
	Name string
}

// fetchZenmuxModels uses a configured account to call /v1/models.
// Zenmux requires auth on its models endpoint, so this only works once at
// least one account is set up; otherwise we fall back to the seed list.
func fetchZenmuxModels(ctx context.Context, cfg *config.Config, s *store.Store) ([]zenmuxModelChoice, error) {
	if s == nil {
		return nil, fmt.Errorf("store not configured")
	}
	accounts, err := enabledAccountsByType(ctx, s, "zenmux")
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("no enabled zenmux accounts")
	}
	acc := accounts[0]
	apiKey := zenmux.ResolveAPIKey(acc)
	if apiKey == "" {
		return nil, fmt.Errorf("missing zenmux api key")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zenmux.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; orchids-api/1.0; +zenmux)")

	proxyFunc := http.ProxyFromEnvironment
	if cfg != nil {
		proxyFunc = util.ProxyFuncFromConfig(cfg)
	}

	client := &http.Client{
		Timeout: 12 * time.Second,
	}
	if proxyFunc != nil {
		client.Transport = &http.Transport{Proxy: proxyFunc}
	} else {
		client.Transport = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("zenmux models fetch failed: %d, body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload openai.ModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return normalizeZenmuxModels(payload.Data), nil
}

func normalizeZenmuxModels(raw []openai.ModelInfo) []zenmuxModelChoice {
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]zenmuxModelChoice, 0, len(raw))
	for _, m := range raw {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		lower := strings.ToLower(id)
		// Drop embedding/moderation/audio endpoints; keep only chat-capable models.
		if strings.Contains(lower, "embedding") || strings.Contains(lower, "moderation") ||
			strings.Contains(lower, "whisper") || strings.Contains(lower, "tts-") {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, zenmuxModelChoice{
			ID:   id,
			Name: zenmuxDisplayName(id),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

func zenmuxDisplayName(modelID string) string {
	if i := strings.LastIndex(modelID, "/"); i >= 0 {
		vendor := modelID[:i]
		rest := modelID[i+1:]
		vendor = strings.TrimPrefix(vendor, vendor)
		_ = vendor
		switch rest {
		case "kimi-k2.7-code-free":
			return "Kimi K2.7 Code Free"
		}
		return rest
	}
	return modelID
}

func zenmuxChoicesToDiscovered(items []zenmuxModelChoice) []discoveredModel {
	out := make([]discoveredModel, 0, len(items))
	for i, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = id
		}
		out = append(out, discoveredModel{ID: id, Name: name, SortOrder: i})
	}
	return out
}

func discoverZenmuxModelsConcurrent(ctx context.Context, cfg *config.Config, s *store.Store, concurrency int) ([]discoveredModel, string, error) {
	items, err := fetchZenmuxModels(ctx, cfg, s)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, "", fmt.Errorf("zenmux public model discovery failed: %w", err)
		}
		return nil, "", fmt.Errorf("zenmux public model discovery returned no choices")
	}
	candidates := zenmuxChoicesToDiscovered(items)
	if len(candidates) == 0 {
		return nil, "", fmt.Errorf("zenmux has no discoverable models")
	}
	return candidates, "zenmux_models_api", nil
}
