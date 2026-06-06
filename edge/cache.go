package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mini-cdn/internal/metrics"
	"mini-cdn/internal/safepath"
)

type CacheEntry struct {
	Path       string
	Size       int64
	CachedAt   time.Time
	LastAccess time.Time
}

type DiskCache struct {
	root       string
	originBase string
	nodeName   string
	ttl        time.Duration
	maxBytes   int64
	client     *http.Client
	metrics    *metrics.Registry

	mu         sync.Mutex
	entries    map[string]*CacheEntry
	totalBytes int64
}

func NewDiskCache(root, originBase, nodeName string, ttl time.Duration, maxBytes int64, registry *metrics.Registry) *DiskCache {
	return &DiskCache{
		root:       root,
		originBase: originBase,
		nodeName:   nodeName,
		ttl:        ttl,
		maxBytes:   maxBytes,
		client:     &http.Client{Timeout: 30 * time.Second},
		metrics:    registry,
		entries:    make(map[string]*CacheEntry),
	}
}

func (c *DiskCache) ServeFile(w http.ResponseWriter, r *http.Request, cleanPath string) {
	start := time.Now()
	labels := map[string]string{"edge": c.nodeName}
	c.metrics.Inc("cdn_requests_total", labels)
	defer func() {
		c.metrics.ObserveDuration("cdn_response_duration_seconds", labels, time.Since(start))
	}()

	if entry, filePath, ok := c.validEntry(cleanPath); ok {
		c.metrics.Inc("cdn_cache_hits_total", labels)
		c.serveCached(w, r, filePath, entry, "HIT")
		return
	}

	if err := c.fetchAndStream(w, r, cleanPath); err != nil {
		if entry, filePath, ok := c.staleEntry(cleanPath); ok {
			c.metrics.Inc("cdn_cache_stale_serves_total", labels)
			c.serveCached(w, r, filePath, entry, "STALE")
			return
		}

		log.Printf("edge %s failed to fetch %s: %v", c.nodeName, cleanPath, err)
		http.Error(w, "origin fetch failed", http.StatusBadGateway)
		return
	}
}

func (c *DiskCache) validEntry(cleanPath string) (*CacheEntry, string, bool) {
	entry, filePath, ok := c.entryFromDisk(cleanPath)
	if !ok {
		return nil, "", false
	}
	if time.Since(entry.CachedAt) > c.ttl {
		return nil, "", false
	}

	c.mu.Lock()
	entry.LastAccess = time.Now()
	c.mu.Unlock()
	return entry, filePath, true
}

func (c *DiskCache) staleEntry(cleanPath string) (*CacheEntry, string, bool) {
	entry, filePath, ok := c.entryFromDisk(cleanPath)
	if !ok {
		return nil, "", false
	}
	c.mu.Lock()
	entry.LastAccess = time.Now()
	c.mu.Unlock()
	return entry, filePath, true
}

func (c *DiskCache) entryFromDisk(cleanPath string) (*CacheEntry, string, bool) {
	filePath, err := safepath.SafeJoin(c.root, cleanPath)
	if err != nil {
		return nil, "", false
	}

	c.mu.Lock()
	entry := c.entries[cleanPath]
	c.mu.Unlock()
	if entry != nil {
		return entry, filePath, true
	}

	stat, err := os.Stat(filePath)
	if err != nil || stat.IsDir() {
		return nil, "", false
	}

	entry = &CacheEntry{
		Path:       cleanPath,
		Size:       stat.Size(),
		CachedAt:   stat.ModTime(),
		LastAccess: stat.ModTime(),
	}
	c.mu.Lock()
	c.entries[cleanPath] = entry
	c.totalBytes += entry.Size
	c.metrics.SetGauge("cdn_cache_bytes", map[string]string{"edge": c.nodeName}, float64(c.totalBytes))
	c.mu.Unlock()
	return entry, filePath, true
}

