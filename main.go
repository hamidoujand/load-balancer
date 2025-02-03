package main

import (
	"context"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	backends := []*Backend{
		{URL: "http://localhost:8001", Healthy: true},
		{URL: "http://localhost:8002", Healthy: true},
	}

	lb := &LoadBalancer{backends: backends}
	lb.StartHealthCheck(time.Second * 10)

	mux := http.NewServeMux()
	mux.Handle("/", configureReverseProxy(lb))
	mux.Handle("POST /admin/change-algorithm", lb)

	log.Println("load balancer on: 8000")
	log.Fatal(http.ListenAndServe(":8000", mux))
}

// ==============================================================================
// Backend
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

//==============================================================================
// Load Balancer

type LoadBalancer struct {
	backends  []*Backend
	index     int
	mu        sync.Mutex
	algorithm BalancerAlgorithm
}

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

func (lb *LoadBalancer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/change-algorithm" && r.Method == http.MethodPost {
		algorithm := r.URL.Query().Get("algorithm")
		switch algorithm {
		case "round-robin":
			lb.algorithm = &RoundRobin{}
		case "least-connection":
			lb.algorithm = &LeastConnection{}
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

//==============================================================================
// Reverse Proxy

type ctxKey int

const (
	startTimeKey ctxKey = iota
	loadBalancerErrKey
	backendKey
)

func configureReverseProxy(lb *LoadBalancer) http.Handler {
	return &httputil.ReverseProxy{
		//this is where request maniuplation happesn before sending to backend.
		Director: func(r *http.Request) {
			startTime := time.Now()
			ctx := context.WithValue(r.Context(), startTimeKey, startTime)

			backend := lb.NextBackend()
			if backend == nil {
				ctx = context.WithValue(ctx, loadBalancerErrKey, "no healthy backend")
				//invalid URL to force the error
				r.URL = &url.URL{}
			} else {
				//now we add to its connections
				atomic.AddInt64(&backend.ActiveConnections, 1)
				ctx = context.WithValue(ctx, backendKey, backend)
				target, _ := url.Parse(backend.URL)
				r.URL.Scheme = target.Scheme
				r.URL.Host = target.Host
				r.URL.Path = target.Path + r.URL.Path
			}
			r = r.WithContext(ctx)
		},

		//this is where we handle failures in proxing request.
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			//when anything goes wrong from the backend side,this handler will be called.

			if errVal := r.Context().Value(loadBalancerErrKey); errVal != nil {
				w.WriteHeader(http.StatusBadGateway)
				w.Write([]byte("no healthy backend available"))
				return
			}

			//access the backend to inc the failure count
			if backendValue := r.Context().Value(backendKey); backendValue != nil {
				backend := backendValue.(*Backend)
				backend.IncrementFailure()
			}

			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("Bad Gateway"))
		},

		//this is where we can modify response from backends before sending to client.
		ModifyResponse: func(resp *http.Response) error {
			if backendValue := resp.Request.Context().Value(backendKey); backendValue != nil {
				backend := backendValue.(*Backend)
				//reduce one active connection from this backend
				atomic.AddInt64(&backend.ActiveConnections, -1)
				//log a simple message
				log.Printf("request to %s, succeeded\n", backend.URL)
			}
			return nil
		},
		//this is where we configure our transport.
		Transport: &http.Transport{
			//custom timeouts
			DialContext: (&net.Dialer{
				Timeout:   time.Second * 30,
				KeepAlive: time.Second * 30,
			}).DialContext,
			//custom pool settings
			MaxIdleConns:          100,
			IdleConnTimeout:       time.Second * 90,
			TLSHandshakeTimeout:   time.Second * 10,
			ExpectContinueTimeout: time.Second * 1,
		},
	}
}

//==============================================================================
// Balancer Algorithm

type BalancerAlgorithm interface {
	NextBackend(backends []*Backend) *Backend
	Name() string
}

//==============================================================================
// Round Robin

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

//==============================================================================
// Least connection

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
