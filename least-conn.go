package main

import (
	"math"
	"sync/atomic"
)

// LeastConnection is the least connection implementation of the BalancerAlgorithm.
type LeastConnection struct{}

func (lc *LeastConnection) NextBackend(backends []*Backend) *Backend {
	var best *Backend
	min := int64(math.MaxInt64)

	for _, backend := range backends {
		if !backend.IsHealthy() {
			continue
		}

		if conns := atomic.LoadInt64(&backend.ActiveConnections); conns < min {
			min = conns
			best = backend
		}
	}
	return best
}

func (lc *LeastConnection) Name() string {
	return "least-connection"
}
