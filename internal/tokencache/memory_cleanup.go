package tokencache

import "time"

func (c *MemoryCache) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.mu.Lock()
			c.pruneExpiredLocked(time.Now())
			c.mu.Unlock()
		case <-c.done:
			return
		}
	}
}

// Close stops background cleanup goroutine
func (c *MemoryCache) Close() {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
}
