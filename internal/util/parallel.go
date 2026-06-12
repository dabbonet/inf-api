// Package util provides common utility functions
package util

import (
	"context"
	"runtime"
	"sync"
	"time"
)

// ParallelFor executes n tasks in parallel, each task receives index [0, n)
// Automatically adjust the concurrency based on the number of CPU cores, and execute small batch tasks serially to avoid goroutine overhead
func ParallelFor(n int, fn func(int)) {
	if n <= 0 {
		return
	}

	// Concurrency threshold: Serial processing is more efficient when there are fewer than this number
	const parallelThreshold = 8

	if n < parallelThreshold {
		// Serial processing of small batches
		for i := 0; i < n; i++ {
			fn(i)
		}
		return
	}

	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}

	var wg sync.WaitGroup
	jobs := make(chan int, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				func() {
					defer func() {
						if r := recover(); r != nil {
							// Prevent crash from panic in worker
						}
					}()
					fn(idx)
				}()
			}
		}()
	}

	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}

// SleepWithContext can cancel sleep, return false to indicate it was cancelled.
func SleepWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
