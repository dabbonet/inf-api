// Package handler provides property-based tests for AsyncCleaner.
package handler

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/quick"
	"time"
)

// **Validates: Requirements 3.1, 3.4**
//
// Property 5: Asynchronous cleaner life cycle
// For any AsyncCleaner instance, after Start it should periodically execute cleanup,
// and after Stop it should cease execution and exit the goroutine.

// TestAsyncCleanerLifecycle tests that AsyncCleaner starts, executes cleanup periodically,
// and stops gracefully.
func TestAsyncCleanerLifecycle(t *testing.T) {
	property := func(intervalMs uint8) bool {
		// Use small intervals for testing (10-100ms)
		if intervalMs < 10 {
			intervalMs = 10
		}
		if intervalMs > 100 {
			intervalMs = 100
		}
		interval := time.Duration(intervalMs) * time.Millisecond

		cleaner := NewAsyncCleaner(interval)
		var callCount int32

		// Start the cleaner with a function that increments a counter
		cleaner.Start(func() {
			atomic.AddInt32(&callCount, 1)
		})

		// Wait for at least 2 cleanup cycles
		time.Sleep(interval * 3)

		// Stop the cleaner
		cleaner.Stop()

		// Record the call count after stopping
		finalCount := atomic.LoadInt32(&callCount)

		// Verify cleanup was called at least once
		if finalCount < 1 {
			return false
		}

		// Wait a bit more to ensure no more calls happen after Stop
		time.Sleep(interval * 2)
		countAfterStop := atomic.LoadInt32(&callCount)

		// Call count should not increase after Stop
		if countAfterStop != finalCount {
			return false
		}

		return true
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 50}); err != nil {
		t.Errorf("AsyncCleaner lifecycle property failed: %v", err)
	}
}

// TestAsyncCleanerMultipleStartStop tests that AsyncCleaner handles multiple Start/Stop cycles.
func TestAsyncCleanerMultipleStartStop(t *testing.T) {
	property := func(cycles uint8) bool {
		// Test 1-5 cycles
		if cycles == 0 {
			cycles = 1
		}
		if cycles > 5 {
			cycles = 5
		}

		interval := 20 * time.Millisecond
		var totalCalls int32

		for i := uint8(0); i < cycles; i++ {
			cleaner := NewAsyncCleaner(interval)

			cleaner.Start(func() {
				atomic.AddInt32(&totalCalls, 1)
			})

			// Let it run for a bit
			time.Sleep(interval * 2)

			// Stop should complete without hanging
			cleaner.Stop()
		}

		// Should have executed cleanup at least once per cycle
		return atomic.LoadInt32(&totalCalls) >= int32(cycles)
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 20}); err != nil {
		t.Errorf("AsyncCleaner multiple Start/Stop property failed: %v", err)
	}
}

// TestAsyncCleanerStopIdempotency tests that calling Stop multiple times is safe.
func TestAsyncCleanerStopIdempotency(t *testing.T) {
	property := func(stopCount uint8) bool {
		// Test 1-10 Stop calls
		if stopCount == 0 {
			stopCount = 1
		}
		if stopCount > 10 {
			stopCount = 10
		}

		cleaner := NewAsyncCleaner(20 * time.Millisecond)
		var callCount int32

		cleaner.Start(func() {
			atomic.AddInt32(&callCount, 1)
		})

		time.Sleep(50 * time.Millisecond)

		// Call Stop multiple times concurrently
		var wg sync.WaitGroup
		for i := uint8(0); i < stopCount; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cleaner.Stop()
			}()
		}

		// Should not hang or panic
		wg.Wait()

		// Verify cleanup was called at least once
		return atomic.LoadInt32(&callCount) >= 1
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 20}); err != nil {
		t.Errorf("AsyncCleaner Stop idempotency property failed: %v", err)
	}
}

// TestAsyncCleanerPanicRecovery tests that AsyncCleaner recovers from panics in cleanup function.
func TestAsyncCleanerPanicRecovery(t *testing.T) {
	property := func(panicAfter uint8) bool {
		// Panic after 1-5 successful calls
		if panicAfter == 0 {
			panicAfter = 1
		}
		if panicAfter > 5 {
			panicAfter = 5
		}

		cleaner := NewAsyncCleaner(20 * time.Millisecond)
		var callCount int32

		cleaner.Start(func() {
			count := atomic.AddInt32(&callCount, 1)
			// Panic on the specified call
			if count == int32(panicAfter) {
				panic("test panic")
			}
		})

		// Wait for panic to occur and recovery
		time.Sleep(time.Duration(panicAfter+2) * 20 * time.Millisecond)

		// Stop should still work after panic
		cleaner.Stop()

		// Should have been called at least panicAfter times
		return atomic.LoadInt32(&callCount) >= int32(panicAfter)
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 20}); err != nil {
		t.Errorf("AsyncCleaner panic recovery property failed: %v", err)
	}
}

// TestAsyncCleanerConcurrentStop tests concurrent Stop operations.
func TestAsyncCleanerConcurrentStop(t *testing.T) {
	property := func(stoppers uint8) bool {
		// Test 2-10 concurrent Stop calls
		if stoppers < 2 {
			stoppers = 2
		}
		if stoppers > 10 {
			stoppers = 10
		}

		cleaner := NewAsyncCleaner(10 * time.Millisecond)
		var callCount int32

		// Start once
		cleaner.Start(func() {
			atomic.AddInt32(&callCount, 1)
		})

		// Wait for at least one execution
		time.Sleep(30 * time.Millisecond)

		// Call Stop concurrently from multiple goroutines
		var wg sync.WaitGroup
		for i := uint8(0); i < stoppers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cleaner.Stop()
			}()
		}

		// Should not panic or deadlock
		wg.Wait()

		// Verify cleanup was called at least once
		return atomic.LoadInt32(&callCount) >= 1
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 20}); err != nil {
		t.Errorf("AsyncCleaner concurrent Stop property failed: %v", err)
	}
}

