package handler

import (
	"sync"
	"time"
)

// AsyncCleaner manages background cleaning tasks
type AsyncCleaner struct {
	interval time.Duration
	stopCh   chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// NewAsyncCleaner creates an asynchronous cleaner
func NewAsyncCleaner(interval time.Duration) *AsyncCleaner {
	return &AsyncCleaner{
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start starts background cleanup
func (c *AsyncCleaner) Start(cleanFn func()) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()

		for {
			select {
			case <-c.stopCh:
				return
			case <-ticker.C:
				// Double-check stop signal before running cleanup
				select {
				case <-c.stopCh:
					return
				default:
				}

				// Use recover to prevent cleanFn panic from causing goroutine to exit
				func() {
					defer func() {
						if r := recover(); r != nil {
							// Silently restore and continue subsequent cleanup
						}
					}()
					cleanFn()
				}()
			}
		}
	}()
}

// Stop Stop the cleaner
func (c *AsyncCleaner) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopCh)
	})
	c.wg.Wait()
}
