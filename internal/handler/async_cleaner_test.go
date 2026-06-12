package handler

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestAsyncCleaner_StartStop(t *testing.T) {
	var counter int32
	cleanFn := func() {
		atomic.AddInt32(&counter, 1)
	}

	cleaner := NewAsyncCleaner(50 * time.Millisecond)
	cleaner.Start(cleanFn)

	// Wait for at least 2 cleanups to be performed
	time.Sleep(150 * time.Millisecond)

	cleaner.Stop()

	count := atomic.LoadInt32(&counter)
	if count < 2 {
		t.Errorf("Expected at least 2 cleanups, got %d", count)
	}

	// Verification will not be executed after it is stopped
	finalCount := count
	time.Sleep(100 * time.Millisecond)
	afterStopCount := atomic.LoadInt32(&counter)
	if afterStopCount != finalCount {
		t.Errorf("Cleanup continued after Stop: before=%d, after=%d", finalCount, afterStopCount)
	}
}

func TestAsyncCleaner_MultipleStops(t *testing.T) {
	cleaner := NewAsyncCleaner(100 * time.Millisecond)
	cleaner.Start(func() {})

	// first stop
	cleaner.Stop()

	// The second stop should not panic
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Second Stop() caused panic: %v", r)
		}
	}()
	cleaner.Stop()
}

func TestAsyncCleaner_CleanFnPanic(t *testing.T) {
	cleaner := NewAsyncCleaner(50 * time.Millisecond)

	var executed int32
	cleanFn := func() {
		count := atomic.AddInt32(&executed, 1)
		if count == 1 {
			panic("test panic")
		}
	}

	// Start the cleaner, which should continue execution even if cleanFn panics
	cleaner.Start(cleanFn)

	time.Sleep(150 * time.Millisecond)
	cleaner.Stop()

	// There is a panic recovery mechanism, executed should be > 1
	count := atomic.LoadInt32(&executed)
	if count < 2 {
		t.Errorf("Expected at least 2 executions (with panic recovery), got %d", count)
	}
}
