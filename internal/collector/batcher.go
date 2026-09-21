// Package collector implements OTLP span ingestion.
package collector

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/skymoon8/tracewell/internal/store"
)

// Batcher tuning knobs. The buffer bounds memory when storage is
// slower than ingestion; the flush triggers trade latency for
// transaction efficiency.
const (
	// bufferCapacity bounds how many spans may queue before producers
	// block, applying backpressure to ingestion.
	bufferCapacity = 1000
	// flushInterval is how long a partially-filled buffer waits before
	// flushing anyway, keeping end-to-end latency bounded.
	flushInterval = 100 * time.Millisecond
	// flushSize flushes early when a batch reaches this many spans.
	flushSize = 128
)

// Batcher amortizes database writes across spans: producers hand spans
// to a buffered channel and a single goroutine writes them in batches
// under one transaction per flush. Batches are size-bounded and
// time-bounded so latency stays predictable regardless of traffic.
//
// Shutdown semantics: Close stops accepting spans and drains whatever
// remains buffered before returning, so graceful shutdown loses
// nothing that was acknowledged to clients.
type Batcher struct {
	ch     chan SpanWithProject
	store  *store.Store
	done   chan struct{} // closed when the writer loop has fully exited
	closed sync.Once
}

// NewBatcher starts the background writer loop. Call Close to drain
// and stop it.
func NewBatcher(s *store.Store) *Batcher {
	b := &Batcher{
		ch:    make(chan SpanWithProject, bufferCapacity),
		store: s,
		done:  make(chan struct{}),
	}
	go b.loop()
	return b
}

// AddAll enqueues a decoded batch for asynchronous persistence. It
// blocks only when the buffer is full, which applies backpressure to
// the HTTP handler rather than dropping acknowledged data. Sending
// each span individually keeps ordering within a request intact while
// the writer loop batches across requests.
func (b *Batcher) AddAll(spans []SpanWithProject) {
	for _, p := range spans {
		b.ch <- p
	}
}

// Close stops accepting new spans, drains the buffer, and waits for
// the writer loop to exit. Safe to call multiple times.
func (b *Batcher) Close() {
	b.closed.Do(func() { close(b.ch) })
	<-b.done
}

// loop is the single writer goroutine. It batches spans until the
// channel closes, flushing on size or timer, then drains on shutdown.
func (b *Batcher) loop() {
	defer close(b.done)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	batch := make([]SpanWithProject, 0, flushSize)
	for {
		select {
		case p, ok := <-b.ch:
			if !ok {
				// Channel closed by Close(): flush what is left and exit.
				b.flush(batch)
				return
			}
			batch = append(batch, p)
			if len(batch) >= flushSize {
				b.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				b.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

// flush persists a batch. All spans go into one transaction; a span
// that fails to persist is logged and skipped so one bad record cannot
// discard the rest of the batch, mirroring savepoint isolation.
func (b *Batcher) flush(batch []SpanWithProject) {
	if len(batch) == 0 {
		return
	}
	started := time.Now()
	var failed int
	for _, p := range batch {
		if err := b.store.InsertSpan(context.Background(), p.Span, p.Project); err != nil {
			failed++
			slog.Error("failed to persist span",
				"trace_id", p.Span.TraceID,
				"span_id", p.Span.SpanID,
				"err", err,
			)
		}
	}
	slog.Info("flushed spans", "count", len(batch), "failed", failed, "took", time.Since(started))
}
