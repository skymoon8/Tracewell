// Package health exposes liveness endpoints.
package health

import "net/http"

// NewHandler returns a handler that serves GET / (the mux strips the
// /healthz prefix before dispatching).
func NewHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
	})
}
