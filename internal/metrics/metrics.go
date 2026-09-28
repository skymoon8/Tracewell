// Package metrics implements a minimal Prometheus-compatible metrics
// registry with the text exposition format, avoiding a client-library
// dependency for the handful of ingestion metrics Tracewell exports.
package metrics

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Registry holds named metrics and renders them in the Prometheus
// text exposition format (version 0.0.4).
type Registry struct {
	mu         sync.Mutex
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	histograms map[string]*Histogram
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		counters:   map[string]*Counter{},
		gauges:     map[string]*Gauge{},
		histograms: map[string]*Histogram{},
	}
}

// NewCounter registers a monotonically increasing counter. Registering
// the same name twice panics: metric definitions are static.
func (r *Registry) NewCounter(name, help string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.counters[name]; dup {
		panic("metrics: duplicate counter " + name)
	}
	c := &Counter{name: name, help: help}
	r.counters[name] = c
	return c
}

// NewGauge registers a gauge: a value that can go up and down.
func (r *Registry) NewGauge(name, help string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.gauges[name]; dup {
		panic("metrics: duplicate gauge " + name)
	}
	g := &Gauge{name: name, help: help}
	r.gauges[name] = g
	return g
}

// NewHistogram registers a cumulative histogram with the given bucket
// upper bounds, to which +Inf is always appended.
func (r *Registry) NewHistogram(name, help string, buckets []float64) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.histograms[name]; dup {
		panic("metrics: duplicate histogram " + name)
	}
	h := &Histogram{name: name, help: help, buckets: buckets}
	sort.Float64s(h.buckets)
	h.counts = make([]uint64, len(h.buckets))
	r.histograms[name] = h
	return h
}

// Render writes every registered metric in the Prometheus text
// exposition format. Output is deterministic: metrics appear in
// registration name order.
func (r *Registry) Render() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	names := make([]string, 0, len(r.counters)+len(r.gauges)+len(r.histograms))
	for n := range r.counters {
		names = append(names, n)
	}
	for n := range r.gauges {
		names = append(names, n)
	}
	for n := range r.histograms {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		switch {
		case r.counters[n] != nil:
			r.counters[n].render(&b, "counter")
		case r.gauges[n] != nil:
			r.gauges[n].render(&b, "gauge")
		default:
			r.histograms[n].render(&b)
		}
	}
	return b.String()
}

// Counter is a cumulative metric that only increases.
type Counter struct {
	name string
	help string
	n    atomic.Int64 // integral values keep counters exact
}

// Add increases the counter by the given non-negative amount.
func (c *Counter) Add(delta int64) {
	if delta < 0 {
		return // counters never decrease; ignore programming errors
	}
	c.n.Add(delta)
}

// Value reports the current count.
func (c *Counter) Value() int64 { return c.n.Load() }

func (c *Counter) render(b *strings.Builder, typ string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n%s %d\n",
		c.name, escapeHelp(c.help), c.name, typ, c.name, c.n.Load())
}

// Gauge is a metric holding a single numeric value that can go up and
// down, including negative values and integers only in this build.
type Gauge struct {
	name string
	help string
	n    atomic.Int64
}

// Set replaces the gauge value.
func (g *Gauge) Set(v int64) { g.n.Store(v) }

// Value reports the current gauge value.
func (g *Gauge) Value() int64 { return g.n.Load() }

func (g *Gauge) render(b *strings.Builder, typ string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n%s %d\n",
		g.name, escapeHelp(g.help), g.name, typ, g.name, g.n.Load())
}

// Histogram tracks observations in cumulative buckets plus sum/count.
type Histogram struct {
	name    string
	help    string
	buckets []float64 // sorted ascending, +Inf implicit

	mu     sync.Mutex
	counts []uint64 // counts[i] is observations <= buckets[i]
	sum    float64
	total  uint64
}

// Observe records one value.
func (h *Histogram) Observe(v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sum += v
	h.total++
	for i, bound := range h.buckets {
		if v <= bound {
			h.counts[i]++
		}
	}
}

func (h *Histogram) render(b *strings.Builder) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s histogram\n",
		h.name, escapeHelp(h.help), h.name)
	h.mu.Lock()
	defer h.mu.Unlock()
	// counts[i] already holds cumulative observations (Observe bumps
	// every bucket whose bound the value fits under), so rendering is
	// a straight dump.
	for i, bound := range h.buckets {
		le := formatFloat(bound)
		fmt.Fprintf(b, "%s_bucket{le=\"%s\"} %d\n", h.name, le, h.counts[i])
	}
	fmt.Fprintf(b, "%s_bucket{le=\"+Inf\"} %d\n", h.name, h.total)
	fmt.Fprintf(b, "%s_sum %s\n", h.name, formatFloat(h.sum))
	fmt.Fprintf(b, "%s_count %d\n", h.name, h.total)
}

// formatFloat renders a float the way Prometheus expects: integers
// without a trailing decimal, NaN/Inf spelled out.
func formatFloat(v float64) string {
	if math.IsNaN(v) {
		return "NaN"
	}
	if math.IsInf(v, 1) {
		return "+Inf"
	}
	if math.IsInf(v, -1) {
		return "-Inf"
	}
	if v == math.Trunc(v) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%g", v)
}

// escapeHelp strips characters the exposition format forbids in help
// text.
func escapeHelp(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}
