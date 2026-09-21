package server

import (
	"net/http"

	"github.com/skymoon8/tracewell/internal/collector"
	"github.com/skymoon8/tracewell/internal/server/health"
)

// NewHandler builds the top-level HTTP mux.
// Route layout:
//
//	GET   /healthz      liveness probe
//	POST  /v1/traces    OTLP span ingestion
func NewHandler(sink func([]collector.SpanWithProject)) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", http.StripPrefix("/healthz", health.NewHandler()))
	mux.Handle("POST /v1/traces", collector.Handler(sink))
	return mux
}
