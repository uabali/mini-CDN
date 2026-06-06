package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"mini-cdn/internal/metrics"
)

func main() {
	port := env("PORT", "8080")
	rawEdges := splitCSV(env("EDGES", "http://edge-1:9100,http://edge-2:9100,http://edge-3:9100"))
	registry := metrics.NewRegistry()

	router, err := NewRouter(rawEdges, registry)
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

	log.Printf("router listening on :%s with edges %s", port, strings.Join(rawEdges, ","))
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