func (c *DiskCache) serveCached(w http.ResponseWriter, r *http.Request, filePath string, entry *CacheEntry, cacheStatus string) {
	file, err := os.Open(filePath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer file.Close()

	w.Header().Set("X-Cache", cacheStatus)
	w.Header().Set("X-Edge-Node", c.nodeName)
	http.ServeContent(w, r, filepath.Base(filePath), entry.CachedAt, file)
}

func (c *DiskCache) fetchAndStream(w http.ResponseWriter, r *http.Request, cleanPath string) error {
	labels := map[string]string{"edge": c.nodeName}
	c.metrics.Inc("cdn_cache_misses_total", labels)
	c.metrics.Inc("cdn_origin_fetches_total", labels)

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, c.originBase+cleanPath, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("origin returned %s", resp.Status)
	}

	filePath, err := safepath.SafeJoin(c.root, cleanPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(filePath), ".download-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	for key, values := range resp.Header {
		if key == "Content-Length" {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.Header().Set("X-Cache", "MISS")
	w.Header().Set("X-Edge-Node", c.nodeName)
	w.WriteHeader(http.StatusOK)

	written, copyErr := io.Copy(io.MultiWriter(w, tmp), resp.Body)
	if copyErr != nil {
		return copyErr
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		return err
	}

	now := time.Now()
	c.mu.Lock()
	if old := c.entries[cleanPath]; old != nil {
		c.totalBytes -= old.Size
	}
	c.entries[cleanPath] = &CacheEntry{
		Path:       cleanPath,
		Size:       written,
		CachedAt:   now,
		LastAccess: now,
	}
	c.totalBytes += written
	c.metrics.SetGauge("cdn_cache_bytes", map[string]string{"edge": c.nodeName}, float64(c.totalBytes))
	c.mu.Unlock()

	return c.evictIfNeeded(cleanPath)
}

func (c *DiskCache) Purge(cleanPath string) error {
	filePath, err := safepath.SafeJoin(c.root, cleanPath)
	if err != nil {
		return err
	}

	c.mu.Lock()
	if entry := c.entries[cleanPath]; entry != nil {
		c.totalBytes -= entry.Size
		delete(c.entries, cleanPath)
	}
	c.metrics.SetGauge("cdn_cache_bytes", map[string]string{"edge": c.nodeName}, float64(c.totalBytes))
	c.mu.Unlock()

	err = os.Remove(filePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (c *DiskCache) PurgeAll() error {
	c.mu.Lock()
	c.entries = make(map[string]*CacheEntry)
	c.totalBytes = 0
	c.metrics.SetGauge("cdn_cache_bytes", map[string]string{"edge": c.nodeName}, 0)
	c.mu.Unlock()

	if err := os.MkdirAll(c.root, 0o755); err != nil {
		return err
	}
	children, err := os.ReadDir(c.root)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := os.RemoveAll(filepath.Join(c.root, child.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (c *DiskCache) evictIfNeeded(protectedPath string) error {
	for {
		c.mu.Lock()
		if c.totalBytes <= c.maxBytes || len(c.entries) == 0 {
			c.metrics.SetGauge("cdn_cache_bytes", map[string]string{"edge": c.nodeName}, float64(c.totalBytes))
			c.mu.Unlock()
			return nil
		}

		var victim *CacheEntry
		for _, entry := range c.entries {
			if entry.Path == protectedPath && len(c.entries) > 1 {
				continue
			}
			if victim == nil || entry.LastAccess.Before(victim.LastAccess) {
				victim = entry
			}
		}
		if victim == nil {
			c.mu.Unlock()
			return nil
		}

		delete(c.entries, victim.Path)
		c.totalBytes -= victim.Size
		c.metrics.SetGauge("cdn_cache_bytes", map[string]string{"edge": c.nodeName}, float64(c.totalBytes))
		c.metrics.Inc("cdn_evictions_total", map[string]string{"edge": c.nodeName})
		c.mu.Unlock()

		filePath, err := safepath.SafeJoin(c.root, victim.Path)
		if err != nil {
			return err
		}
		if err := os.Remove(filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
}
