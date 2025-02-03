package main

import (
	"log"
	"net/http"
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
