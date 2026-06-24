package codebuff

import (
	"io"
	"strings"
	"testing"
)

type errReader struct{ err error }

func (r *errReader) Read([]byte) (int, error) { return 0, r.err }

func TestScannerErrorPropagation(t *testing.T) {
	parser := NewStreamParser(io.NopCloser(&errReader{err: io.ErrUnexpectedEOF}))
	defer parser.Close()

	_, err := parser.Next()
	if err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("expected unexpected EOF error, got: %v", err)
	}
}

func TestModelLockedDetection(t *testing.T) {
	tests := []struct {
		name     string
		errMsg   string
		isLocked bool
	}{
		{"model_locked in body", `Codebuff chat failed: status=409 body={"error":"model_locked","retryAfterMs":2000}`, true},
		{"model_locked in network error", `Codebuff request failed: status=409 body={"error":"model_locked"}`, true},
		{"waiting_room_required", `Codebuff request failed: status=409 body={"error":"waiting_room_required"}`, false},
		{"plain error", "connection reset by peer", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &Error{Message: tt.errMsg, StatusCode: 409}
			if got := IsModelLocked(err); got != tt.isLocked {
				t.Errorf("IsModelLocked(%q) = %v, want %v", tt.errMsg, got, tt.isLocked)
			}
		})
	}
}

func TestRetryAfterParsing(t *testing.T) {
	err := &Error{
		Message:    `Codebuff request failed: status=409 body={"error":"model_locked","retryAfterMs":2500}`,
		StatusCode: 409,
	}
	if ms := ParseRetryAfter(err); ms != 2500 {
		t.Errorf("expected 2500ms retryAfter, got %d", ms)
	}

	errNoRetry := &Error{
		Message:    "Codebuff chat failed: status=429 body=rate limit",
		StatusCode: 429,
	}
	if got := ParseRetryAfter(errNoRetry); got != 0 {
		t.Errorf("expected 0 retryAfter, got %d", got)
	}
}

func TestIsWaitingRoomRequired(t *testing.T) {
	err := &Error{
		Message:    `Codebuff request failed: status=409 body={"error":"waiting_room_required"}`,
		StatusCode: 409,
	}
	if !IsWaitingRoomRequired(err) {
		t.Error("expected waiting_room_required to be detected")
	}
}

func TestStreamParserEOF(t *testing.T) {
	parser := NewStreamParser(io.NopCloser(strings.NewReader("")))
	defer parser.Close()
	_, err := parser.Next()
	if err != io.EOF {
		t.Fatalf("expected EOF from empty body, got: %v", err)
	}
}
