package main

import (
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"mini-cdn/internal/metrics"
)

type EdgeTarget struct {
	Name    string
	URL     *url.URL
	Healthy bool
}

type Router struct {
	mu      sync.Mutex
	edges   []*EdgeTarget
	next    int
	client  *http.Client
	metrics *metrics.Registry
}

func NewRouter(rawEdges []string, registry *metrics.Registry) (*Router, error) {
	edges := make([]*EdgeTarget, 0, len(rawEdges))
	for _, raw := range rawEdges {
		parsed, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		edges = append(edges, &EdgeTarget{
			Name:    parsed.Hostname(),
			URL:     parsed,
			Healthy: true,
		})
	}
	if len(edges) == 0 {
		return nil, errors.New("at least one edge is required")
	}

	return &Router{
		edges:   edges,
		client:  &http.Client{Timeout: 3 * time.Second},
		metrics: registry,
	}, nil
}

func (r *Router) NextEdge() (*EdgeTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := 0; i < len(r.edges); i++ {
		idx := (r.next + i) % len(r.edges)
		edge := r.edges[idx]
		if edge.Healthy {
			r.next = (idx + 1) % len(r.edges)
			return edge, nil
		}
	}
	return nil, errors.New("no healthy edges available")
}

func (r *Router) MarkHealth(edge *EdgeTarget, healthy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	edge.Healthy = healthy
	value := 0.0
	if healthy {
		value = 1
	}
	r.metrics.SetGauge("cdn_edge_healthy", map[string]string{"edge": edge.Name}, value)
}

func (r *Router) HealthCheckOnce() {
	for _, edge := range r.edges {
		req, err := http.NewRequest(http.MethodGet, edge.URL.String()+"/healthz", nil)
		if err != nil {
			r.MarkHealth(edge, false)
			continue
		}
		resp, err := r.client.Do(req)
		if err != nil {
			r.MarkHealth(edge, false)
			continue
		}
		_ = resp.Body.Close()
		r.MarkHealth(edge, resp.StatusCode == http.StatusOK)
	}
}

func (r *Router) StartHealthChecks(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		r.HealthCheckOnce()
		for range ticker.C {
			r.HealthCheckOnce()
		}
	}()
}

func (r *Router) Proxy(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	r.metrics.Inc("cdn_router_requests_total", map[string]string{"method": req.Method})
	defer func() {
		r.metrics.ObserveDuration("cdn_router_response_duration_seconds", nil, time.Since(start))
	}()

	edge, err := r.NextEdge()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(edge.URL)
	originalDirector := proxy.Director
	proxy.Director = func(out *http.Request) {
		originalDirector(out)
		out.Host = edge.URL.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		r.MarkHealth(edge, false)
		http.Error(w, "edge unavailable", http.StatusBadGateway)
	}

	w.Header().Set("X-Router-Strategy", "round-robin")
	proxy.ServeHTTP(w, req)
}

func (r *Router) FanOut(w http.ResponseWriter, req *http.Request) {
	for _, edge := range r.edges {
		target := edge.URL.String() + req.URL.Path
		out, err := http.NewRequestWithContext(req.Context(), req.Method, target, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp, err := r.client.Do(out)
		if err != nil {
			r.MarkHealth(edge, false)
			http.Error(w, "edge unavailable", http.StatusBadGateway)
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 400 {
			http.Error(w, "purge failed on "+edge.Name, resp.StatusCode)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
