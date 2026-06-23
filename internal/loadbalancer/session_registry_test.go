package loadbalancer

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSessionRegistry_RecordAndLookup(t *testing.T) {
	r := NewSessionRegistry(SessionRegistryConfig{EntryTTL: time.Minute, MaxEntries: 5})
	ctx := context.Background()

	r.Record(ctx, 1, "claude-3-5")
	got, ok := r.Lookup(ctx, 1, "claude-3-5")
	if !ok || got != "claude-3-5" {
		t.Fatalf("expected lookup to return stored model, got %q ok=%v", got, ok)
	}
	// Different (accountID, model) -> miss.
	if _, ok := r.Lookup(ctx, 2, "claude-3-5"); ok {
		t.Fatal("expected miss for different accountID")
	}
	if _, ok := r.Lookup(ctx, 1, "gpt-4"); ok {
		t.Fatal("expected miss for different model")
	}
}

func TestSessionRegistry_TTLExpiry(t *testing.T) {
	r := NewSessionRegistry(SessionRegistryConfig{EntryTTL: 50 * time.Millisecond})
	r.now = func() time.Time {
		return time.Unix(0, 0)
	}
	r.Record(context.Background(), 1, "m1")
	r.now = func() time.Time { return time.Unix(0, int64(200*time.Millisecond)) }
	if _, ok := r.Lookup(context.Background(), 1, "m1"); ok {
		t.Fatal("entry should be expired")
	}
}

func TestSessionRegistry_Eviction(t *testing.T) {
	r := NewSessionRegistry(SessionRegistryConfig{MaxEntries: 2})
	r.Record(context.Background(), 1, "m1")
	r.Record(context.Background(), 2, "m2")
	// First record is oldest; adding m3 should evict m1.
	r.now = func() time.Time { return time.Unix(1, 0) }
	r.Record(context.Background(), 3, "m3")
	if _, ok := r.Lookup(context.Background(), 1, "m1"); ok {
		t.Fatal("m1 should have been evicted")
	}
}

func TestSessionRegistry_Forget(t *testing.T) {
	r := NewSessionRegistry(SessionRegistryConfig{})
	r.Record(context.Background(), 1, "m1")
	r.Forget(context.Background(), 1, "m1")
	if _, ok := r.Lookup(context.Background(), 1, "m1"); ok {
		t.Fatal("entry should be gone after Forget")
	}
}

func TestSessionRegistry_ModelConflictMarker(t *testing.T) {
	r := NewSessionRegistry(SessionRegistryConfig{})
	r.MarkModelConflict(context.Background(), 1, "m1")
	if !r.IsReportedModelConflict(context.Background(), 1, "m1") {
		t.Fatal("expected conflict marker to be reported")
	}
	// Unrelated (account, model) must not have the marker.
	if r.IsReportedModelConflict(context.Background(), 1, "m2") {
		t.Fatal("expected unrelated model to be free of conflict marker")
	}
}

func TestModelConflictError_As(t *testing.T) {
	err := &ModelConflictError{
		AccountID:        7,
		AccountName:      "acc1",
		RequestedModel:   "m1",
		UpstreamModel:    "m2",
	}
	target, ok := AsModelConflict(err)
	if !ok {
		t.Fatal("expected errors.As to match")
	}
	if target.RequestedModel != "m1" || target.UpstreamModel != "m2" {
		t.Fatalf("unexpected fields: %+v", target)
	}
	// errors.As with errors.New must not match.
	if _, ok := AsModelConflict(errors.New("plain")); ok {
		t.Fatal("plain error must not match ModelConflictError")
	}
}
