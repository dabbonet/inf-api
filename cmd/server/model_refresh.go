package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/goccy/go-json"

	"orchids-api/internal/config"
	"orchids-api/internal/kimchi"
	"orchids-api/internal/puter"
	"orchids-api/internal/store"
	"orchids-api/internal/util"
)

const (
	defaultModelRefreshConcurrency = 4
	maxModelRefreshConcurrency     = 16
)

var verifyPuterModelForRefresh = func(ctx context.Context, cfg *config.Config, acc *store.Account, modelID string) error {
	client := puter.NewFromAccount(acc, refreshModelRequestConfig(cfg, "puter"))
	defer client.Close()
	return client.VerifyModel(ctx, modelID)
}

type modelRefreshResult struct {
	Channel         string   `json:"channel"`
	Source          string   `json:"source"`
	Concurrency     int      `json:"concurrency"`
	Discovered      int      `json:"discovered"`
	Verified        int      `json:"verified"`
	Added           int      `json:"added"`
	Updated         int      `json:"updated"`
	Deleted         int      `json:"deleted"`
	Offline         int      `json:"offline"`
	DefaultModelID  string   `json:"default_model_id,omitempty"`
	AddedModelIDs   []string `json:"added_model_ids,omitempty"`
	DeletedModelIDs []string `json:"deleted_model_ids,omitempty"`
	OfflineModelIDs []string `json:"offline_model_ids,omitempty"`
}

type discoveredModel struct {
	ID        string
	Name      string
	SortOrder int
}

type modelRefreshRequest struct {
	Channel     string `json:"channel"`
	Concurrency int    `json:"concurrency,omitempty"`
}

type modelRefreshFunc func(ctx context.Context, cfg *config.Config, s *store.Store, channel string, concurrency int) (*modelRefreshResult, error)

var runModelRefresh modelRefreshFunc = syncModelsForChannelConcurrent

func makeModelRefreshHandler(cfg *config.Config, s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		channel := strings.TrimSpace(r.URL.Query().Get("channel"))
		concurrency := defaultModelRefreshConcurrency
		if parsed, ok := parseModelRefreshConcurrency(r.URL.Query().Get("concurrency")); ok {
			concurrency = parsed
		}
		if r.Body != nil {
			defer r.Body.Close()
			var req modelRefreshRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err == nil && strings.TrimSpace(req.Channel) != "" {
				channel = strings.TrimSpace(req.Channel)
			}
			if req.Concurrency != 0 {
				concurrency = normalizeModelRefreshConcurrency(req.Concurrency)
			}
		}

		if channel != "" && !isRefreshableModelChannel(channel) {
			http.Error(w, fmt.Sprintf("channel %q is not supported; refresh only Puter, Codebuff, or Kimchi", channel), http.StatusBadRequest)
			return
		}

		result, err := runModelRefresh(r.Context(), cfg, s, channel, concurrency)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if result != nil && result.Concurrency == 0 {
			result.Concurrency = concurrency
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(result); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func syncModelsForChannel(ctx context.Context, cfg *config.Config, s *store.Store, channel string) (*modelRefreshResult, error) {
	return syncModelsForChannelConcurrent(ctx, cfg, s, channel, defaultModelRefreshConcurrency)
}

func syncModelsForChannelConcurrent(ctx context.Context, cfg *config.Config, s *store.Store, channel string, concurrency int) (*modelRefreshResult, error) {
	channel = normalizeAdminModelChannel(channel)
	if channel == "" {
		return nil, fmt.Errorf("channel is required")
	}
	if s == nil {
		return nil, fmt.Errorf("store not configured")
	}

	concurrency = normalizeModelRefreshConcurrency(concurrency)
	candidates, source, err := discoverModelsForChannelConcurrent(ctx, cfg, s, channel, concurrency)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%s has no discoverable models", channel)
	}

	result, err := applyModelRefresh(ctx, s, channel, source, candidates)
	if result != nil {
		result.Concurrency = concurrency
	}
	return result, err
}

func normalizeAdminModelChannel(channel string) string {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "puter":
		return "Puter"
	case "kimchi":
		return "Kimchi"
	default:
		return ""
	}
}

