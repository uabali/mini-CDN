package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"mini-cdn/internal/metrics"
)

func main() {
	port := env("PORT", "8080")
	rawEdges := splitCSV(env("EDGES", "http://edge-1:9100,http://edge-2:9100,http://edge-3:9100"))
	registry := metrics.NewRegistry()

	router, err := NewRouter(rawEdges, registry, RouterConfig{
		Strategy:    env("ROUTER_STRATEGY", StrategyConsistent),
		VNodes:      intEnv("HASH_VNODES", 100),
		MaxInFlight: intEnv("MAX_INFLIGHT", 0),
	})
	if err != nil {
		log.Fatal(err)
	}
	router.StartHealthChecks(2 * time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(registry.Render()))
	})
	mux.HandleFunc("/purge", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		router.FanOut(w, r)
	})
	mux.HandleFunc("/cache/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		router.FanOut(w, r)
	})
	mux.HandleFunc("/", router.Proxy)

	log.Printf("router listening on :%s strategy=%s max_inflight=%d edges=%s",
		port, router.Strategy(), intEnv("MAX_INFLIGHT", 0), strings.Join(rawEdges, ","))
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func intEnv(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
