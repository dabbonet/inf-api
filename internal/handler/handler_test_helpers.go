package handler

import (
	"context"
	"sync"
	"time"

	"orchids-api/internal/audit"
	"orchids-api/internal/config"
	"orchids-api/internal/debug"
	"orchids-api/internal/provider"
	cbprov "orchids-api/internal/provider/codebuff"
	kimchiprov "orchids-api/internal/provider/kimchi"
	puterprov "orchids-api/internal/provider/puter"
	"orchids-api/internal/upstream"
)

// registerDefaultSpecs registers the canonical puter + codebuff specs on h.
// Called automatically by newTestHandler so that tests with empty LB still
// resolve correct per-provider Mode (UseRawModel, KeepToolsOnFollowup, …).
func (h *Handler) registerDefaultSpecs() {
	h.RegisterSpec(puterprov.Spec())
	h.RegisterSpec(cbprov.Spec())
	h.RegisterSpec(kimchiprov.Spec())
}

type fakePayloadClient struct {
	mu                  sync.Mutex
	calls               []upstream.UpstreamRequest
	conversationIDsByOp []string
	eventsByOp          [][]upstream.SSEMessage
}

func (f *fakePayloadClient) SendRequestWithPayload(ctx context.Context, req upstream.UpstreamRequest, onMessage func(upstream.SSEMessage), logger *debug.Logger) error {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	idx := len(f.calls) - 1
	var events []upstream.SSEMessage
	if idx >= 0 && idx < len(f.eventsByOp) {
		events = f.eventsByOp[idx]
	}
	f.mu.Unlock()

	if len(events) > 0 {
		for _, event := range events {
			onMessage(event)
		}
		return nil
	}

	onMessage(upstream.SSEMessage{
		Type:  "model.finish",
		Event: map[string]interface{}{"finishReason": "end_turn"},
	})
	return nil
}

func (f *fakePayloadClient) snapshotCalls() []upstream.UpstreamRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]upstream.UpstreamRequest, len(f.calls))
	copy(out, f.calls)
	return out
}

func newTestHandler(client upstream.UpstreamClient) *Handler {
	h := &Handler{
		config:       &config.Config{DebugEnabled: false},
		client:       client,
		specs:        provider.NewRegistry(),
		sessionStore: NewMemorySessionStore(30*time.Minute, 1024),
		dedupStore:   NewMemoryDedupStore(duplicateWindow, duplicateCleanupWindow),
		auditLogger:  audit.NewNopLogger(),
	}
	h.registerDefaultSpecs()
	return h
}

type spyConnTracker struct {
	mu             sync.Mutex
	counts         map[int64]int64
	acquireCalls   int
	releaseCalls   int
	getCountsCalls int
}

func newSpyConnTracker(counts map[int64]int64) *spyConnTracker {
	cloned := make(map[int64]int64, len(counts))
	for id, count := range counts {
		cloned[id] = count
	}
	return &spyConnTracker{counts: cloned}
}

func (t *spyConnTracker) Acquire(accountID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.acquireCalls++
	t.counts[accountID]++
}

func (t *spyConnTracker) Release(accountID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.releaseCalls++
	if current := t.counts[accountID]; current > 0 {
		t.counts[accountID] = current - 1
	}
}

func (t *spyConnTracker) GetCount(accountID int64) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counts[accountID]
}

func (t *spyConnTracker) GetCounts(accountIDs []int64) map[int64]int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.getCountsCalls++
	counts := make(map[int64]int64, len(accountIDs))
	for _, id := range accountIDs {
		counts[id] = t.counts[id]
	}
	return counts
}
