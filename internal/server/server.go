// Package server provides the HTTP server for Tracewell.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Server wraps an http.Server with Tracewell-specific configuration
// and graceful shutdown handling.
type Server struct {
	http *http.Server
}

// Config holds the server startup options.
type Config struct {
	// Addr is the TCP listen address, e.g. ":6006".
	Addr string
	// ReadHeaderTimeout guards against slowloris-style connections.
	ReadHeaderTimeout time.Duration
}

// New creates a Server serving the given handler.
func New(cfg Config, handler http.Handler) *Server {
	return &Server{
		http: &http.Server{
			Addr:              cfg.Addr,
			Handler:           handler,
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		},
	}
}

// ListenAndServe starts the HTTP server and blocks until it stops.
// It always returns a non-nil error; http.ErrServerClosed is mapped to nil
// so callers can treat "shut down gracefully" as success.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}
	slog.Info("http server listening", "addr", ln.Addr().String())
	err = s.http.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown gracefully drains in-flight requests. It returns an error if
// the context deadline expires before all connections have finished.
func (s *Server) Shutdown(ctx context.Context) error {
	slog.Info("shutting down http server")
	return s.http.Shutdown(ctx)
}
