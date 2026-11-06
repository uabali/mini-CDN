package main

import (
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mini-cdn/internal/hashring"
	"mini-cdn/internal/metrics"
)

const (
	StrategyRoundRobin  = "roundrobin"
	StrategyConsistent  = "consistent"
)

type EdgeTarget struct {
	Name    string
	URL     *url.URL
	Healthy bool
}

type RouterConfig struct {
	Strategy   string
	VNodes     int
	MaxInFlight int
}

type Router struct {
	mu          sync.Mutex
	edges       []*EdgeTarget
	byName      map[string]*EdgeTarget
	next        int
	strategy    string
	ring        *hashring.Ring
	client      *http.Client
	metrics     *metrics.Registry
	maxInFlight int
	inflight    atomic.Int64
}

func NewRouter(rawEdges []string, registry *metrics.Registry, cfg RouterConfig) (*Router, error) {
	strategy := strings.ToLower(strings.TrimSpace(cfg.Strategy))
	if strategy == "" {
		strategy = StrategyConsistent
	}
	if strategy != StrategyRoundRobin && strategy != StrategyConsistent {
		return nil, errors.New("strategy must be consistent or roundrobin")
	}

	edges := make([]*EdgeTarget, 0, len(rawEdges))
	byName := make(map[string]*EdgeTarget, len(rawEdges))
	for _, raw := range rawEdges {
		parsed, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		edge := &EdgeTarget{
			Name:    parsed.Hostname(),
			URL:     parsed,
			Healthy: true,
		}
		edges = append(edges, edge)
		byName[edge.Name] = edge
	}
	if len(edges) == 0 {
		return nil, errors.New("at least one edge is required")
	}

	r := &Router{
		edges:       edges,
		byName:      byName,
		strategy:    strategy,
		ring:        hashring.New(cfg.VNodes),
		client:      &http.Client{Timeout: 3 * time.Second},
		metrics:     registry,
		maxInFlight: cfg.MaxInFlight,
	}
	r.syncRingLocked()
	return r, nil
}

func (r *Router) Strategy() string {
	return r.strategy
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

func (r *Router) SelectEdge(key string) (*EdgeTarget, error) {
	if r.strategy == StrategyRoundRobin {
		return r.NextEdge()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	name := r.ring.Get(key)
	if name == "" {
		return nil, errors.New("no healthy edges available")
	}
	edge := r.byName[name]
	if edge == nil || !edge.Healthy {
		return nil, errors.New("no healthy edges available")
	}
	return edge, nil
}

func (r *Router) MarkHealth(edge *EdgeTarget, healthy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if edge.Healthy == healthy {
		r.publishHealthLocked(edge)
		return
	}
	edge.Healthy = healthy
	r.syncRingLocked()
	r.publishHealthLocked(edge)
}

func (r *Router) publishHealthLocked(edge *EdgeTarget) {
	value := 0.0
	if edge.Healthy {
		value = 1
	}
	r.metrics.SetGauge("cdn_edge_healthy", map[string]string{"edge": edge.Name}, value)
}

func (r *Router) syncRingLocked() {
	names := make([]string, 0, len(r.edges))
	for _, edge := range r.edges {
		inRing := 0.0
		if edge.Healthy {
			names = append(names, edge.Name)
			inRing = 1
		}
		r.metrics.SetGauge("cdn_hash_ring_member", map[string]string{"edge": edge.Name}, inRing)
		r.publishHealthLocked(edge)
	}
	r.ring.SetNodes(names)
	r.metrics.SetGauge("cdn_hash_ring_members", map[string]string{"strategy": r.strategy}, float64(len(names)))
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

func (r *Router) tryAcquire() bool {
	if r.maxInFlight <= 0 {
		return true
	}
	current := r.inflight.Add(1)
	if current > int64(r.maxInFlight) {
		r.inflight.Add(-1)
		return false
	}
	return true
}

func (r *Router) release() {
	if r.maxInFlight <= 0 {
		return
	}
	r.inflight.Add(-1)
}

func (r *Router) shed(w http.ResponseWriter) {
	r.metrics.Inc("cdn_load_shed_total", map[string]string{"component": "router"})
	w.Header().Set("Retry-After", "1")
	w.Header().Set("X-Load-Shed", "1")
	http.Error(w, "overloaded", http.StatusServiceUnavailable)
}

func (r *Router) Proxy(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	r.metrics.Inc("cdn_router_requests_total", map[string]string{"method": req.Method, "strategy": r.strategy})
	defer func() {
		r.metrics.ObserveDuration("cdn_router_response_duration_seconds", map[string]string{"strategy": r.strategy}, time.Since(start))
	}()

	if !r.tryAcquire() {
		r.shed(w)
		return
	}
	defer r.release()

	edge, err := r.SelectEdge(req.URL.Path)
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

	w.Header().Set("X-Router-Strategy", r.strategy)
	w.Header().Set("X-Edge-Selected", edge.Name)
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
