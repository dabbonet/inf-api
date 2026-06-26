package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/goccy/go-json"
	"orchids-api/internal/codebuff"
)

// rangeQueryAlias maps the human-friendly range labels used in the dashboard
// UI to the underlying lookback window in seconds. The JSON consumer stays
// backward-compatible — "?range=all" keeps the previous lifetime aggregate.
var rangeQueryAlias = map[string]int64{
	"all":   0,           // legacy behaviour: lifetime aggregate hash
	"today": 24 * 3600,   // since midnight UTC
	"24h":   24 * 3600,
	"6h":    6 * 3600,
	"1h":    1 * 3600,
	"15m":   15 * 60,
}

// HandleCodebuffMetrics handles GET /api/codebuff/metrics and returns the
// per-account, per-model telemetry counters we record in Redis.
//
// Optional query parameter: ?range=1h | 6h | 24h | today | 15m | all
// When set, totals are derived from per-minute buckets (Feature 1).
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

	rangeSeconds := int64(0)
	if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("range"))); q != "" {
		if v, ok := rangeQueryAlias[q]; ok {
			rangeSeconds = v
		} else if n, perr := strconv.ParseInt(q, 10, 64); perr == nil && n >= 0 {
			rangeSeconds = n
		}
	}

	var metrics []codebuff.AccountMetrics
	if rangeSeconds > 0 {
		metrics, err = a.codebuffTelemetryStore.GetAccountsMetricsInRange(ctx, refs, rangeSeconds)
	} else {
		metrics, err = a.codebuffTelemetryStore.GetAccountsMetrics(ctx, refs)
	}
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
