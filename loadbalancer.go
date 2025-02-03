package main

import (
	"log"
	"net/http"
	"sync"
	"time"
)

// BalanverAlgorithm defines the behavior required by a load balancer algorithm.
type BalancerAlgorithm interface {
	NextBackend(backends []*Backend) *Backend
	Name() string
}

// LoadBalancer represents the core load balancing logic.
type LoadBalancer struct {
	backends  []*Backend
	index     int
	mu        sync.Mutex
	algorithm BalancerAlgorithm
}

// SetAlgorithm changes the load balancer algorithm.
func (lb *LoadBalancer) SetAlgorithm(algo BalancerAlgorithm) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.algorithm = algo
	log.Printf("changed the algorithm to %s\n", algo.Name())
}

// NextBackend for now uses "round-robin/least-connection" to cycle through backends and return the healthy ones.
func (lb *LoadBalancer) NextBackend() *Backend {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	if lb.algorithm == nil {
		//default is round robin
		lb.algorithm = &RoundRobin{}
	}

	return lb.algorithm.NextBackend(lb.backends)
}

// ServeHTTP implements http.Handler interface.
func (lb *LoadBalancer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/change-algorithm" && r.Method == http.MethodPost {
		algorithm := r.URL.Query().Get("algorithm")
		switch algorithm {
		case "round-robin":
			lb.SetAlgorithm(&RoundRobin{})
		case "least-connection":
			lb.SetAlgorithm(&LeastConnection{})
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
	//not-found
	w.WriteHeader(http.StatusNotFound)
}

// StartHealthCheck checks the health of each backend periodically and marks that backend as unhealthy if resp fails.
func (lb *LoadBalancer) StartHealthCheck(checkInterval time.Duration) {
	for _, backend := range lb.backends {
		go func() {
			//create a client to hit that backend
			client := http.Client{Timeout: time.Second * 5}
			for {
				time.Sleep(checkInterval)
				resp, err := client.Get(backend.URL + "/health")
				if err != nil || resp.StatusCode != http.StatusOK {
					backend.MarkUnHealthy()
				} else {
					backend.MarkHealthy()
				}
			}
		}()
	}
}
