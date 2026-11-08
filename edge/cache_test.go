package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mini-cdn/internal/metrics"
)

func TestDiskCacheHitMiss(t *testing.T) {
	t.Parallel()

	var originHits int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&originHits, 1)
		_, _ = w.Write([]byte("hello"))
	}))
	defer origin.Close()

	cache := NewDiskCache(t.TempDir(), origin.URL, "edge-test", time.Minute, 1024, metrics.NewRegistry())

	req := httptest.NewRequest(http.MethodGet, "/images/test.txt", nil)
	first := httptest.NewRecorder()
	cache.ServeFile(first, req, "/images/test.txt")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d", first.Code)
	}
	if first.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("first X-Cache = %q", first.Header().Get("X-Cache"))
	}

	second := httptest.NewRecorder()
	cache.ServeFile(second, req, "/images/test.txt")
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d", second.Code)
	}
	if second.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("second X-Cache = %q", second.Header().Get("X-Cache"))
	}
	if got := atomic.LoadInt64(&originHits); got != 1 {
		t.Fatalf("origin hits = %d, want 1", got)
	}
}

func TestDiskCacheTTLExpiration(t *testing.T) {
	t.Parallel()

	var originHits int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit := atomic.AddInt64(&originHits, 1)
		_, _ = w.Write([]byte(string(rune('0' + hit))))
	}))
	defer origin.Close()

	cache := NewDiskCache(t.TempDir(), origin.URL, "edge-test", 10*time.Millisecond, 1024, metrics.NewRegistry())
	req := httptest.NewRequest(http.MethodGet, "/images/ttl.txt", nil)

	first := httptest.NewRecorder()
	cache.ServeFile(first, req, "/images/ttl.txt")
	if first.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("first X-Cache = %q", first.Header().Get("X-Cache"))
	}

	time.Sleep(25 * time.Millisecond)

	second := httptest.NewRecorder()
	cache.ServeFile(second, req, "/images/ttl.txt")
	if second.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("second X-Cache = %q", second.Header().Get("X-Cache"))
	}
	if got := atomic.LoadInt64(&originHits); got != 2 {
		t.Fatalf("origin hits = %d, want 2", got)
	}
}

func TestDiskCacheLRUEviction(t *testing.T) {
	t.Parallel()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/images/a.txt":
			_, _ = w.Write([]byte("aaaa"))
		case "/images/b.txt":
			_, _ = w.Write([]byte("bbbb"))
		case "/images/c.txt":
			_, _ = w.Write([]byte("cccc"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()

	root := t.TempDir()
	cache := NewDiskCache(root, origin.URL, "edge-test", time.Minute, 8, metrics.NewRegistry())

	fetch := func(path string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		cache.ServeFile(rec, req, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
	}

	fetch("/images/a.txt")
	time.Sleep(time.Millisecond)
	fetch("/images/b.txt")
	time.Sleep(time.Millisecond)
	fetch("/images/a.txt")
	time.Sleep(time.Millisecond)
	fetch("/images/c.txt")

	if _, err := os.Stat(filepath.Join(root, "images", "a.txt")); err != nil {
		t.Fatalf("expected a.txt to remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "images", "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected b.txt to be evicted, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "images", "c.txt")); err != nil {
		t.Fatalf("expected c.txt to remain: %v", err)
	}
}

func TestDiskCachePurgeAllKeepsRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cache := NewDiskCache(root, "http://example.invalid", "edge-test", time.Minute, 1024, metrics.NewRegistry())

	if err := os.MkdirAll(filepath.Join(root, "images"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "images", "a.txt"), []byte("aaaa"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := cache.PurgeAll(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(root); err != nil {
		t.Fatalf("expected cache root to remain: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty cache root, got %d entries", len(entries))
	}
}

func TestStaleServedWhenOriginIsDownBeforeWrite(t *testing.T) {
	t.Parallel()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("STABLEDATA"))
	}))

	cache := NewDiskCache(t.TempDir(), origin.URL, "edge-test", time.Hour, 1024, metrics.NewRegistry())
	req := httptest.NewRequest(http.MethodGet, "/images/obj.txt", nil)
	first := httptest.NewRecorder()
	cache.ServeFile(first, req, "/images/obj.txt")
	if first.Body.String() != "STABLEDATA" {
		t.Fatalf("first body = %q", first.Body.String())
	}

	origin.Close()
	cache.mu.Lock()
	cache.entries["/images/obj.txt"].CachedAt = time.Now().Add(-2 * time.Hour)
	cache.mu.Unlock()

	second := httptest.NewRecorder()
	cache.ServeFile(second, req, "/images/obj.txt")
	if second.Header().Get("X-Cache") != "STALE" {
		t.Fatalf("X-Cache = %q", second.Header().Get("X-Cache"))
	}
	if second.Body.String() != "STABLEDATA" {
		t.Fatalf("stale body = %q", second.Body.String())
	}
}

func TestPartialOriginFailureDoesNotAppendStale(t *testing.T) {
	t.Parallel()

	var once sync.Once
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var first bool
		once.Do(func() { first = true })
		if first {
			_, _ = w.Write([]byte("STABLEDATA"))
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("response writer cannot hijack")
			return
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = bufrw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 20\r\n\r\nPARTIAL")
		_ = bufrw.Flush()
	}))
	defer origin.Close()

	cache := NewDiskCache(t.TempDir(), origin.URL, "edge-test", time.Millisecond, 1024, metrics.NewRegistry())
	req := httptest.NewRequest(http.MethodGet, "/images/obj.txt", nil)
	first := httptest.NewRecorder()
	cache.ServeFile(first, req, "/images/obj.txt")
	time.Sleep(5 * time.Millisecond)

	second := httptest.NewRecorder()
	cache.ServeFile(second, req, "/images/obj.txt")
	body := second.Body.String()
	if !strings.Contains(body, "PARTIAL") || strings.Contains(body, "STABLEDATA") {
		t.Fatalf("body = %q, want the partial origin bytes and not the cached object", body)
	}
	if got := second.Header().Get("X-Cache"); got != "MISS" {
		t.Fatalf("X-Cache = %q, want MISS (stale must not be written after the response starts)", got)
	}
}

func TestConcurrentMissCountsBytesOnce(t *testing.T) {
	t.Parallel()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer origin.Close()

	cache := NewDiskCache(t.TempDir(), origin.URL, "edge-test", time.Minute, 1024*1024, metrics.NewRegistry())
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/images/a.txt", nil)
			rec := httptest.NewRecorder()
			cache.ServeFile(rec, req, "/images/a.txt")
		}()
	}
	wg.Wait()
	cache.mu.Lock()
	got := cache.totalBytes
	cache.mu.Unlock()
	if got != 5 {
		t.Fatalf("totalBytes = %d, want 5", got)
	}
}
