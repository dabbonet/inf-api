package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goccy/go-json"

	"orchids-api/internal/auth"
	"orchids-api/internal/codebuff"
	"orchids-api/internal/config"
	apperrors "orchids-api/internal/errors"
	"orchids-api/internal/middleware"
	"orchids-api/internal/kimchi"
	"orchids-api/internal/puter"
	"orchids-api/internal/store"
	"orchids-api/internal/tokencache"
	"orchids-api/internal/util"
)

type API struct {	store               *store.Store
	tokenCache          tokencache.Cache
	promptCache         tokencache.PromptCache
	adminUser           string
	adminPass           string
	loginLimiter        *middleware.RateLimiter
	config              atomic.Pointer[config.Config]
	codebuffQuotaStore       *codebuff.QuotaStore
	codebuffTelemetryStore   *codebuff.TelemetryStore

	// Account check backoff / storm control
	checkMu          sync.Mutex
	checkInFlight    map[int64]bool
	checkFailCount   map[int64]int
	checkNextAllowed map[int64]time.Time
	checkSem         chan struct{}
}

var puterVerifyAccount = func(ctx context.Context, acc *store.Account, cfg *config.Config) error {
	client := puter.NewFromAccount(acc, cfg)
	defer client.Close()
	return client.VerifyAuthToken(ctx)
}

var puterFetchMonthlyUsage = func(ctx context.Context, acc *store.Account, cfg *config.Config) (*puter.MonthlyUsage, error) {
	client := puter.NewFromAccount(acc, cfg)
	defer client.Close()
	return client.FetchMonthlyUsage(ctx)
}



// classifyAccountStatusFromError delegates to the centralized errors package.
func classifyAccountStatusFromError(errStr string) string {
	return apperrors.ClassifyAccountStatus(errStr)
}

