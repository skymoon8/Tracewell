package metrics

import (
	"strings"
	"testing"
)

func TestCounterAndGaugeRender(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("test_events_total", "Events handled")
	g := r.NewGauge("test_depth", "Current depth")

	c.Add(3)
	c.Add(2)
	c.Add(-1) // ignored: counters never decrease
	g.Set(42)

	out := r.Render()
	// Metrics render in name order, not registration order.
	want := `# HELP test_depth Current depth
# TYPE test_depth gauge
test_depth 42
# HELP test_events_total Events handled
# TYPE test_events_total counter
test_events_total 5
`
	if out != want {
		t.Errorf("render =\n%s\nwant\n%s", out, want)
	}
}

func TestHistogramRender(t *testing.T) {
	r := NewRegistry()
	h := r.NewHistogram("test_latency_seconds", "Latency", []float64{0.1, 1})

	h.Observe(0.05) // bucket 0.1 and 1 and Inf
	h.Observe(0.5)  // bucket 1 and Inf
	h.Observe(5)    // Inf only

	out := r.Render()
	for _, want := range []string{
		`# TYPE test_latency_seconds histogram`,
		`test_latency_seconds_bucket{le="0.1"} 1`,
		`test_latency_seconds_bucket{le="1"} 2`,
		`test_latency_seconds_bucket{le="+Inf"} 3`,
		`test_latency_seconds_count 3`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q in:\n%s", want, out)
		}
	}
}

func TestDuplicateRegistrationPanics(t *testing.T) {
	r := NewRegistry()
	r.NewCounter("dup_total", "first")
	defer func() {
		if recover() == nil {
			t.Error("duplicate registration did not panic")
		}
	}()
	r.NewCounter("dup_total", "second")
}
