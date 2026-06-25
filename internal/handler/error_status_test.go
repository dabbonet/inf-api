package handler

import (
	"net/http"
	"testing"
)

func TestErrorCategoryToStatus(t *testing.T) {
	tests := []struct {
		name     string
		category string
		want     int
	}{
		{"client_bad_request", "client", http.StatusBadRequest},
		{"auth_unauthorized", "auth", http.StatusUnauthorized},
		{"auth_blocked_forbidden", "auth_blocked", http.StatusForbidden},
		{"rate_limit_429", "rate_limit", http.StatusTooManyRequests},
		{"session_conflict_400", "session_conflict", http.StatusBadRequest},
		{"model_unavailable_503", "model_unavailable", http.StatusServiceUnavailable},
		{"timeout_504", "timeout", http.StatusGatewayTimeout},
		{"server_502", "server", http.StatusBadGateway},
		{"network_502", "network", http.StatusBadGateway},
		{"canceled_499", "canceled", 499},
		{"unknown_502", "unknown", http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorCategoryToStatus(tt.category); got != tt.want {
				t.Errorf("errorCategoryToStatus(%q) = %d, want %d", tt.category, got, tt.want)
			}
		})
	}
}
