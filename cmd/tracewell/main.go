// Command tracewell runs the Tracewell server.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/skymoon8/tracewell/internal/server"
)

func main() {
	if err := run(); err != nil {
		slog.Error("tracewell exited with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		httpAddr = flag.String("http-addr", ":6006", "HTTP listen address")
	)
	flag.Parse()

	srv := server.New(
		server.Config{
			Addr:              *httpAddr,
			ReadHeaderTimeout: 10 * time.Second,
		},
		server.NewHandler(),
	)

	// Block until SIGINT/SIGTERM, then drain in-flight requests.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	slog.Info("tracewell stopped")
	return nil
}
