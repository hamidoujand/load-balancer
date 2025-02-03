package main

import "sync"

// RoundRobin is the round robin implementation of the BalancerAlgorithm.
type RoundRobin struct {
	mu    sync.Mutex
	index int
}

func (rr *RoundRobin) NextBackend(backends []*Backend) *Backend {
	rr.mu.Lock()
	defer rr.mu.Unlock()

	start := rr.index
	for {
		backend := backends[rr.index]
		rr.index = (rr.index + 1) % (len(backends))
		if backend.IsHealthy() {
			return backend
		}

		if start == rr.index {
			return nil
		}
	}
}

func (rr *RoundRobin) Name() string {
	return "round-robin"
}
