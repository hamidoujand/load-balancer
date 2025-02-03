package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

func main() {
	backends := []*Backend{
		{URL: "http://localhost:8001", Healthy: true},
		{URL: "http://localhost:8002", Healthy: true},
	}

	lb := &LoadBalancer{backends: backends}
	lb.StartHealthCheck(time.Second * 10)

	//server
	log.Println("load balancer on: 8000")
	log.Fatal(http.ListenAndServe(":8000", configureReverseProxy(lb)))
}

// ==============================================================================
// Backend
type Backend struct {
	URL          string
	Healthy      bool
	mu           sync.RWMutex
	failureCount int
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
	backends []*Backend
	index    int
	mu       sync.Mutex
}

// NextBackend for now uses "round-robin" to cycle through backends and return the healthy ones.
func (lb *LoadBalancer) NextBackend() *Backend {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	start := lb.index
	for {
		backend := lb.backends[lb.index]
		//calculate next index
		lb.index = (lb.index + 1) % len(lb.backends)

		//check if the backend is healthy
		if backend.IsHealthy() {
			return backend
		}

		//if the idx is back to where we start, means we do not have a healthy one
		if lb.index == start {
			return nil
		}
	}
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
