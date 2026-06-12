package debug

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/goccy/go-json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger debug logger
type Logger struct {
	enabled    bool
	sseEnabled bool
	dir        string
	rawFile    *os.File
	outFile    *os.File
	mu         sync.Mutex
	startTime  time.Time
}

// New creates a new debug logger
func New(enabled bool, sseEnabled bool) *Logger {
	if !enabled {
		return &Logger{enabled: false}
	}

	now := time.Now()
	timestamp := now.Format("2006-01-02_15-04-05.000")
	suffix := "0000"
	var randBytes [2]byte
	if _, err := rand.Read(randBytes[:]); err == nil {
		suffix = hex.EncodeToString(randBytes[:])
	}
	dir := filepath.Join("debug-logs", fmt.Sprintf("%s_%s", timestamp, suffix))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &Logger{enabled: false}
	}

	return &Logger{
		enabled:    true,
		sseEnabled: sseEnabled,
		dir:        dir,
		startTime:  time.Now(),
	}
}

// CleanupAllLogs clears all debug logs (called at startup)
func CleanupAllLogs() error {
	if err := os.RemoveAll("debug-logs"); err != nil {
		return err
	}
	return os.MkdirAll("debug-logs", 0755)
}

// Dir returns log directory
func (l *Logger) Dir() string {
	if !l.enabled {
		return ""
	}
	return l.dir
}

// LogIncomingRequest records 1. Incoming Claude API request
func (l *Logger) LogIncomingRequest(req interface{}) {
	if !l.enabled {
		return
	}
	l.writeJSON("1_claude_request.json", req)
}

// LogEarlyExit records the reason for early return
func (l *Logger) LogEarlyExit(reason string, details map[string]interface{}) {
	if !l.enabled {
		return
	}
	payload := map[string]interface{}{
		"reason":     reason,
		"elapsed_ms": time.Since(l.startTime).Milliseconds(),
	}
	if details != nil {
		payload["details"] = details
	}
	l.writeJSON("1_early_exit.json", payload)
}

// LogConvertedPrompt record 2. Converted prompt
func (l *Logger) LogConvertedPrompt(prompt string) {
	if !l.enabled {
		return
	}
	l.writeFile("2_converted_prompt.md", prompt)
}

// LogUpstreamRequest record 3. Request sent to upstream
func (l *Logger) LogUpstreamRequest(url string, headers map[string]string, body interface{}) {
	if !l.enabled {
		return
	}

	data := map[string]interface{}{
		"url":     url,
		"headers": headers,
		"body":    body,
	}
	l.writeJSON("3_upstream_request.json", data)
}

// LogUpstreamHTTPError logs upstream HTTP errors (request failed or returned non-200)
func (l *Logger) LogUpstreamHTTPError(url string, status int, body string, err error) {
	if !l.enabled {
		return
	}
	payload := map[string]interface{}{
		"url":        url,
		"status":     status,
		"body":       body,
		"elapsed_ms": time.Since(l.startTime).Milliseconds(),
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	l.writeJSON("3_upstream_http_error.json", payload)
}

// LogUpstreamSSE Logging 4. Raw SSE returned by upstream (append write)
func (l *Logger) LogUpstreamSSE(eventType string, data string) {
	if !l.enabled || !l.sseEnabled {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.rawFile == nil {
		f, err := os.OpenFile(filepath.Join(l.dir, "4_upstream_sse.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return
		}
		l.rawFile = f
	}

	elapsed := time.Since(l.startTime).Milliseconds()
	fmt.Fprintf(l.rawFile, "[%dms] %s: %s\n", elapsed, eventType, data)
}

// LogOutputSSE logging 5. SSE converted to client (append writing)
func (l *Logger) LogOutputSSE(event string, data string) {
	if !l.enabled || !l.sseEnabled {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.outFile == nil {
		f, err := os.OpenFile(filepath.Join(l.dir, "5_client_sse.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return
		}
		l.outFile = f
	}

	elapsed := time.Since(l.startTime).Milliseconds()
	fmt.Fprintf(l.outFile, "[%dms] event: %s\ndata: %s\n\n", elapsed, event, data)
}

// LogInputTokenBreakdown records input token decomposition
func (l *Logger) LogInputTokenBreakdown(profile string, basePromptTokens, systemContextTokens, historyTokens, toolsTokens, total int) {
	if !l.enabled {
		return
	}

	payload := map[string]interface{}{
		"prompt_profile":        profile,
		"base_prompt_tokens":    basePromptTokens,
		"system_context_tokens": systemContextTokens,
		"history_tokens":        historyTokens,
		"tools_tokens":          toolsTokens,
		"estimated_total":       total,
	}
	l.writeJSON("6_input_token_breakdown.json", payload)
}

// LogSummary Log request summary
func (l *Logger) LogSummary(inputTokens, outputTokens int, duration time.Duration, stopReason string) {
	if !l.enabled {
		return
	}

	summary := map[string]interface{}{
		"input_tokens":  inputTokens,
		"output_tokens": outputTokens,
		"total_tokens":  inputTokens + outputTokens,
		"duration_ms":   duration.Milliseconds(),
		"stop_reason":   stopReason,
	}
	l.writeJSON("6_summary.json", summary)
}

// Close Close the log file
func (l *Logger) Close() {
	if !l.enabled {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.rawFile != nil {
		l.rawFile.Close()
		l.rawFile = nil
	}
	if l.outFile != nil {
		l.outFile.Close()
		l.outFile = nil
	}
}

func (l *Logger) writeJSON(filename string, data interface{}) {
	if !l.enabled {
		return
	}
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(filepath.Join(l.dir, filename), jsonData, 0644)
}

func (l *Logger) writeFile(filename string, content string) {
	if !l.enabled {
		return
	}
	os.WriteFile(filepath.Join(l.dir, filename), []byte(content), 0644)
}
