package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mini-cdn/internal/metrics"
)

func defaultCfg() RouterConfig {
	return RouterConfig{Strategy: StrategyRoundRobin, VNodes: 50, MaxInFlight: 0}
}

func TestNextEdgeRoundRobinSkipsUnhealthy(t *testing.T) {
	t.Parallel()

	router, err := NewRouter([]string{
		"http://edge-1:9100",
		"http://edge-2:9100",
		"http://edge-3:9100",
	}, metrics.NewRegistry(), defaultCfg())
	if err != nil {
		t.Fatal(err)
	}

	first, err := router.NextEdge()
	if err != nil {
		t.Fatal(err)
	}
	second, err := router.NextEdge()
	if err != nil {
		t.Fatal(err)
	}
	third, err := router.NextEdge()
	if err != nil {
		t.Fatal(err)
	}

	if first.Name != "edge-1" || second.Name != "edge-2" || third.Name != "edge-3" {
		t.Fatalf("round robin order = %s, %s, %s", first.Name, second.Name, third.Name)
	}

	router.MarkHealth(second, false)
	next, err := router.NextEdge()
	if err != nil {
		t.Fatal(err)
	}
	if next.Name != "edge-1" {
		t.Fatalf("next healthy edge = %s, want edge-1", next.Name)
	}
}

func TestConsistentHashSameKeySameEdge(t *testing.T) {
	t.Parallel()

	router, err := NewRouter([]string{
		"http://edge-1:9100",
		"http://edge-2:9100",
		"http://edge-3:9100",
	}, metrics.NewRegistry(), RouterConfig{Strategy: StrategyConsistent, VNodes: 80})
	if err != nil {
		t.Fatal(err)
	}

	first, err := router.SelectEdge("/images/test.png")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		got, err := router.SelectEdge("/images/test.png")
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != first.Name {
			t.Fatalf("consistent hash unstable: %s then %s", first.Name, got.Name)
		}
	}
}

func TestConsistentHashDropsUnhealthyFromRing(t *testing.T) {
	t.Parallel()

	router, err := NewRouter([]string{
		"http://edge-1:9100",
		"http://edge-2:9100",
		"http://edge-3:9100",
	}, metrics.NewRegistry(), RouterConfig{Strategy: StrategyConsistent, VNodes: 100})
	if err != nil {
		t.Fatal(err)
	}

	keys := []string{"/a", "/b", "/c", "/d", "/e", "/f", "/g", "/h"}
	for _, key := range keys {
		if _, err := router.SelectEdge(key); err != nil {
			t.Fatal(err)
		}
	}

	down := router.byName["edge-2"]
	router.MarkHealth(down, false)

	for _, key := range keys {
		edge, err := router.SelectEdge(key)
		if err != nil {
			t.Fatal(err)
		}
		if edge.Name == "edge-2" {
			t.Fatalf("key %s still routed to unhealthy edge-2", key)
		}
	}
}

func TestLoadShedReturns503(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	router, err := NewRouter([]string{backend.URL}, metrics.NewRegistry(), RouterConfig{
		Strategy:    StrategyRoundRobin,
		MaxInFlight: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	var shedCount atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/images/test.png", nil)
			router.Proxy(rec, req)
			if rec.Code == http.StatusServiceUnavailable {
				shedCount.Add(1)
				if rec.Header().Get("X-Load-Shed") != "1" {
					t.Errorf("missing X-Load-Shed header")
				}
				if rec.Header().Get("Retry-After") == "" {
					t.Errorf("missing Retry-After header")
				}
			}
		}()
	}
	wg.Wait()

	if shedCount.Load() == 0 {
		t.Fatal("expected at least one shed response")
	}
}

func TestNewRouterRejectsEmptyEdges(t *testing.T) {
	t.Parallel()

	if _, err := NewRouter(nil, metrics.NewRegistry(), defaultCfg()); err == nil {
		t.Fatal("expected error")
	}
}

func TestNewRouterRejectsBadStrategy(t *testing.T) {
	t.Parallel()

	if _, err := NewRouter([]string{"http://edge-1:9100"}, metrics.NewRegistry(), RouterConfig{Strategy: "random"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestEdgeTargetNamesUseHostnames(t *testing.T) {
	t.Parallel()

	parsed, err := url.Parse("http://edge-1:9100")
	if err != nil {
		t.Fatal(err)
	}
	edge := EdgeTarget{Name: parsed.Hostname(), URL: parsed, Healthy: true}
	if edge.Name != "edge-1" {
		t.Fatalf("edge name = %q", edge.Name)
	}
}

func TestSameHostDifferentPortsStayOnTheRing(t *testing.T) {
	t.Parallel()

	router, err := NewRouter([]string{
		"http://127.0.0.1:9101",
		"http://127.0.0.1:9102",
	}, metrics.NewRegistry(), RouterConfig{Strategy: StrategyConsistent, VNodes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if router.ring.Len() != 2 {
		t.Fatalf("ring len = %d, want 2 (hosts must not collapse)", router.ring.Len())
	}
	members := router.ring.Members()
	if len(members) != 2 || members[0] == members[1] {
		t.Fatalf("members = %v", members)
	}
}

func TestGetFailsOverOnceWhenFirstEdgeIsDown(t *testing.T) {
	t.Parallel()

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("from-good"))
	}))
	defer good.Close()

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("from-bad"))
	}))
	badURL := bad.URL
	bad.Close()

	router, err := NewRouter([]string{badURL, good.URL}, metrics.NewRegistry(), RouterConfig{
		Strategy:    StrategyRoundRobin,
		MaxInFlight: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/images/test.png", nil)
	router.Proxy(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "from-good" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("X-Edge-Failover") == "" {
		t.Fatal("expected X-Edge-Failover")
	}
}
