package handler

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	apperrors "orchids-api/internal/errors"
	"orchids-api/internal/store"
)

// accountStatusMu protects concurrent markAccountStatus calls,
// Avoid multiple goroutines from modifying the StatusCode/LastAttempt of the same Account at the same time.
var accountStatusMu sync.Mutex

// classifyAccountStatus delegates to the centralized errors package.
func classifyAccountStatus(errStr string) string {
	return apperrors.ClassifyAccountStatus(errStr)
}

func isWarpQuotaExhaustedError(errStr string) bool {
	lower := strings.ToLower(errStr)
	return strings.Contains(lower, "no ai credits remaining") ||
		strings.Contains(lower, "no remaining quota") ||
		strings.Contains(lower, "quota_limit") ||
		strings.Contains(lower, "out of credits") ||
		strings.Contains(lower, "credits exhausted") ||
		strings.Contains(lower, "run out of credits")
}

func isWarpCloudAgentForbiddenError(errStr string) bool {
	lower := strings.ToLower(errStr)
	return strings.Contains(lower, "not allowed to use the provided cloud agent")
}

func markAccountStatus(ctx context.Context, store *store.Store, acc *store.Account, status string) {
	if acc == nil || store == nil || status == "" {
		return
	}

	accountStatusMu.Lock()
	defer accountStatusMu.Unlock()

	now := time.Now()
	acc.StatusCode = status
	acc.LastAttempt = now

	if err := store.UpdateAccount(ctx, acc); err != nil {
		slog.Warn("Account status update failed", "account_id", acc.ID, "status", status, "error", err)
		return
	}
	slog.Debug("Account status is marked", "account_id", acc.ID, "status", status)
}

func markWarpQuotaExhausted(ctx context.Context, store *store.Store, acc *store.Account) {
	if acc == nil || store == nil || !strings.EqualFold(strings.TrimSpace(acc.AccountType), "warp") {
		return
	}

	accountStatusMu.Lock()
	defer accountStatusMu.Unlock()

	acc.StatusCode = "429"
	acc.LastAttempt = time.Now()
	if acc.WarpMonthlyLimit <= 0 && acc.UsageLimit > 0 {
		acc.WarpMonthlyLimit = acc.UsageLimit
	}
	if acc.WarpMonthlyLimit > 0 {
		acc.WarpMonthlyRemaining = 0
		acc.WarpBonusRemaining = 0
		acc.UsageCurrent = acc.WarpMonthlyLimit
		if acc.UsageLimit <= 0 {
			acc.UsageLimit = acc.WarpMonthlyLimit
		}
	}

	if err := store.UpdateAccount(ctx, acc); err != nil {
		slog.Warn("Warp quota status update failed", "account_id", acc.ID, "error", err)
		return
	}
	slog.Debug("Warp quota has been marked as insufficient", "account_id", acc.ID)
}
