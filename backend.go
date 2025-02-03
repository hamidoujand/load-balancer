package main

import "sync"

// Backend represent each backend that load balancer route traffic into.
type Backend struct {
	URL               string
	Healthy           bool
	mu                sync.RWMutex
	failureCount      int
	ActiveConnections int64
}

func (b *Backend) IsHealthy() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.Healthy
}

func (b *Backend) MarkHealthy() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Healthy = true
	b.failureCount = 0
}

func (b *Backend) MarkUnHealthy() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Healthy = false
}

func (b *Backend) IncrementFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failureCount++
	if b.failureCount >= 5 {
		b.Healthy = false
	}
}
