package metrics

// Ingestion bundles the metrics covering the ingestion pipeline:
// receipt from the HTTP endpoint, queue depth, and flush outcomes.
// One struct instance is created at startup and shared by the
// collector and the batcher.
type Ingestion struct {
	// SpansReceived counts spans accepted at the OTLP endpoint.
	SpansReceived *Counter
	// QueueSize tracks how many spans sit buffered in the batcher.
	QueueSize *Gauge
	// SpansFlushed counts spans persisted to storage.
	SpansFlushed *Counter
	// SpanErrors counts spans dropped because persistence failed.
	SpanErrors *Counter
	// FlushDuration observes batch persistence time in seconds.
	FlushDuration *Histogram
	// HTTPRequests counts handled HTTP requests (all routes).
	HTTPRequests *Counter
	// HTTPDuration observes end-to-end request handling time.
	HTTPDuration *Histogram
}

// NewIngestion registers the ingestion metric set on the registry.
// Histogram buckets cover the millisecond-to-seconds range typical of
// SQLite batch inserts.
func NewIngestion(r *Registry) *Ingestion {
	return &Ingestion{
		SpansReceived: r.NewCounter(
			"tracewell_ingestion_spans_received_total",
			"Spans accepted at the OTLP endpoint",
		),
		QueueSize: r.NewGauge(
			"tracewell_ingestion_queue_size",
			"Spans currently buffered in the write queue",
		),
		SpansFlushed: r.NewCounter(
			"tracewell_ingestion_spans_flushed_total",
			"Spans persisted to storage",
		),
		SpanErrors: r.NewCounter(
			"tracewell_ingestion_span_errors_total",
			"Spans dropped because persistence failed",
		),
		FlushDuration: r.NewHistogram(
			"tracewell_ingestion_flush_duration_seconds",
			"Time to persist one flush batch",
			[]float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
		),
		HTTPRequests: r.NewCounter(
			"tracewell_http_requests_total",
			"HTTP requests handled",
		),
		HTTPDuration: r.NewHistogram(
			"tracewell_http_request_duration_seconds",
			"HTTP request handling time",
			[]float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
		),
	}
}
