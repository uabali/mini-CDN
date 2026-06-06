package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"mini-cdn/internal/metrics"
	"mini-cdn/internal/safepath"
)

func main() {
	port := env("PORT", "9100")
	nodeName := env("NODE_NAME", "edge-local")
	originBase := strings.TrimRight(env("ORIGIN_BASE_URL", "http://origin:9000"), "/")
	cacheRoot := env("CACHE_ROOT", "./cache")
	ttl := durationEnv("CACHE_TTL", 5*time.Minute)
	maxBytes := bytesEnv("CACHE_MAX_BYTES", 500*1024*1024)

	registry := metrics.NewRegistry()
	cache := NewDiskCache(cacheRoot, originBase, nodeName, ttl, maxBytes, registry)

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
		if err := cache.PurgeAll(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/cache/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		target := "/" + strings.TrimPrefix(r.URL.Path, "/cache/")
		cleanPath, err := safepath.CleanRequestPath(target)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := cache.Purge(cleanPath); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		cleanPath, err := safepath.CleanRequestPath(r.URL.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cache.ServeFile(w, r, cleanPath)
	})

	log.Printf("%s listening on :%s, origin=%s, ttl=%s, max_bytes=%d", nodeName, port, originBase, ttl, maxBytes)
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

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func bytesEnv(key string, fallback int64) int64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}
