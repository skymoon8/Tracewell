package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skymoon8/tracewell/internal/metrics"
	"github.com/skymoon8/tracewell/internal/server"
)

func TestHealthz(t *testing.T) {
	srv := httptest.NewServer(server.NewHandler(nil, nil, server.ServerMetrics{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, want %q", body.Status, "ok")
	}
}

func TestUnknownRouteReturns404(t *testing.T) {
	srv := httptest.NewServer(server.NewHandler(nil, nil, server.ServerMetrics{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/nope")
	if err != nil {
		t.Fatalf("GET /nope: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	reg := metrics.NewRegistry()
	ing := metrics.NewIngestion(reg)
	srv := httptest.NewServer(server.NewHandler(nil, nil, server.ServerMetrics{Registry: reg, Ingestion: ing}))
	defer srv.Close()

	// Generate some traffic through the instrumented mux.
	if _, err := http.Get(srv.URL + "/healthz"); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("content type = %q", ct)
	}
	text := string(body)
	for _, want := range []string{
		"tracewell_http_requests_total 1",
		"tracewell_ingestion_spans_received_total 0",
		"tracewell_http_request_duration_seconds_bucket",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics output missing %q in:\n%s", want, text)
		}
	}
}