func parseModelRefreshConcurrency(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return defaultModelRefreshConcurrency, true
	}
	return normalizeModelRefreshConcurrency(value), true
}

func normalizeModelRefreshConcurrency(concurrency int) int {
	if concurrency <= 0 {
		return defaultModelRefreshConcurrency
	}
	if concurrency > maxModelRefreshConcurrency {
		return maxModelRefreshConcurrency
	}
	return concurrency
}

func boundedModelRefreshWorkers(total int, concurrency int) int {
	if total <= 0 {
		return 0
	}
	workers := normalizeModelRefreshConcurrency(concurrency)
	if workers > total {
		workers = total
	}
	return workers
}

func discoverModelsForChannel(ctx context.Context, cfg *config.Config, s *store.Store, channel string) ([]discoveredModel, string, error) {
	return discoverModelsForChannelConcurrent(ctx, cfg, s, channel, defaultModelRefreshConcurrency)
}

func discoverModelsForChannelConcurrent(ctx context.Context, cfg *config.Config, s *store.Store, channel string, concurrency int) ([]discoveredModel, string, error) {
	switch strings.ToLower(channel) {
	case "puter":
		return discoverPuterModelsConcurrent(ctx, cfg, s, concurrency)
	case "kimchi":
		return discoverKimchiModels(ctx, cfg, s)
	default:
		return nil, "", fmt.Errorf("unsupported channel: %s", channel)
	}
}

func discoverPuterModelsConcurrent(ctx context.Context, cfg *config.Config, s *store.Store, concurrency int) ([]discoveredModel, string, error) {
	proxyFunc := http.ProxyFromEnvironment
	if cfg != nil {
		proxyFunc = util.ProxyFuncFromConfig(cfg)
	}
	items, err := fetchPuterPublicModelChoices(ctx, proxyFunc)
	source := "puter_public_models"
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, "", fmt.Errorf("puter public model discovery failed: %w", err)
		}
		return nil, "", fmt.Errorf("puter public model discovery returned no choices")
	}

	candidates := puterChoicesToDiscovered(items)
	if len(candidates) == 0 {
		return nil, "", fmt.Errorf("puter has no discoverable models")
	}

	accounts, accErr := enabledAccountsByType(ctx, s, "puter")
	if accErr != nil || len(accounts) == 0 {
		if accErr != nil {
			return candidates, source + "_unverified", nil
		}
		return candidates, source + "_unverified", nil
	}

	summary := verifyPuterDiscoveredModelsConcurrent(ctx, cfg, accounts, candidates, concurrency)
	verified := summary.Verified
	if len(verified) == 0 && summary.SawInsufficientFunds {
		return candidates, source + "_quota_limited", nil
	}
	if len(verified) == 0 {
		return nil, "", fmt.Errorf("no puter models verified by test_mode")
	}
	return verified, source + "_test_mode", nil
}

func discoverKimchiModels(ctx context.Context, cfg *config.Config, s *store.Store) ([]discoveredModel, string, error) {
	if s == nil {
		return nil, "", fmt.Errorf("store not configured")
	}
	accounts, err := enabledAccountsByType(ctx, s, "kimchi")
	if err != nil || len(accounts) == 0 {
		if err != nil {
			return nil, "", fmt.Errorf("no enabled kimchi accounts: %w", err)
		}
		return nil, "", fmt.Errorf("no enabled kimchi accounts for model discovery")
	}
	// Use the first enabled kimchi account to fetch the model catalog.
	acc := accounts[0]
	models, err := kimchi.RefreshModels(ctx, acc, cfg)
	if err != nil {
		return nil, "", fmt.Errorf("kimchi model discovery failed: %w", err)
	}
	if len(models) == 0 {
		return nil, "", fmt.Errorf("kimchi returned no models")
	}
	candidates := make([]discoveredModel, len(models))
	for i, m := range models {
		candidates[i] = discoveredModel{ID: m.ModelID, Name: firstNonEmpty(m.Name, m.ModelID), SortOrder: i}
	}
	return candidates, "kimchi_metadata", nil
}

