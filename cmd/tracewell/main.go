// Command tracewell runs the Tracewell server.
package main

import (
	"context"
	"database/sql"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"github.com/skymoon8/tracewell/internal/collector"
	"github.com/skymoon8/tracewell/internal/server"
	"github.com/skymoon8/tracewell/internal/store"
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
		dataDir  = flag.String("data-dir", "data", "directory holding the SQLite database")
	)
	flag.Parse()

	// Open the store before binding the port: if persistence cannot be
	// set up there is nothing useful to serve.
	db, err := openDB(*dataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	st, err := store.Open(db)
	if err != nil {
		return err
	}

	batcher := collector.NewBatcher(st)

	srv := server.New(
		server.Config{
			Addr:              *httpAddr,
			ReadHeaderTimeout: 10 * time.Second,
		},
		server.NewHandler(batcher.AddAll, db),
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
		batcher.Close()
		return err
	case <-ctx.Done():
	}

	// Shutdown order matters: stop accepting requests first, then
	// drain buffered spans, then release the database.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		batcher.Close()
		return err
	}
	batcher.Close()
	slog.Info("tracewell stopped")
	return nil
}

// openDB opens the SQLite database inside dir, creating the directory
// if needed. WAL mode keeps reads cheap while writes stream in.
func openDB(dir string) (*sql.DB, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite",
		"file:"+filepath.Join(dir, "tracewell.db")+
			"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	// A single writer connection avoids SQLITE_BUSY contention between
	// batch flushes; the batcher serializes writes anyway.
	db.SetMaxOpenConns(1)
	return db, nil
}
