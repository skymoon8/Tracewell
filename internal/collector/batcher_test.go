package collector_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/skymoon8/tracewell/internal/collector"
	"github.com/skymoon8/tracewell/internal/store"
	"github.com/skymoon8/tracewell/internal/trace"
)

// newBatcherStore opens a store backed by a temp SQLite file.
func newBatcherStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "test.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func batchSpan(id, parent string, kind trace.SpanKind) trace.Span {
	return trace.Span{
		Name:       "span-" + id,
		TraceID:    "aaaabbbbccccddddaaaabbbbccccdddd",
		SpanID:     id,
		ParentID:   parent,
		StartTime:  time.Now(),
		EndTime:    time.Now(),
		SpanKind:   kind,
		StatusCode: trace.StatusOK,
		Attributes: map[string]any{},
	}
}

func TestBatcherPersistsSpans(t *testing.T) {
	s := newBatcherStore(t)
	b := collector.NewBatcher(s)

	b.AddAll([]collector.SpanWithProject{
		{Project: "proj", Span: batchSpan("root", "", trace.KindChain)},
		{Project: "proj", Span: batchSpan("child", "root", trace.KindLLM)},
	})
	b.Close()

	var n int
	s.DB().QueryRow(`SELECT COUNT(*) FROM spans`).Scan(&n)
	if n != 2 {
		t.Errorf("span count = %d, want 2", n)
	}
	var projectCount int
	s.DB().QueryRow(`SELECT COUNT(*) FROM projects WHERE name = 'proj'`).Scan(&projectCount)
	if projectCount != 1 {
		t.Errorf("project count = %d, want 1", projectCount)
	}
}

func TestBatcherFlushesOnTimer(t *testing.T) {
	s := newBatcherStore(t)
	b := collector.NewBatcher(s)

	// Fewer than flushSize spans: only the timer guarantees they land.
	b.AddAll([]collector.SpanWithProject{
		{Project: "proj", Span: batchSpan("timer-span", "", trace.KindChain)},
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		s.DB().QueryRow(`SELECT COUNT(*) FROM spans WHERE span_id = 'timer-span'`).Scan(&n)
		if n == 1 {
			b.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	b.Close()
	t.Fatal("span not persisted within 2s of timer flush")
}

func TestBatcherCloseDrains(t *testing.T) {
	s := newBatcherStore(t)
	b := collector.NewBatcher(s)

	// Queue more than flushSize so some spans are still buffered when
	// Close is called; none may be lost.
	items := make([]collector.SpanWithProject, 0, 300)
	for i := 0; i < 300; i++ {
		id := "bulk-" + string(rune('a'+i%26)) + time.Now().Format("150405.000000000")
		items = append(items, collector.SpanWithProject{
			Project: "proj",
			Span:    batchSpan(id, "", trace.KindChain),
		})
	}
	b.AddAll(items)
	b.Close()

	var n int
	s.DB().QueryRow(`SELECT COUNT(*) FROM spans`).Scan(&n)
	if n != 300 {
		t.Errorf("span count = %d, want 300 (Close must drain the buffer)", n)
	}
}

func TestBatcherIsolatesFailures(t *testing.T) {
	s := newBatcherStore(t)
	b := collector.NewBatcher(s)

	good := batchSpan("good", "", trace.KindChain)
	// A channel value cannot be JSON-marshaled, so serializing this
	// span fails and it must be skipped without losing its neighbors.
	broken := batchSpan("broken", "", trace.KindChain)
	broken.Attributes = map[string]any{"bad": make(chan int)}

	b.AddAll([]collector.SpanWithProject{
		{Project: "proj", Span: good},
		{Project: "proj", Span: broken},
	})
	b.Close()

	var n int
	s.DB().QueryRow(`SELECT COUNT(*) FROM spans`).Scan(&n)
	if n != 1 {
		t.Errorf("span count = %d, want 1 (bad span isolated, good span saved)", n)
	}
}