func puterChoicesToDiscovered(items []puterPublicModelChoice) []discoveredModel {
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

type puterModelVerificationSummary struct {
	Verified             []discoveredModel
	SawInsufficientFunds bool
}

func verifyPuterDiscoveredModelsConcurrent(ctx context.Context, cfg *config.Config, accounts []*store.Account, candidates []discoveredModel, concurrency int) puterModelVerificationSummary {
	if len(accounts) == 0 || len(candidates) == 0 {
		return puterModelVerificationSummary{}
	}
	workerCount := boundedModelRefreshWorkers(len(candidates), concurrency)
	if workerCount <= 1 {
		return verifyPuterDiscoveredModelsSerial(ctx, cfg, accounts, candidates)
	}

	results := make([]puterModelProbeResult, len(candidates))
	jobs := make(chan int, len(candidates))
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer wg.Done()
			for idx := range jobs {
				candidate := candidates[idx]
				if strings.TrimSpace(candidate.ID) == "" {
					continue
				}
				startAccount := idx % len(accounts)
				allDefinitiveRejects := true
				for attempt := 0; attempt < len(accounts); attempt++ {
					if err := ctx.Err(); err != nil {
						allDefinitiveRejects = false
						break
					}
					acc := accounts[(startAccount+attempt)%len(accounts)]
					err := verifyPuterModelForRefresh(ctx, cfg, acc, candidate.ID)
					if err == nil {
						results[idx] = puterModelProbeAccepted
						break
					}
					if isPuterInsufficientFundsError(err) {
						results[idx] = results[idx].withInsufficientFunds()
					}
					if !isPuterModelDefinitiveReject(err) {
						allDefinitiveRejects = false
					}
				}
				if results[idx] != puterModelProbeAccepted && results[idx] != puterModelProbeQuotaLimited && allDefinitiveRejects {
					results[idx] = puterModelProbeRejected
				}
			}
		}()
	}
	for idx := range candidates {
		jobs <- idx
	}
	close(jobs)
	wg.Wait()

	verified := make([]discoveredModel, 0, len(candidates))
	sawInsufficientFunds := false
	for idx, candidate := range candidates {
		if results[idx] == puterModelProbeQuotaLimited {
			sawInsufficientFunds = true
		}
		if results[idx] != puterModelProbeAccepted {
			continue
		}
		candidate.SortOrder = len(verified)
		verified = append(verified, candidate)
	}
	return puterModelVerificationSummary{Verified: verified, SawInsufficientFunds: sawInsufficientFunds}
}

func verifyPuterDiscoveredModelsSerial(ctx context.Context, cfg *config.Config, accounts []*store.Account, candidates []discoveredModel) puterModelVerificationSummary {
	verified := make([]discoveredModel, 0, len(candidates))
	sawInsufficientFunds := false
	accountIndex := 0
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.ID) == "" {
			continue
		}
		ok := false
		for attempt := 0; attempt < len(accounts); attempt++ {
			if err := ctx.Err(); err != nil {
				break
			}
			acc := accounts[(accountIndex+attempt)%len(accounts)]
			err := verifyPuterModelForRefresh(ctx, cfg, acc, candidate.ID)
			if err == nil {
				ok = true
				accountIndex = (accountIndex + attempt + 1) % len(accounts)
				break
			}
			if isPuterInsufficientFundsError(err) {
				sawInsufficientFunds = true
			}
		}
		if ok {
			candidate.SortOrder = len(verified)
			verified = append(verified, candidate)
		}
	}
	return puterModelVerificationSummary{Verified: verified, SawInsufficientFunds: sawInsufficientFunds}
}

type puterModelProbeResult uint8

const (
	puterModelProbeUnknown puterModelProbeResult = iota
	puterModelProbeAccepted
	puterModelProbeRejected
	puterModelProbeQuotaLimited
)

func (r puterModelProbeResult) withInsufficientFunds() puterModelProbeResult {
	if r == puterModelProbeAccepted {
		return r
	}
	return puterModelProbeQuotaLimited
}

func isPuterInsufficientFundsError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "insufficient_funds") ||
		strings.Contains(text, "available funding is insufficient") ||
		strings.Contains(text, "insufficient funding") ||
		strings.Contains(text, "status=402") ||
		strings.Contains(text, "status 402")
}

