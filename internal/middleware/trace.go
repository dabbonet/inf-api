// Package middleware provides HTTP middleware
package middleware

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"orchids-api/internal/logutil"
)

// TraceIDHeader is the HTTP header name of the request trace ID
const TraceIDHeader = "X-Trace-ID"

// RequestIDHeader is the HTTP header name (alias) of the request ID
const RequestIDHeader = "X-Request-ID"

// traceIDKey is the key of storage trace ID in context
type traceIDKey struct{}

// GenerateTraceID generates a new trace ID
func GenerateTraceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// downgrade to timestamp
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405.000000")))
	}
	return hex.EncodeToString(b)
}

// TraceMiddleware adds request tracing function
// Get the trace ID from the request header and generate a new one if there is none
func TraceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try to get the trace ID from the request header
		traceID := r.Header.Get(TraceIDHeader)
		if traceID == "" {
			traceID = r.Header.Get(RequestIDHeader)
		}
		if traceID == "" {
			traceID = GenerateTraceID()
		}

		// Add trace ID to response header
		w.Header().Set(TraceIDHeader, traceID)

		// Add trace ID to context
		ctx := context.WithValue(r.Context(), traceIDKey{}, traceID)

		// Continue processing the request
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetTraceID Gets trace ID from context
func GetTraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if traceID, ok := ctx.Value(traceIDKey{}).(string); ok {
		return traceID
	}
	return ""
}

// TracedResponseWriter wraps ResponseWriter to record response status
type TracedResponseWriter struct {
	http.ResponseWriter
	StatusCode   int
	BytesWritten int64
}

// NewTracedResponseWriter creates a new TracedResponseWriter
func NewTracedResponseWriter(w http.ResponseWriter) *TracedResponseWriter {
	return &TracedResponseWriter{
		ResponseWriter: w,
		StatusCode:     http.StatusOK,
	}
}

// WriteHeader implements http.ResponseWriter
func (w *TracedResponseWriter) WriteHeader(code int) {
	w.StatusCode = code
	w.ResponseWriter.WriteHeader(code)
}

// Write implements http.ResponseWriter
func (w *TracedResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.BytesWritten += int64(n)
	return n, err
}

// Flush implements http.Flusher
func (w *TracedResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker to ensure that scenarios such as WebSocket upgrade are available.
func (w *TracedResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hj.Hijack()
}

// LoggingMiddleware records request logs, including trace ID and time consumption
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		traceID := GetTraceID(r.Context())

		// Wrapper ResponseWriter
		wrapped := NewTracedResponseWriter(w)

		if logutil.VerboseDiagnosticsEnabled() {
			slog.Debug("Request started",
				"trace_id", traceID,
				"method", r.Method,
				"path", r.URL.Path,
				"remote_addr", r.RemoteAddr,
			)
		}

		// Handle request
		next.ServeHTTP(wrapped, r)

		// Logging request completed
		duration := time.Since(start)
		level := slog.LevelDebug
		if wrapped.StatusCode >= 500 {
			level = slog.LevelError
		} else if wrapped.StatusCode >= 400 {
			level = slog.LevelWarn
		} else if !logutil.VerboseDiagnosticsEnabled() {
			return
		}

		slog.Log(r.Context(), level, "Request completed",
			"trace_id", traceID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", wrapped.StatusCode,
			"bytes", wrapped.BytesWritten,
			"duration", duration,
		)
	})
}

// Chain chain combination of multiple middleware
func Chain(middlewares ...func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(final http.Handler) http.Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			final = middlewares[i](final)
		}
		return final
	}
}