func httpStatusFromAccountStatus(status string) int {
	switch strings.TrimSpace(status) {
	case "401":
		return http.StatusUnauthorized
	case "402":
		return http.StatusPaymentRequired
	case "403":
		return http.StatusForbidden
	case "404":
		return http.StatusNotFound
	case "429":
		return http.StatusTooManyRequests
	default:
		return http.StatusBadGateway
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// resolveCodebuffAuthToken delegates to codebuff.ResolveAuthToken so the
// create/sync paths stay in lock-step with the per-request client factory.
func resolveCodebuffAuthToken(acc *store.Account) string {
	return codebuff.ResolveAuthToken(acc)
}

func normalizeAccountOutput(acc *store.Account) *store.Account {
	if acc == nil {
		return nil
	}
	out := *acc
	if strings.TrimSpace(out.Subscription) == "" && isActiveModelChannel(out.AccountType) {
		out.Subscription = "basic"
	}
	return &out
}

// ensureDefaultSubscription sets a default level for active account types
// when the caller left Subscription blank. Returns true if a default was
// applied (i.e. the field was empty before).
func ensureDefaultSubscription(acc *store.Account) bool {
	if acc == nil {
		return false
	}
	if strings.TrimSpace(acc.Subscription) != "" {
		return false
	}
	if !isActiveModelChannel(acc.AccountType) {
		return false
	}
	acc.Subscription = "basic"
	return true
}

func encodeAccountWithQuota(w http.ResponseWriter, acc *store.Account) error {
 {
		out := normalizeAccountOutput(acc)
		if out == nil {
			http.Error(w, "account not found", http.StatusNotFound)
			return fmt.Errorf("account not found")
		}
		// Use a typed alias so the JSON encoder can serialize both the embedded
		// account struct and the additional quota_* fields in one pass.
		type quotaEnvelope struct {
			*store.Account
			QuotaLimit      float64 `json:"quota_limit"`
			QuotaUsed       float64 `json:"quota_used"`
			QuotaRemaining  float64 `json:"quota_remaining"`
			QuotaMode       string  `json:"quota_mode"`
			QuotaUnit       string  `json:"quota_unit"`
			QuotaSupported  bool    `json:"quota_supported"`
		}
		fields := buildQuotaResponseFields(out)
		env := quotaEnvelope{
			Account:        out,
			QuotaLimit:     toFloat(fields["quota_limit"]),
			QuotaUsed:      toFloat(fields["quota_used"]),
			QuotaRemaining: toFloat(fields["quota_remaining"]),
			QuotaMode:      toString(fields["quota_mode"]),
			QuotaUnit:      toString(fields["quota_unit"]),
			QuotaSupported: toBool(fields["quota_supported"]),
		}
		return json.NewEncoder(w).Encode(env)
	}
}

func toFloat(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	}
	return 0
}

func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toBool(v interface{}) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func normalizedAccountCredentialKey(acc *store.Account) string {
	if acc == nil {
		return ""
	}

	accountType := strings.ToLower(strings.TrimSpace(acc.AccountType))
	var token string

	switch accountType {
	case "puter":
		token = puter.ResolveAuthToken(acc)
	case "kimchi":
		token = kimchi.ResolveAuthToken(acc)
	default:
		token = strings.TrimSpace(firstNonEmptyString(acc.RefreshToken, acc.SessionCookie, acc.ClientCookie, acc.Token))
	}

	if token == "" || accountType == "" {
		return ""
	}
	return accountType + ":" + token
}

func (a *API) findDuplicateAccountByCredential(ctx context.Context, acc *store.Account, excludeID int64) (*store.Account, error) {
	if a == nil || a.store == nil || acc == nil {
		return nil, nil
	}

	key := normalizedAccountCredentialKey(acc)
	if key == "" {
		return nil, nil
	}

	accounts, err := a.store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	for _, existing := range accounts {
		if existing == nil || existing.ID == excludeID {
			continue
		}
		if normalizedAccountCredentialKey(existing) == key {
			return existing, nil
		}
	}
	return nil, nil
}

func duplicateAccountError(existing *store.Account) error {
	if existing == nil {
		return fmt.Errorf("duplicate account token")
	}
	accountType := strings.TrimSpace(existing.AccountType)
	if accountType == "" {
		accountType = "account"
	}
	return fmt.Errorf("duplicate %s token already exists on account #%d", accountType, existing.ID)
}

func buildQuotaResponseFields(acc *store.Account) map[string]interface{} {
	fields := map[string]interface{}{
		"quota_limit":     0.0,
		"quota_used":      0.0,
		"quota_remaining": 0.0,
		"quota_mode":      "remaining",
		"quota_unit":      "credits",
		"quota_supported": true,
	}
	if acc == nil {
		return fields
	}

	limit := acc.UsageLimit
	current := acc.UsageCurrent
	if limit < 0 {
		limit = 0
	}
	if current < 0 {
		current = 0
	}

	switch strings.ToLower(strings.TrimSpace(acc.AccountType)) {
	case "puter":
		if limit <= 0 {
			fields["quota_limit"] = 0.0
			fields["quota_used"] = 0.0
			fields["quota_remaining"] = 0.0
			fields["quota_mode"] = "unknown"
			fields["quota_unit"] = "credits"
			fields["quota_supported"] = false
			break
		}
		remaining := current
		if remaining > limit {
			remaining = limit
		}
		used := limit - remaining
		if used < 0 {
			used = 0
		}
		fields["quota_limit"] = limit
		fields["quota_used"] = used
		fields["quota_remaining"] = remaining
		fields["quota_mode"] = "remaining"
		fields["quota_unit"] = "credits"
	default:
		if limit <= 0 {
			fields["quota_limit"] = 0.0
			fields["quota_used"] = 0.0
			fields["quota_remaining"] = 0.0
			fields["quota_mode"] = "unknown"
			fields["quota_unit"] = "credits"
			fields["quota_supported"] = false
			break
		}
		fields["quota_limit"] = limit
		remaining := current
		if remaining > limit {
			remaining = limit
		}
		used := limit - remaining
		if used < 0 {
			used = 0
		}
		fields["quota_used"] = used
		fields["quota_remaining"] = remaining
	}

	return fields
}

func applyPuterMonthlyUsage(acc *store.Account, usage *puter.MonthlyUsage) {
	if acc == nil || usage == nil {
		return
	}
	limit := usage.AllowanceInfo.MonthUsageAllowance
	remaining := usage.AllowanceInfo.Remaining
	if limit < 0 {
		limit = 0
	}
	if remaining < 0 {
		remaining = 0
	}
	if limit > 0 && remaining > limit {
		remaining = limit
	}
	acc.UsageCurrent = remaining
	acc.UsageLimit = limit
}

func (a *API) refreshAccountState(ctx context.Context, acc *store.Account) (string, int, error) {
	if acc == nil {
		return "", http.StatusBadRequest, fmt.Errorf("account is nil")
	}

	if strings.EqualFold(acc.AccountType, "puter") {
		if puter.ResolveAuthToken(acc) == "" {
			return "", http.StatusBadRequest, fmt.Errorf("failed to verify puter account: missing auth token")
		}
		usage, usageErr := puterFetchMonthlyUsage(ctx, acc, a.config.Load())
		if usageErr == nil {
			applyPuterMonthlyUsage(acc, usage)
			if acc.UsageLimit > 0 && acc.UsageCurrent <= 0 {
				return "402", 0, nil
			}
			return "", 0, nil
		}

		usageStatus := classifyAccountStatusFromError(usageErr.Error())
		httpStatus := http.StatusBadGateway
		if usageStatus != "" {
			httpStatus = httpStatusFromAccountStatus(usageStatus)
		}
		return usageStatus, httpStatus, fmt.Errorf("failed to fetch puter usage: %w", usageErr)
	}

	if strings.EqualFold(acc.AccountType, "codebuff") {
		return a.refreshCodebuffAccountState(ctx, acc)
	}

	return "", http.StatusBadRequest, fmt.Errorf("unsupported account type: %s", acc.AccountType)
}

func (a *API) refreshCodebuffAccountState(ctx context.Context, acc *store.Account) (string, int, error) {
	if acc == nil {
		return "", http.StatusBadRequest, fmt.Errorf("account is nil")
	}
	token := resolveCodebuffAuthToken(acc)
	if token == "" {
		return "", http.StatusBadRequest, fmt.Errorf("failed to verify codebuff account: missing bearer token")
	}
	if a.codebuffQuotaStore == nil {
		// Quota store not yet wired; treat as best-effort info rather than fatal error.
		return "", 0, nil
	}

	client := codebuff.NewClient(token, a.config.Load())

	streakData, streakErr := client.GetStreak(ctx)
	if streakErr == nil {
		if streak := codebuff.ParseStreak(streakData); streak != nil {
			_ = a.codebuffQuotaStore.RecordStreak(ctx, acc.ID, streak)
		}
	}

	sessData, sessErr := client.GetSession(ctx, "")
	if sessErr == nil {
		if limits, _ := codebuff.ParseSessionRateLimits(sessData); len(limits) > 0 {
			_ = a.codebuffQuotaStore.RecordSessionQuotas(ctx, acc.ID, limits)
		}
	}

	if streakErr != nil && sessErr != nil {
		usageStatus := classifyAccountStatusFromError(streakErr.Error())
		if usageStatus == "" {
			usageStatus = classifyAccountStatusFromError(sessErr.Error())
		}
		httpStatus := http.StatusBadGateway
		if usageStatus != "" {
			httpStatus = httpStatusFromAccountStatus(usageStatus)
		}
		if usageStatus == "" {
			usageStatus = "502"
		}
		return usageStatus, httpStatus, fmt.Errorf("failed to fetch codebuff session: %w", sessErr)
	}
	return "", 0, nil
}

type ExportData struct {
	Version  int             `json:"version"`
	ExportAt time.Time       `json:"export_at"`
	Accounts []store.Account `json:"accounts"`
}

type ImportResult struct {
	Total    int `json:"total"`
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"`
}

type CreateKeyResponse struct {
	ID        int64     `json:"id"`
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	KeyPrefix string    `json:"key_prefix"`
	KeySuffix string    `json:"key_suffix"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

type UpdateKeyRequest struct {
	Enabled *bool `json:"enabled"`
}

func New(s *store.Store, adminUser, adminPass string, cfg *config.Config) *API {
	a := &API{
		store:        s,
		adminUser:    adminUser,
		adminPass:    adminPass,
		loginLimiter: middleware.NewRateLimiter(5, 15*time.Minute),

		checkInFlight:    map[int64]bool{},
		checkFailCount:   map[int64]int{},
		checkNextAllowed: map[int64]time.Time{},
		checkSem:         make(chan struct{}, 2),
	}
	if cfg != nil {
		a.config.Store(cfg)
	}
	return a
}

func (a *API) SetPromptCache(cache tokencache.PromptCache) {
	a.promptCache = cache
}

func secureCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (a *API) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ip := middleware.ExtractIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Real-IP"))
	if a.loginLimiter != nil && !a.loginLimiter.Allow(ip) {
		http.Error(w, "Too many login attempts, try again later", http.StatusTooManyRequests)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	adminUser := a.adminUser
	adminPass := a.adminPass
	if cfg := a.config.Load(); cfg != nil {
		adminUser = cfg.AdminUser
		adminPass = cfg.AdminPass
	}

	if !secureCompare(req.Username, adminUser) || !secureCompare(req.Password, adminPass) {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	token, err := auth.GenerateSessionToken()
	if err != nil {
		slog.Error("Failed to generate session token", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// NOTE: Do not mark cookies as Secure when served over plain HTTP,
	// otherwise browsers will drop the cookie and the Admin UI will appear unable to log in.
	// When behind a TLS-terminating proxy, honor X-Forwarded-Proto.
	isHTTPS := r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")

	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400 * 7,
	})

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (a *API) HandleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session_token")
	if err == nil {
		auth.InvalidateSessionToken(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (a *API) HandleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(a.config.Load())
	case http.MethodPost:
		// Copy current config, decode into copy, then atomically store
		current := a.config.Load()
		newCfg := *current
		if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := a.persistConfig(r.Context(), current, &newCfg); err != nil {
			http.Error(w, "Failed to save config: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(&newCfg)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *API) HandleConfigList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	data, err := configPayload(a.config.Load())
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1,
			"msg":  "Failed to get config: " + err.Error(),
		})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code": 0,
		"data": data,
	})
}

func (a *API) HandleConfigSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	current := a.config.Load()
	newCfg, err := buildConfigFromPatch(r, current)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1,
			"msg":  "parse request failed: " + err.Error(),
		})
		return
	}
	if err := a.persistConfig(r.Context(), current, newCfg); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1,
			"msg":  "save config failed: " + err.Error(),
		})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code": 0,
		"msg":  "success",
	})
}

func (a *API) HandleAccounts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		accounts, err := a.store.ListAccounts(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if accounts == nil {
			accounts = []*store.Account{}
		}
		type quotaEnvelope struct {
			*store.Account
			QuotaLimit     float64 `json:"quota_limit"`
			QuotaUsed      float64 `json:"quota_used"`
			QuotaRemaining float64 `json:"quota_remaining"`
			QuotaMode      string  `json:"quota_mode"`
			QuotaUnit      string  `json:"quota_unit"`
			QuotaSupported bool    `json:"quota_supported"`
		}
		out := make([]quotaEnvelope, 0, len(accounts))
		for _, acc := range accounts {
			if acc == nil {
				continue
			}
			nacc := normalizeAccountOutput(acc)
			if nacc == nil {
				continue
			}
			fields := buildQuotaResponseFields(nacc)
			out = append(out, quotaEnvelope{
				Account:        nacc,
				QuotaLimit:     toFloat(fields["quota_limit"]),
				QuotaUsed:      toFloat(fields["quota_used"]),
				QuotaRemaining: toFloat(fields["quota_remaining"]),
				QuotaMode:      toString(fields["quota_mode"]),
				QuotaUnit:      toString(fields["quota_unit"]),
				QuotaSupported: toBool(fields["quota_supported"]),
			})
		}
		json.NewEncoder(w).Encode(out)

	case http.MethodPost:
		var acc store.Account
		if err := json.NewDecoder(r.Body).Decode(&acc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.EqualFold(acc.AccountType, "codebuff") {
			acc.NSFWEnabled = true
		}
		ensureDefaultSubscription(&acc)
		if existing, err := a.findDuplicateAccountByCredential(r.Context(), &acc, 0); err != nil {
			slog.Error("Failed to detect duplicate account token", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		} else if existing != nil {
			http.Error(w, duplicateAccountError(existing).Error(), http.StatusConflict)
			return
		}

		if err := a.store.CreateAccount(r.Context(), &acc); err != nil {
			slog.Error("Failed to create account", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if strings.TrimSpace(acc.Token) == "" {
			acc.Token = truncateAccountDisplayToken(&acc)
		}

		if acc.Enabled && shouldSyncAccountOnCreate(&acc) {
			if wantsAsyncAccountSync(r) {
				a.syncAccountAfterCreate(acc)
			} else {
				syncCtx, syncCancel := context.WithTimeout(r.Context(), 25*time.Second)
				accountStatus, _, syncErr := a.refreshAccountState(syncCtx, &acc)
				syncCancel()
				if syncErr != nil {
					slog.Warn("Initial account sync failed", "account_id", acc.ID, "type", acc.AccountType, "error", syncErr)
					if accountStatus != "" {
						acc.StatusCode = accountStatus
						acc.LastAttempt = time.Now()
					}
				} else {
					applySuccessfulAccountRefreshStatus(&acc, accountStatus)
				}
				if updateErr := a.store.UpdateAccount(r.Context(), &acc); updateErr != nil {
					slog.Warn("Failed to persist initial account sync", "account_id", acc.ID, "type", acc.AccountType, "error", updateErr)
				}
			}
		}

		w.WriteHeader(http.StatusCreated)
		encodeAccountWithQuota(w, &acc)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *API) HandleAccountByID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	path := strings.TrimPrefix(r.URL.Path, "/api/accounts/")
	parts := strings.Split(path, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	isRefresh := len(parts) > 1 && parts[1] == "refresh"
	isVerify := len(parts) > 1 && parts[1] == "verify"
	isCheck := len(parts) > 1 && parts[1] == "check"
	isUsage := len(parts) > 1 && parts[1] == "usage"
	isCodebuffStatus := len(parts) > 1 && parts[1] == "codebuff-status"
	isCodebuffSync := len(parts) > 1 && parts[1] == "codebuff-sync"

	switch r.Method {
	case http.MethodGet:
		if isCodebuffStatus {
			a.handleCodebuffStatus(w, r, id)
			return
		}
		if isRefresh || isVerify {
			http.Error(w, "Deprecated endpoint. Use /api/accounts/{id}/check instead.", http.StatusGone)
			return
		}
		if isUsage {
			acc, err := a.store.GetAccount(r.Context(), id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			resp := map[string]interface{}{
				"account_id":     acc.ID,
				"name":           acc.Name,
				"account_type":   acc.AccountType,
				"subscription":   acc.Subscription,
				"usage_current":  acc.UsageCurrent,
				"usage_limit":    acc.UsageLimit,
				"usage_total":    acc.UsageTotal,
				"quota_reset_at": acc.QuotaResetAt,
			}
			for k, v := range buildQuotaResponseFields(acc) {
				resp[k] = v
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		if isCheck {
			// Storm control / backoff: only allow a small number of concurrent checks,
			// and apply exponential backoff per account on failures.
			now := time.Now()
			a.checkMu.Lock()
			if a.checkInFlight[id] {
				a.checkMu.Unlock()
				http.Error(w, "account check already in progress", http.StatusTooManyRequests)
				return
			}
			if next, ok := a.checkNextAllowed[id]; ok && !next.IsZero() && now.Before(next) {
				retryAfter := int(next.Sub(now).Seconds())
				if retryAfter < 1 {
					retryAfter = 1
				}
				a.checkMu.Unlock()
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				http.Error(w, "account check backoff", http.StatusTooManyRequests)
				return
			}
			a.checkInFlight[id] = true
			a.checkMu.Unlock()
			defer func() {
				a.checkMu.Lock()
				delete(a.checkInFlight, id)
				a.checkMu.Unlock()
			}()

			// global concurrency limit
			a.checkSem <- struct{}{}
			defer func() { <-a.checkSem }()

			acc, err := a.store.GetAccount(r.Context(), id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}

			checkOK := false
			checkErrStatus := ""
			defer func() {
				a.checkMu.Lock()
				defer a.checkMu.Unlock()
				if checkOK {
					a.checkFailCount[id] = 0
					a.checkNextAllowed[id] = time.Now().Add(3 * time.Second)
					return
				}
				fails := a.checkFailCount[id] + 1
				a.checkFailCount[id] = fails
				d := time.Duration(1<<util.MinInt(fails, 8)) * time.Second
				// For CF/rate-limit style failures, start with a bigger cooldown.
				if checkErrStatus == "403" || checkErrStatus == "429" {
					if d < 60*time.Second {
						d = 60 * time.Second
					}
				}
				if d > 10*time.Minute {
					d = 10 * time.Minute
				}
				a.checkNextAllowed[id] = time.Now().Add(d)
			}()

			accountStatus, httpStatus, refreshErr := a.refreshAccountState(r.Context(), acc)
			if refreshErr != nil {
				checkErrStatus = accountStatus
				if accountStatus != "" {
					acc.StatusCode = accountStatus
					acc.LastAttempt = time.Now()
					if updateErr := a.store.UpdateAccount(r.Context(), acc); updateErr != nil {
						slog.Warn("Failed to persist account refresh status", "account_id", acc.ID, "error", updateErr)
					}
				}
				if httpStatus == 0 {
					httpStatus = http.StatusBadRequest
				}
				http.Error(w, refreshErr.Error(), httpStatus)
				return
			}

			// Clear account status after successful refresh/verification
			applySuccessfulAccountRefreshStatus(acc, accountStatus)
			checkOK = true

			if err := a.store.UpdateAccount(r.Context(), acc); err != nil {
				http.Error(w, "Failed to save checked account: "+err.Error(), http.StatusInternalServerError)
				return
			}
			encodeAccountWithQuota(w, acc)
			return
		}
		acc, err := a.store.GetAccount(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		encodeAccountWithQuota(w, acc)

	case http.MethodPost:
		if isCodebuffSync {
			a.handleCodebuffSync(w, r, id)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

	case http.MethodPut:
		existing, err := a.store.GetAccount(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}

		var acc store.Account
		if err := json.NewDecoder(r.Body).Decode(&acc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		acc.ID = id
		if strings.TrimSpace(acc.AccountType) == "" {
			acc.AccountType = existing.AccountType
		}
		

		if acc.SessionID == "" {
			acc.SessionID = existing.SessionID
		}
		if acc.SessionCookie == "" {
			acc.SessionCookie = existing.SessionCookie
		}
		if acc.ClientUat == "" {
			acc.ClientUat = existing.ClientUat
		}
		if acc.ProjectID == "" {
			acc.ProjectID = existing.ProjectID
		}
		if acc.UserID == "" {
			acc.UserID = existing.UserID
		}
		if acc.Email == "" {
			acc.Email = existing.Email
		}
		if duplicate, err := a.findDuplicateAccountByCredential(r.Context(), &acc, id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		} else if duplicate != nil {
			http.Error(w, duplicateAccountError(duplicate).Error(), http.StatusConflict)
			return
		}
		ensureDefaultSubscription(&acc)

		if err := a.store.UpdateAccount(r.Context(), &acc); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if strings.TrimSpace(acc.Token) == "" {
			acc.Token = truncateAccountDisplayToken(&acc)
			if updateErr := a.store.UpdateAccount(r.Context(), &acc); updateErr != nil {
				slog.Warn("Failed to persist derived display token", "account_id", acc.ID, "error", updateErr)
			}
		}
		json.NewEncoder(w).Encode(normalizeAccountOutput(&acc))

	case http.MethodDelete:
		if err := a.store.DeleteAccount(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *API) HandleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	accounts, err := a.store.ListAccounts(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	exportData := ExportData{
		Version:  1,
		ExportAt: time.Now(),
		Accounts: make([]store.Account, len(accounts)),
	}
	for i, acc := range accounts {
		exportData.Accounts[i] = *normalizeAccountOutput(acc)
		exportData.Accounts[i].ID = 0
		exportData.Accounts[i].RequestCount = 0
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=accounts_export.json")
	json.NewEncoder(w).Encode(exportData)
}

func (a *API) HandleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var exportData ExportData
	if err := json.NewDecoder(r.Body).Decode(&exportData); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	result := ImportResult{Total: len(exportData.Accounts)}

	for _, acc := range exportData.Accounts {
		acc.ID = 0
		acc.RequestCount = 0
		if err := a.store.CreateAccount(r.Context(), &acc); err != nil {
			slog.Warn("Failed to import account", "name", acc.Name, "error", err)
			result.Skipped++
		} else {
			result.Imported++
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func generateApiKey() (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	b := make([]byte, 48)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		b[i] = charset[n.Int64()]
	}
	return "sk-" + string(b), nil
}

func (a *API) HandleKeys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		keys, err := a.store.ListApiKeys(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(keys)

	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}

		fullKey, err := generateApiKey()
		if err != nil {
			slog.Error("Failed to generate api key", "error", err)
			http.Error(w, "failed to generate api key", http.StatusInternalServerError)
			return
		}

		hash := sha256.Sum256([]byte(fullKey))
		hashStr := hex.EncodeToString(hash[:])
		key := store.ApiKey{
			Name:      req.Name,
			KeyHash:   hashStr,
			KeyFull:   fullKey,
			KeyPrefix: "sk-",
			KeySuffix: fullKey[len(fullKey)-4:],
			Enabled:   true,
		}
		if err := a.store.CreateApiKey(r.Context(), &key); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(CreateKeyResponse{
			ID:        key.ID,
			Key:       fullKey,
			Name:      key.Name,
			KeyPrefix: key.KeyPrefix,
			KeySuffix: key.KeySuffix,
			Enabled:   key.Enabled,
			CreatedAt: key.CreatedAt,
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *API) HandleKeyByID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	idStr := strings.TrimPrefix(r.URL.Path, "/api/keys/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPatch:
		var req UpdateKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Enabled == nil {
			http.Error(w, "enabled is required", http.StatusBadRequest)
			return
		}

		if err := a.store.UpdateApiKeyEnabled(r.Context(), id, *req.Enabled); err != nil {
			if errors.Is(err, store.ErrNoRows) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		key, err := a.store.GetApiKeyByID(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if key == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(key)

	case http.MethodDelete:
		if err := a.store.DeleteApiKey(r.Context(), id); err != nil {
			if errors.Is(err, store.ErrNoRows) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *API) HandleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		models, err := a.store.ListModels(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		filtered := make([]*store.Model, 0, len(models))
		for _, model := range models {
			if model == nil {
				continue
			}
			if !isActiveModelChannel(model.Channel) {
				continue
			}
			filtered = append(filtered, model)
		}
		json.NewEncoder(w).Encode(filtered)

	case http.MethodPost:
		var m store.Model
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if !isActiveModelChannel(m.Channel) {
			http.Error(w, fmt.Sprintf("channel %q is no longer supported; use Puter or Codebuff", m.Channel), http.StatusBadRequest)
			return
		}

		if err := a.store.CreateModel(r.Context(), &m); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(m)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *API) HandleModelByID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	id := strings.TrimPrefix(r.URL.Path, "/api/models/")
	if id == "" {
		http.Error(w, "Model ID required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		m, err := a.store.GetModel(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNoRows) || err.Error() == "redis: nil" {
				http.Error(w, "Model not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !isActiveModelChannel(m.Channel) {
			http.Error(w, "Model not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(m)

	case http.MethodPut:
		var m store.Model
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m.ID = id

		if !isActiveModelChannel(m.Channel) {
			http.Error(w, fmt.Sprintf("channel %q is no longer supported; use Puter or Codebuff", m.Channel), http.StatusBadRequest)
			return
		}

		if err := a.store.UpdateModel(r.Context(), &m); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(m)

	case http.MethodDelete:
		if err := a.store.DeleteModel(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *API) SetTokenCache(c tokencache.Cache) {
	a.tokenCache = c
}

func (a *API) HandleCacheClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if a.tokenCache == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := a.tokenCache.Clear(r.Context()); err != nil {
		http.Error(w, "Failed to clear cache: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func tokenCacheConfigChanged(before, after *config.Config) bool {
	if before == nil || after == nil {
		return true
	}
	return before.CacheTokenCount != after.CacheTokenCount ||
		before.CacheTTL != after.CacheTTL ||
		before.CacheStrategy != after.CacheStrategy ||
		before.EnableTokenCache != after.EnableTokenCache ||
		before.TokenCacheTTL != after.TokenCacheTTL ||
		before.TokenCacheStrategy != after.TokenCacheStrategy
}

func (a *API) clearTokenCaches(ctx context.Context) {
	if a.tokenCache != nil {
		if err := a.tokenCache.Clear(ctx); err != nil {
			slog.Warn("failed to clear token cache after config update", "error", err)
		}
	}
	if a.promptCache != nil {
		if err := a.promptCache.Clear(ctx); err != nil {
			slog.Warn("failed to clear prompt cache after config update", "error", err)
		}
	}
}

func configPayload(cfg *config.Config) (map[string]interface{}, error) {
	if cfg == nil {
		return map[string]interface{}{}, nil
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	payload := map[string]interface{}{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if v, ok := payload["admin_pass"]; ok {
		payload["admin_password"] = v
	}
	if rawProxyURL, ok := payload["proxy_url"].(string); (!ok || strings.TrimSpace(rawProxyURL) == "") && cfg != nil {
		if proxyURL := util.ProxyURLFromConfig(cfg); proxyURL != nil {
			payload["proxy_url"] = proxyURL.String()
		}
	}
	return payload, nil
}

func buildConfigFromPatch(r *http.Request, current *config.Config) (*config.Config, error) {
	base := &config.Config{}
	if current != nil {
		copyCfg := *current
		base = &copyCfg
	}

	baseMap, err := configPayload(base)
	if err != nil {
		return nil, err
	}

	patch := map[string]interface{}{}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		return nil, err
	}

	if v, ok := patch["admin_password"]; ok {
		patch["admin_pass"] = v
	}

	for key, value := range patch {
		baseMap[key] = normalizeConfigPatchValue(key, value)
	}
	if _, ok := patch["proxy_url"]; ok {
		baseMap["proxy_http"] = ""
		baseMap["proxy_https"] = ""
		baseMap["proxy_user"] = ""
		baseMap["proxy_pass"] = ""
	}

	raw, err := json.Marshal(baseMap)
	if err != nil {
		return nil, err
	}
	var newCfg config.Config
	if err := json.Unmarshal(raw, &newCfg); err != nil {
		return nil, err
	}
	return &newCfg, nil
}

func normalizeConfigPatchValue(key string, value interface{}) interface{} {
	if value == nil {
		return nil
	}

	switch key {
	case "enable_token_refresh", "enable_usage_refresh", "enable_token_count", "cache_token_count",
		"enable_token_cache", "auto_refresh_token", "kiro_use_builtin_proxy", "warp_use_builtin_proxy",
		"antigravity_use_builtin_proxy", "warp_credit_refund",
		"enable_context_compress", "debug_enabled":
		if b, ok := parseBoolish(value); ok {
			return b
		}
	case "retry_delay", "request_timeout", "refresh_interval", "cache_ttl", "token_cache_ttl",
		"redis_db", "token_refresh_interval", "load_balancer_cache_ttl", "concurrency_limit",
		"concurrency_timeout", "max_retries", "credential_retries":
		if i, ok := parseIntish(value); ok {
			return i
		}
	case "proxy_bypass":
		return normalizeProxyBypassValue(value)
	case "proxy_url":
		return strings.TrimSpace(fmt.Sprint(value))
	}

	return value
}

func parseBoolish(value interface{}) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		s := strings.TrimSpace(strings.ToLower(v))
		switch s {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		}
	case float64:
		return v != 0, true
	}
	return false, false
}

func parseIntish(value interface{}) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

func normalizeProxyBypassValue(value interface{}) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s := strings.TrimSpace(fmt.Sprint(item))
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		lines := strings.FieldsFunc(v, func(r rune) bool {
			return r == '\n' || r == ','
		})
		out := make([]string, 0, len(lines))
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" {
				out = append(out, line)
			}
		}
		return out
	default:
		return nil
	}
}

func shouldSyncAccountOnCreate(acc *store.Account) bool {
	if acc == nil {
		return false
	}
	return true
}

func wantsAsyncAccountSync(r *http.Request) bool {
	if r == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Account-Sync")), "async")
}

func (a *API) syncAccountAfterCreate(acc store.Account) {
	if !acc.Enabled || !shouldSyncAccountOnCreate(&acc) {
		return
	}

	go func(account store.Account) {
		syncCtx, syncCancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer syncCancel()

		accountStatus, _, syncErr := a.refreshAccountState(syncCtx, &account)
		if syncErr != nil {
			slog.Warn("Initial account sync failed", "account_id", account.ID, "type", account.AccountType, "error", syncErr)
			if accountStatus != "" {
				account.StatusCode = accountStatus
				account.LastAttempt = time.Now()
			}
		} else {
			applySuccessfulAccountRefreshStatus(&account, accountStatus)
		}

		if updateErr := a.store.UpdateAccount(context.Background(), &account); updateErr != nil {
			slog.Warn("Failed to persist initial account sync", "account_id", account.ID, "type", account.AccountType, "error", updateErr)
		}
	}(acc)
}

func applySuccessfulAccountRefreshStatus(acc *store.Account, status string) {
	if acc == nil {
		return
	}
	if strings.TrimSpace(status) == "" {
		acc.StatusCode = ""
		acc.LastAttempt = time.Time{}
		return
	}
	acc.StatusCode = strings.TrimSpace(status)
	acc.LastAttempt = time.Now()
}

func applyAccountStatusFromError(acc *store.Account, err error) {
	if acc == nil || err == nil {
		return
	}
	status := classifyAccountStatusFromError(err.Error())
	if status == "" {
		return
	}
	acc.StatusCode = status
	acc.LastAttempt = time.Now()
}

func (a *API) persistConfig(ctx context.Context, current, newCfg *config.Config) error {
	if newCfg == nil {
		return fmt.Errorf("config is nil")
	}
	if a.store == nil {
		return fmt.Errorf("settings store not configured")
	}

	config.ApplyHardcoded(newCfg)

	data, err := json.Marshal(newCfg)
	if err != nil {
		return err
	}

	// Keep the original shared config pointer updated in place so long-lived
	// components started with that pointer (handler/background loops/providers)
	// observe runtime config changes such as proxy updates immediately.
	storedCfg := newCfg
	if current != nil {
		*current = *newCfg
		storedCfg = current
	}
	a.config.Store(storedCfg)
	if err := a.store.SetSetting(ctx, "config", string(data)); err != nil {
		return err
	}
	if tokenCacheConfigChanged(current, newCfg) {
		a.clearTokenCaches(ctx)
	}
	return nil
}

func isActiveModelChannel(channel string) bool {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "puter", "codebuff", "kimchi":
		return true
	default:
		return false
	}
}

func truncateAccountDisplayToken(acc *store.Account) string {
	if acc == nil {
		return ""
	}
	var raw string
	switch strings.ToLower(strings.TrimSpace(acc.AccountType)) {
	case "warp":
		raw = firstNonEmptyString(acc.RefreshToken, acc.SessionCookie, acc.ClientCookie, acc.Token)
	case "kimchi":
		raw = firstNonEmptyString(acc.RefreshToken, acc.SessionCookie, acc.ClientCookie, acc.Token)
	default:
		raw = firstNonEmptyString(acc.ClientCookie, acc.SessionCookie, acc.RefreshToken, acc.Token)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if len(raw) <= 30 {
		return raw
	}
	return raw[:30] + "..."
}
