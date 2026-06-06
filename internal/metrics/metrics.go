package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Registry struct {
	mu       sync.Mutex
	counters map[string]float64
	gauges   map[string]float64
}

func NewRegistry() *Registry {
	return &Registry{
		counters: make(map[string]float64),
		gauges:   make(map[string]float64),
	}
}

func (r *Registry) Inc(name string, labels map[string]string) {
	r.Add(name, labels, 1)
}

func (r *Registry) Add(name string, labels map[string]string, value float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters[metricKey(name, labels)] += value
}

func (r *Registry) SetGauge(name string, labels map[string]string, value float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges[metricKey(name, labels)] = value
}

func (r *Registry) ObserveDuration(name string, labels map[string]string, d time.Duration) {
	seconds := d.Seconds()
	r.Add(name+"_count", labels, 1)
	r.Add(name+"_sum", labels, seconds)
}

func (r *Registry) Render() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var b strings.Builder
	counterKeys := sortedKeys(r.counters)
	for _, key := range counterKeys {
		fmt.Fprintf(&b, "%s %.6f\n", key, r.counters[key])
	}

	gaugeKeys := sortedKeys(r.gauges)
	for _, key := range gaugeKeys {
		fmt.Fprintf(&b, "%s %.6f\n", key, r.gauges[key])
	}

	return b.String()
}

func metricKey(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}

	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, key, escape(labels[key])))
	}
	return fmt.Sprintf("%s{%s}", name, strings.Join(parts, ","))
}

func sortedKeys(values map[string]float64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func escape(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return value
}
