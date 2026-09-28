package server

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/skymoon8/tracewell/internal/api"
	"github.com/skymoon8/tracewell/internal/collector"
	"github.com/skymoon8/tracewell/internal/metrics"
	"github.com/skymoon8/tracewell/internal/server/health"
)

// ServerMetrics bundles everything the server layer instruments.
// Registry non-nil adds the /metrics endpoint; Ingestion non-nil
// enables collector and request instrumentation.
type ServerMetrics struct {
	Registry  *metrics.Registry
	Ingestion *metrics.Ingestion
}

// NewHandler builds the top-level HTTP mux.
// Route layout:
//
//	GET   /healthz      liveness probe
//	POST  /v1/traces    OTLP span ingestion
//	GET   /v1/...       REST query API (projects, traces)
//	GET   /metrics      Prometheus exposition (when Registry set)
func NewHandler(sink func([]collector.SpanWithProject), db *sql.DB, m ServerMetrics) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", http.StripPrefix("/healthz", health.NewHandler()))
	mux.Handle("POST /v1/traces", collector.Handler(sink, m.Ingestion))
	if db != nil {
		mux.Handle("GET /v1/", api.Handler(db))
	}
	if m.Registry != nil {
		mux.Handle("GET /metrics", metricsHandler(m.Registry))
	}
	return instrument(mux, m.Ingestion)
}

// metricsHandler serves the registry in the Prometheus text format.
func metricsHandler(r *metrics.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(r.Render()))
	}
}

// instrument wraps the mux with request counting and duration timing.
// The path label is intentionally omitted: method+code labels keep
// cardinality bounded without route templating.
func instrument(next http.Handler, m *metrics.Ingestion) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		m.HTTPRequests.Add(1)
		m.HTTPDuration.Observe(time.Since(started).Seconds())
	})
}
