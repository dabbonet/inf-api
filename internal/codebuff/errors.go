package codebuff

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Error represents an upstream codebuff failure.
type Error struct {
	Message    string
	StatusCode int
}

func (e *Error) Error() string {
	return e.Message
}

// IsWaitingRoomRequired returns true when the error indicates the session was
// superseded and a new one must be created.
func IsWaitingRoomRequired(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "waiting_room_required")
}

// IsModelLocked returns true when the error indicates the current session is
// locked to a different model and must be deleted before creating a new one.
func IsModelLocked(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "model_locked")
}

// ParseRetryAfter extracts the retry-after duration from an upstream error
// that embeds a JSON blob with "retryAfterMs".
func ParseRetryAfter(err error) int {
	if err == nil {
		return 0
	}
	s := err.Error()
	// Look for a JSON object fragment containing retryAfterMs.
	re := regexp.MustCompile(`\{[^}]*"retryAfterMs"\s*:\s*(\d+(?:\.\d+)?)[^}]*\}`)
	m := re.FindStringSubmatch(s)
	if len(m) > 1 {
		if ms, parseErr := strconv.ParseFloat(m[1], 64); parseErr == nil {
			return int(ms)
		}
	}
	return 0
}

// NewError creates a codebuff error from an HTTP response.
func NewError(resp *http.Response, body []byte, prefix string) *Error {
	raw := string(body)
	if raw == "" && resp != nil {
		// Try to read from response body if body slice is empty.
		raw = ""
	}
	text := raw
	if len(text) > 500 {
		text = text[:500]
	}
	status := http.StatusBadGateway
	if resp != nil {
		status = resp.StatusCode
	}

	// Handle 409 session_model_mismatch with a user-friendly message.
	if status == http.StatusConflict && strings.Contains(raw, `"error":"session_model_mismatch"`) {
		return &Error{
			Message:    "Codebuff 409 session_model_mismatch: 当前 IP/区域受限；请换用 US 服务器或 US 出口 IP 后重试。",
			StatusCode: status,
		}
	}

	if prefix == "" {
		prefix = "Codebuff request failed"
	}
	return &Error{
		Message:    fmt.Sprintf("%s: status=%d body=%s", prefix, status, text),
		StatusCode: status,
	}
}

// NewNetworkError creates a codebuff error for transport failures.
func NewNetworkError(method, url string, err error) *Error {
	return &Error{
		Message:    fmt.Sprintf("Codebuff request failed: %s %s network error (%T): %v", method, url, err, err),
		StatusCode: http.StatusBadGateway,
	}
}