func isPuterModelDefinitiveReject(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if text == "" {
		return false
	}
	if strings.Contains(text, "model not found") ||
		strings.Contains(text, "invalid model") ||
		strings.Contains(text, "unknown model") ||
		strings.Contains(text, "unsupported model") {
		return true
	}
	return false
}

func refreshModelRequestConfig(cfg *config.Config, channel string) *config.Config {
	if cfg == nil {
		cfg = &config.Config{}
	} else {
		copyCfg := *cfg
		cfg = &copyCfg
	}

	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "puter":
		if cfg.RequestTimeout <= 0 || cfg.RequestTimeout > 15 {
			cfg.RequestTimeout = 15
		}
	}

	return cfg
}

func enabledAccountsByType(ctx context.Context, s *store.Store, accountType string) ([]*store.Account, error) {
	accounts, err := s.GetEnabledAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.Account, 0, len(accounts))
	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(acc.AccountType), accountType) {
			out = append(out, acc)
		}
	}
	return out, nil
}

func applyModelRefresh(ctx context.Context, s *store.Store, channel string, source string, candidates []discoveredModel) (*modelRefreshResult, error) {
	existingModels, err := s.ListModels(ctx)
	if err != nil {
		return nil, err
	}

	result := &modelRefreshResult{
		Channel:    channel,
		Source:     source,
		Discovered: len(candidates),
	}

	existingByID := make(map[string]*store.Model)
	fetchedSet := make(map[string]discoveredModel, len(candidates))
	for _, model := range candidates {
		fetchedSet[model.ID] = model
	}
	result.Verified = len(candidates)

	for _, model := range existingModels {
		if model == nil || !strings.EqualFold(strings.TrimSpace(model.Channel), channel) {
			continue
		}
		existingByID[model.ModelID] = model
	}

	defaultModelID := chooseRefreshedDefaultModel(channel, existingByID, candidates)
	result.DefaultModelID = defaultModelID

	for _, model := range candidates {
		existing := existingByID[model.ID]
		if existing == nil {
			record := &store.Model{
				Channel:   channel,
				ModelID:   model.ID,
				Name:      firstNonEmpty(model.Name, model.ID),
				Status:    store.ModelStatusAvailable,
				Verified:  true,
				IsDefault: model.ID == defaultModelID,
				SortOrder: model.SortOrder,
			}
			if err := s.CreateModel(ctx, record); err != nil {
				return nil, err
			}
			result.Added++
			result.AddedModelIDs = append(result.AddedModelIDs, model.ID)
			continue
		}
	}
	if shouldDeleteMissingModelsOnRefresh(channel, source) {
		for modelID, existing := range existingByID {
			if _, ok := fetchedSet[modelID]; ok {
				continue
			}
			if existing == nil || existing.ID == "" {
				continue
			}
			if err := s.DeleteModel(ctx, existing.ID); err != nil {
				return nil, err
			}
			result.Deleted++
			result.DeletedModelIDs = append(result.DeletedModelIDs, modelID)
		}
	}

	sort.Strings(result.AddedModelIDs)
	sort.Strings(result.DeletedModelIDs)
	sort.Strings(result.OfflineModelIDs)
	return result, nil
}

func shouldDeleteMissingModelsOnRefresh(channel, source string) bool {
	if !strings.EqualFold(strings.TrimSpace(channel), "warp") {
		return false
	}
	source = strings.TrimSpace(source)
	return strings.Contains(source, "feature_model_choice_agent_mode") || strings.Contains(source, "feature_model_choice_all")
}

func chooseRefreshedDefaultModel(channel string, existing map[string]*store.Model, ordered []discoveredModel) string {
	for _, model := range ordered {
		if current := existing[model.ID]; current != nil && current.IsDefault {
			return model.ID
		}
	}
	for _, model := range ordered {
		return model.ID
	}
	return ""
}

func discoveredModelsContain(models []discoveredModel, id string) bool {
	for _, model := range models {
		if model.ID == id {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func isRefreshableModelChannel(channel string) bool {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "puter", "codebuff", "kimchi":
		return true
	default:
		return false
	}
}
