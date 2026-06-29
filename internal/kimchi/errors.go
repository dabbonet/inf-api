package kimchi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	apperrors "orchids-api/internal/errors"
)

// ClassifyError converts a SendRequestWithPayload return value into a
// inf-api AppError using upstream HTTP status + body keyword fingerprints.
//
// Codebuff has a richer classifier (model_locked / waiting_room_required /
// session_model_mismatch / ban state). Kimchi is simpler — we only see
// auth, billing, and transient failures. The state machine:
//
//   401 → authentication_error    (token expired/invalid/revoked)
//   402 → rate_limit_error        (free credits exhausted -> prompt upgrade)
//   403 → permission_denied       (account-level ban? not observed yet)
//   404 → not_found               (model slug wrong / API path typo)
//   429 → rate_limit_error        (server-side throttling)
//   5xx → upstream_error          (retryable up to maxRetries)
//
// Default fallback when *HTTPError.Body* contains "credits" or "payment":
// 402, even on a generic 403. This matches Kimchi's billing-page warnings.
//
// Use this in the admin handlers and (optionally) in the handler-tier
// error path to surface rich codes to clients.
func ClassifyError(err error) *apperrors.AppError {
	if err == nil {
		return nil
	}
	var he *HTTPError
	if !errors.As(err, &he) {
		// Network / timeout / context-cancel: classify as upstream timeout.
		if strings.Contains(err.Error(), "context") || strings.Contains(err.Error(), "timeout") {
			return apperrors.ErrUpstreamTimeout
		}
		return apperrors.ErrUpstreamUnavailable
	}
	body := strings.ToLower(he.Body)
	switch he.Status {
	case http.StatusUnauthorized:
		return apperrors.ErrUnauthorized
	case http.StatusForbidden:
		if strings.Contains(body, "credit") || strings.Contains(body, "payment") {
			return apperrors.New(apperrors.CodeRateLimitExceeded,
				"Kimchi account out of free credits — add a payment method on app.kimchi.dev or stay on the kimchi coding-agent plan.",
				http.StatusPaymentRequired)
		}
		return apperrors.New(apperrors.CodePermissionDenied,
			"Kimchi upstream returned 403 (account restricted).",
			http.StatusForbidden)
	case http.StatusNotFound:
		return apperrors.ErrModelNotFound
	case http.StatusTooManyRequests:
		return apperrors.ErrRateLimitExceeded
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return apperrors.ErrUpstreamTimeout
	}
	if he.Status >= 500 {
		return apperrors.New(apperrors.CodeUpstreamError,
			fmt.Sprintf("Kimchi upstream %d", he.Status),
			http.StatusBadGateway)
	}
	// 4xx we didn't recognise — surface the raw body so it's debuggable.
	return apperrors.New(apperrors.CodeUpstreamError,
		fmt.Sprintf("Kimchi upstream %d: %s", he.Status, strings.TrimSpace(he.Body)),
		http.StatusBadGateway)
}
