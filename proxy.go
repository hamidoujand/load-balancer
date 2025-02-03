package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"
)

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