// TestAsyncCleanerPeriodicExecution tests that cleanup executes at regular intervals.
func TestAsyncCleanerPeriodicExecution(t *testing.T) {
	interval := 50 * time.Millisecond
	cleaner := NewAsyncCleaner(interval)

	var callTimes []time.Time
	var mu sync.Mutex

	cleaner.Start(func() {
		mu.Lock()
		callTimes = append(callTimes, time.Now())
		mu.Unlock()
	})

	// Let it run for several intervals
	time.Sleep(interval * 5)
	cleaner.Stop()

	mu.Lock()
	defer mu.Unlock()

	// Should have been called at least 3 times
	if len(callTimes) < 3 {
		t.Errorf("Expected at least 3 cleanup calls, got %d", len(callTimes))
		return
	}

	// Check that intervals are approximately correct (within 50% tolerance)
	for i := 1; i < len(callTimes); i++ {
		actualInterval := callTimes[i].Sub(callTimes[i-1])
		// Allow 50% tolerance for timing variations
		minInterval := interval / 2
		maxInterval := interval * 2

		if actualInterval < minInterval || actualInterval > maxInterval {
			t.Errorf("Interval %d: expected ~%v, got %v", i, interval, actualInterval)
		}
	}
}

// TestAsyncCleanerGoroutineExit tests that the goroutine exits after Stop.
func TestAsyncCleanerGoroutineExit(t *testing.T) {
	// Get initial goroutine count
	initialCount := countGoroutines()

	cleaner := NewAsyncCleaner(10 * time.Millisecond)
	var callCount int32

	cleaner.Start(func() {
		atomic.AddInt32(&callCount, 1)
	})

	// Wait for at least one execution
	time.Sleep(30 * time.Millisecond)

	// Stop the cleaner
	cleaner.Stop()

	// Give time for goroutine to exit
	time.Sleep(50 * time.Millisecond)

	// Goroutine count should return to initial level (or close to it)
	finalCount := countGoroutines()

	// Allow some tolerance for test framework goroutines
	if finalCount > initialCount+2 {
		t.Errorf("Goroutine leak detected: initial=%d, final=%d", initialCount, finalCount)
	}

	// Verify cleanup was called
	if atomic.LoadInt32(&callCount) < 1 {
		t.Errorf("Cleanup function was not called")
	}
}

// TestAsyncCleanerZeroInterval tests behavior with very short intervals.
func TestAsyncCleanerZeroInterval(t *testing.T) {
	// Use 1ms interval (effectively as fast as possible)
	cleaner := NewAsyncCleaner(1 * time.Millisecond)
	var callCount int32

	cleaner.Start(func() {
		atomic.AddInt32(&callCount, 1)
	})

	// Let it run briefly
	time.Sleep(50 * time.Millisecond)
	cleaner.Stop()

	// Should have been called many times
	finalCount := atomic.LoadInt32(&callCount)
	if finalCount < 10 {
		t.Errorf("Expected at least 10 calls with 1ms interval, got %d", finalCount)
	}
}

// TestAsyncCleanerLongRunningCleanup tests that long-running cleanup doesn't block Stop excessively.
func TestAsyncCleanerLongRunningCleanup(t *testing.T) {
	cleaner := NewAsyncCleaner(10 * time.Millisecond)
	var callCount int32

	cleaner.Start(func() {
		atomic.AddInt32(&callCount, 1)
		// Simulate long-running cleanup
		time.Sleep(100 * time.Millisecond)
	})

	// Let it start one cleanup
	time.Sleep(20 * time.Millisecond)

	// Stop should wait for current cleanup to complete
	stopStart := time.Now()
	cleaner.Stop()
	stopDuration := time.Since(stopStart)

	// Stop should complete within a reasonable time
	// It needs to wait for the current cleanup (100ms) plus some overhead
	// We allow up to 150ms to account for timing variability
	if stopDuration > 150*time.Millisecond {
		t.Errorf("Stop took too long: %v", stopDuration)
	}

	// Should have been called at least once
	if atomic.LoadInt32(&callCount) < 1 {
		t.Errorf("Cleanup function was not called")
	}
}

// countGoroutines returns the current number of goroutines.
func countGoroutines() int {
	return runtime.NumGoroutine()
}

// BenchmarkAsyncCleanerStartStop benchmarks Start/Stop cycle performance.
func BenchmarkAsyncCleanerStartStop(b *testing.B) {
	for i := 0; i < b.N; i++ {
		cleaner := NewAsyncCleaner(10 * time.Millisecond)
		cleaner.Start(func() {})
		cleaner.Stop()
	}
}

// BenchmarkAsyncCleanerExecution benchmarks cleanup execution overhead.
func BenchmarkAsyncCleanerExecution(b *testing.B) {
	cleaner := NewAsyncCleaner(1 * time.Millisecond)
	var callCount int32

	cleaner.Start(func() {
		atomic.AddInt32(&callCount, 1)
	})

	b.ResetTimer()
	time.Sleep(time.Duration(b.N) * time.Millisecond)
	b.StopTimer()

	cleaner.Stop()
}
