package main

import (
	"net/url"
	"testing"

	"mini-cdn/internal/metrics"
)

func TestNextEdgeRoundRobinSkipsUnhealthy(t *testing.T) {
	t.Parallel()

	router, err := NewRouter([]string{
		"http://edge-1:9100",
		"http://edge-2:9100",
		"http://edge-3:9100",
	}, metrics.NewRegistry())
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

func TestNewRouterRejectsEmptyEdges(t *testing.T) {
	t.Parallel()

	if _, err := NewRouter(nil, metrics.NewRegistry()); err == nil {
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
