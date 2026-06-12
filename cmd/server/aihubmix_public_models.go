package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"orchids-api/internal/aihubmix"
	"orchids-api/internal/config"
	"orchids-api/internal/store"
	"orchids-api/internal/util"
)

type aihubmixPublicModel struct {
	ID      string `json:"model_id"`
	Name    string `json:"model_name"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	Types   string `json:"types"`
}

type aihubmixPublicModelsResponse struct {
	Object string                `json:"object"`
	Data   []aihubmixPublicModel `json:"data"`
}

type aihubmixModelChoice struct {
	ID   string
	Name string
}

// fetchAihubmixPublicModels hits the aihubmix public catalog and returns the
// normalized model list. No auth required.
func fetchAihubmixPublicModels(ctx context.Context, proxyFunc func(*http.Request) (*url.URL, error)) ([]aihubmixModelChoice, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, aihubmix.PublicModelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; orchids-api/1.0; +aihubmix)")

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
		return nil, fmt.Errorf("aihubmix models fetch failed: %d", resp.StatusCode)
	}

	var payload aihubmixPublicModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return normalizeAihubmixPublicModels(payload.Data), nil
}

func normalizeAihubmixPublicModels(rawModels []aihubmixPublicModel) []aihubmixModelChoice {
	if len(rawModels) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(rawModels))
	out := make([]aihubmixModelChoice, 0, len(rawModels))
	for _, raw := range rawModels {
		id := strings.TrimSpace(raw.ID)
		if id == "" {
			continue
		}
		// Whitelist: only keep the curated set of aihubmix models we want to
		// surface in the UI. The full aihubmix catalog has 700+ entries
		// (chat, image, embedding, rerank, tts, stt, ocr, video, ...) — we
		// only need a handful of free chat/image models.
		if !aihubmixModelAllowed(id) {
			continue
		}
		// Belt-and-suspenders filter on the upstream `types` field.
		types := strings.ToLower(raw.Types)
		isLLM := strings.Contains(types, "llm")
		isImage := strings.Contains(types, "image_generation")
		if !isLLM && !isImage {
			continue
		}
		lower := strings.ToLower(id)
		if strings.HasPrefix(lower, "text-embedding") || strings.HasPrefix(lower, "moderation") {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		name := aihubmixDisplayName(id)
		if raw.Name != "" {
			name = raw.Name
		}
		out = append(out, aihubmixModelChoice{
			ID:   id,
			Name: name,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// aihubmixAllowedModels is the curated whitelist of aihubmix model IDs that
// will be visible in the admin UI and exposed at /aihubmix/v1/models.
var aihubmixAllowedModels = map[string]struct{}{
	"gpt-5.5-free":             {},
	"gpt-image-2-free":         {},
	"coding-glm-5.1-free":      {},
	"qwen3.6-plus-preview-free": {},
}

func aihubmixModelAllowed(id string) bool {
	_, ok := aihubmixAllowedModels[strings.ToLower(strings.TrimSpace(id))]
	return ok
}

func aihubmixDisplayName(modelID string) string {
	switch modelID {
	case "gpt-5.5-free":
		return "GPT-5.5 Free"
	case "gpt-image-2-free":
		return "GPT Image 2 Free"
	case "coding-glm-5.1-free":
		return "Coding GLM 5.1 Free"
	}
	return modelID
}

func aihubmixChoicesToDiscovered(items []aihubmixModelChoice) []discoveredModel {
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

func discoverAihubmixModelsConcurrent(ctx context.Context, cfg *config.Config, s *store.Store, concurrency int) ([]discoveredModel, string, error) {
	proxyFunc := http.ProxyFromEnvironment
	if cfg != nil {
		proxyFunc = util.ProxyFuncFromConfig(cfg)
	}

	items, err := fetchAihubmixPublicModels(ctx, proxyFunc)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, "", fmt.Errorf("aihubmix public model discovery failed: %w", err)
		}
		return nil, "", fmt.Errorf("aihubmix public model discovery returned no choices")
	}
	candidates := aihubmixChoicesToDiscovered(items)
	if len(candidates) == 0 {
		return nil, "", fmt.Errorf("aihubmix has no discoverable models")
	}
	return candidates, "aihubmix_public_models", nil
}
