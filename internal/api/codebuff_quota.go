package api

import (
	"net/http"
	"strings"

	"github.com/goccy/go-json"
	"orchids-api/internal/codebuff"
)

func (a *API) handleCodebuffStatus(w http.ResponseWriter, r *http.Request, id int64) {
	ctx := r.Context()
	acc, err := a.store.GetAccount(ctx, id)
	if err != nil {
		http.Error(w, "Account not found", http.StatusNotFound)
		return
	}
	if !strings.EqualFold(acc.AccountType, "codebuff") {
		http.Error(w, "Not a codebuff account", http.StatusBadRequest)
		return
	}
	if a.codebuffQuotaStore == nil {
		http.Error(w, "Quota store not available", http.StatusServiceUnavailable)
		return
	}
	status, err := a.codebuffQuotaStore.GetAccountStatus(ctx, acc.ID, acc.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

func (a *API) handleCodebuffSync(w http.ResponseWriter, r *http.Request, id int64) {
	ctx := r.Context()
	acc, err := a.store.GetAccount(ctx, id)
	if err != nil {
		http.Error(w, "Account not found", http.StatusNotFound)
		return
	}
	if !strings.EqualFold(acc.AccountType, "codebuff") {
		http.Error(w, "Not a codebuff account", http.StatusBadRequest)
		return
	}
	if a.codebuffQuotaStore == nil {
		http.Error(w, "Quota store not available", http.StatusServiceUnavailable)
		return
	}

	client := codebuff.NewClient(codebuff.ResolveAuthToken(acc), a.config.Load())

	// Fetch streak
	streakData, streakErr := client.GetStreak(ctx)
	if streakErr == nil {
		streak := codebuff.ParseStreak(streakData)
		if streak != nil {
			_ = a.codebuffQuotaStore.RecordStreak(ctx, acc.ID, streak)
		}
	}

	// Fetch session (active session data, which may include rateLimitsByModel)
	sessData, sessErr := client.GetSession(ctx, "")
	if sessErr == nil {
		limits, _ := codebuff.ParseSessionRateLimits(sessData)
		if len(limits) > 0 {
			_ = a.codebuffQuotaStore.RecordSessionQuotas(ctx, acc.ID, limits)
		}
	}

	status, err := a.codebuffQuotaStore.GetAccountStatus(ctx, acc.ID, acc.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

// HandleCodebuffPoolStatus handles GET /api/codebuff/pool-status.
func (a *API) HandleCodebuffPoolStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.codebuffQuotaStore == nil {
		http.Error(w, "Quota store not available", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()
	accounts, err := a.store.ListAccounts(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pool, err := a.codebuffQuotaStore.GetPoolStatus(ctx, accounts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(pool)
}

// SetCodebuffQuotaStore injects the codebuff quota store.
func (a *API) SetCodebuffQuotaStore(qs *codebuff.QuotaStore) {
	a.codebuffQuotaStore = qs
}
