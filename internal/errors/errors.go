// Package errors provides a unified error handling mechanism
package errors

import (
	"github.com/goccy/go-json"
	"net/http"
)

// AppError represents an application layer error, including error code, message and optional reason
type AppError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"-"`
	Cause      error  `json:"-"`
}

// ToJSON returns the wrong JSON representation
func (e *AppError) ToJSON() []byte {
	data, _ := json.Marshal(map[string]interface{}{
		"type": "error",
		"error": map[string]string{
			"type":    e.Code,
			"message": e.Message,
		},
	})
	return data
}

// WriteResponse writes errors to the HTTP response
func (e *AppError) WriteResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.HTTPStatus)
	w.Write(e.ToJSON())
}

// Predefined error code
const (
	CodeInvalidRequest    = "invalid_request_error"
	CodeAuthError         = "authentication_error"
	CodePermissionDenied  = "permission_denied"
	CodeNotFound          = "not_found"
	CodeOverloaded        = "overloaded_error"
	CodeUpstreamError     = "upstream_error"
	CodeInternalError     = "internal_error"
	CodeRateLimitExceeded = "rate_limit_exceeded"
	CodeTimeout           = "timeout_error"
	CodeCircuitOpen       = "circuit_breaker_open"
)

// Predefined error instances
var (
	// Request error
	ErrInvalidRequest = &AppError{
		Code:       CodeInvalidRequest,
		Message:    "Invalid request format",
		HTTPStatus: http.StatusBadRequest,
	}
	ErrRequestBodyTooLarge = &AppError{
		Code:       CodeInvalidRequest,
		Message:    "Request body too large",
		HTTPStatus: http.StatusRequestEntityTooLarge,
	}
	ErrMethodNotAllowed = &AppError{
		Code:       CodeInvalidRequest,
		Message:    "Method not allowed",
		HTTPStatus: http.StatusMethodNotAllowed,
	}

	// Authentication error
	ErrUnauthorized = &AppError{
		Code:       CodeAuthError,
		Message:    "Authentication failed",
		HTTPStatus: http.StatusUnauthorized,
	}
	ErrInvalidToken = &AppError{
		Code:       CodeAuthError,
		Message:    "Invalid token",
		HTTPStatus: http.StatusUnauthorized,
	}
	ErrSessionExpired = &AppError{
		Code:       CodeAuthError,
		Message:    "Session expired",
		HTTPStatus: http.StatusUnauthorized,
	}

	// Resource error
	ErrAccountNotFound = &AppError{
		Code:       CodeNotFound,
		Message:    "Account not found",
		HTTPStatus: http.StatusNotFound,
	}
	ErrModelNotFound = &AppError{
		Code:       CodeNotFound,
		Message:    "Model not found",
		HTTPStatus: http.StatusNotFound,
	}
	ErrResourceNotFound = &AppError{
		Code:       CodeNotFound,
		Message:    "Resource not found",
		HTTPStatus: http.StatusNotFound,
	}

	// Service error
	ErrNoAvailableAccount = &AppError{
		Code:       CodeOverloaded,
		Message:    "No available account",
		HTTPStatus: http.StatusServiceUnavailable,
	}
	ErrUpstreamUnavailable = &AppError{
		Code:       CodeUpstreamError,
		Message:    "Upstream service unavailable",
		HTTPStatus: http.StatusBadGateway,
	}
	ErrUpstreamTimeout = &AppError{
		Code:       CodeTimeout,
		Message:    "Upstream service response timeout",
		HTTPStatus: http.StatusGatewayTimeout,
	}
	ErrCircuitBreakerOpen = &AppError{
		Code:       CodeCircuitOpen,
		Message:    "Service circuit broken, please try again later",
		HTTPStatus: http.StatusServiceUnavailable,
	}

	// Current limiting error
	ErrRateLimitExceeded = &AppError{
		Code:       CodeRateLimitExceeded,
		Message:    "Request rate limit exceeded",
		HTTPStatus: http.StatusTooManyRequests,
	}
	ErrConcurrencyLimitExceeded = &AppError{
		Code:       CodeRateLimitExceeded,
		Message:    "Concurrent request limit exceeded",
		HTTPStatus: http.StatusTooManyRequests,
	}

	// Internal error
	ErrInternal = &AppError{
		Code:       CodeInternalError,
		Message:    "Internal service error",
		HTTPStatus: http.StatusInternalServerError,
	}
	ErrStoreNotConfigured = &AppError{
		Code:       CodeInternalError,
		Message:    "storage is not configured",
		HTTPStatus: http.StatusInternalServerError,
	}
)

// New creates a new application error
func New(code, message string, httpStatus int) *AppError {
	return &AppError{
		Code:       code,
		Message:    message,
		HTTPStatus: httpStatus,
	}
}
