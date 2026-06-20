package api

import (
	"github.com/goccy/go-json"
	"net/http"
	"strings"

	"orchids-api/internal/codebuff"
)

// HandleCodebuffMetrics handles GET /api/codebuff/metrics and returns the
// per-account, per-model telemetry counters we record in Redis.
func (a *API) HandleCodebuffMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if a.codebuffTelemetryStore == nil {
		http.Error(w, "Telemetry store not available", http.StatusServiceUnavailable)
		return
	}

	accounts, err := a.store.ListAccounts(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	refs := make([]codebuff.AccountRef, 0, len(accounts))
	for _, acc := range accounts {
		if acc == nil || !strings.EqualFold(acc.AccountType, "codebuff") {
			continue
		}
		refs = append(refs, codebuff.AccountRef{ID: acc.ID, Name: acc.Name})
	}

	metrics, err := a.codebuffTelemetryStore.GetAccountsMetrics(ctx, refs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(metrics)
}

// SetCodebuffTelemetryStore injects the telemetry store.
func (a *API) SetCodebuffTelemetryStore(ts *codebuff.TelemetryStore) {
	a.codebuffTelemetryStore = ts
}
