package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"mini-cdn/internal/metrics"
	"mini-cdn/internal/safepath"
)

func main() {
	port := env("PORT", "9000")
	root := env("ORIGIN_ROOT", "./files")
	registry := metrics.NewRegistry()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(registry.Render()))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		registry.Inc("cdn_origin_requests_total", map[string]string{"method": r.Method})
		defer func() {
			registry.ObserveDuration("cdn_origin_response_duration_seconds", nil, time.Since(start))
		}()

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		cleanPath, err := safepath.CleanRequestPath(r.URL.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		filePath, err := safepath.SafeJoin(root, cleanPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		file, err := os.Open(filePath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer file.Close()

		stat, err := file.Stat()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if stat.IsDir() {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("X-Origin-Server", "origin")
		http.ServeContent(w, r, stat.Name(), stat.ModTime(), file)
	})

	log.Printf("origin listening on :%s with root %s", port, root)
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
