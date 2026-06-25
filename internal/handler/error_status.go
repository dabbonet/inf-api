package handler

import "net/http"

// errorCategoryToStatus maps a codebuff upstream error category to a proper
// HTTP status code for non-stream passthrough responses.
//
// Streaming mode always uses 200 + SSE error event (OpenAI streaming convention);
// non-streaming surfaces these proper codes so clients don't have to fish
// arbitrary 200 responses for embedded error JSON.
func errorCategoryToStatus(category string) int {
	switch category {
	case "client", "session_conflict":
		// Bad request, model_locked, waiting_room_required, max_token_limit
		return http.StatusBadRequest
	case "auth":
		return http.StatusUnauthorized
	case "auth_blocked":
		return http.StatusForbidden
	case "rate_limit":
		return http.StatusTooManyRequests
	case "model_unavailable":
		return http.StatusServiceUnavailable
	case "timeout":
		return http.StatusGatewayTimeout
	case "server", "network":
		return http.StatusBadGateway
	case "canceled":
		return 499 // client closed connection (nginx convention)
	default:
		return http.StatusBadGateway
	}
}
