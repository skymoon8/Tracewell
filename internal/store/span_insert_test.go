package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/skymoon8/tracewell/internal/store"
	"github.com/skymoon8/tracewell/internal/trace"
)

// newTestStore opens an in-memory-ish SQLite store backed by a temp
// file (shared in-memory DBs don't play well with multiple conns).
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "test.db")+"?_pragma=journal_mode(WAL)&_pragmabusy_timeout(5000)")
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

func span(id, parent string, kind trace.SpanKind, status trace.StatusCode, tokP, tokC int, start, end time.Time) trace.Span {
	s := trace.Span{
		Name:       "span-" + id,
		TraceID:    "aaaabbbbccccddddaaaabbbbccccdddd",
		SpanID:     id,
		ParentID:   parent,
		StartTime:  start,
		EndTime:    end,
		SpanKind:   kind,
		StatusCode: status,
		Attributes: map[string]any{},
	}
	if kind == trace.KindLLM {
		s.TokenUsage = &trace.TokenUsage{Prompt: tokP, Completion: tokC, Total: tokP + tokC}
	}
	return s
}

var base = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func TestInsertSpanBasic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sp := span("sp1", "", trace.KindLLM, trace.StatusError, 10, 5, base, base.Add(time.Second))

	if err := s.InsertSpan(ctx, sp, "proj"); err != nil {
		t.Fatal(err)
	}

	var name, kind, status string
	var cumErr, cumP, cumC int
	var tokP, tokC sql.NullInt64
	err := s.DB().QueryRow(`
        SELECT name, span_kind, status_code,
               cumulative_error_count, cumulative_llm_token_count_prompt,
               cumulative_llm_token_count_completion,
               llm_token_count_prompt, llm_token_count_completion
        FROM spans WHERE span_id = 'sp1'`).
		Scan(&name, &kind, &status, &cumErr, &cumP, &cumC, &tokP, &tokC)
	if err != nil {
		t.Fatal(err)
	}
	if name != "span-sp1" || kind != "LLM" || status != "ERROR" {
		t.Errorf("row = %s/%s/%s", name, kind, status)
	}
	if cumErr != 1 || cumP != 10 || cumC != 5 {
		t.Errorf("cumulative = %d/%d/%d, want 1/10/5", cumErr, cumP, cumC)
	}
	if !tokP.Valid || tokP.Int64 != 10 || !tokC.Valid || tokC.Int64 != 5 {
		t.Errorf("self tokens = %v %v", tokP, tokC)
	}

	// Project and trace rows came along.
	var projectCount, traceCount int
	s.DB().QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&projectCount)
	s.DB().QueryRow(`SELECT COUNT(*) FROM traces`).Scan(&traceCount)
	if projectCount != 1 || traceCount != 1 {
		t.Errorf("rows = %d projects, %d traces", projectCount, traceCount)
	}
}

func TestInsertSpanDuplicateIgnored(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sp := span("dup", "", trace.KindChain, trace.StatusOK, 0, 0, base, base)

	if err := s.InsertSpan(ctx, sp, "proj"); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertSpan(ctx, sp, "proj"); err != nil {
		t.Fatal(err)
	}

	var n int
	s.DB().QueryRow(`SELECT COUNT(*) FROM spans`).Scan(&n)
	if n != 1 {
		t.Errorf("span count = %d, want 1 (duplicates ignored)", n)
	}
}

func TestTraceTimeRangeExpands(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Parent lands first covering [12:00:00, 12:00:02].
	if err := s.InsertSpan(ctx, span("p", "", trace.KindChain, trace.StatusOK, 0, 0, base, base.Add(2*time.Second)), "proj"); err != nil {
		t.Fatal(err)
	}
	// An out-of-order child widens both ends.
	child := span("c", "p", trace.KindChain, trace.StatusOK, 0, 0, base.Add(-time.Second), base.Add(5*time.Second))
	if err := s.InsertSpan(ctx, child, "proj"); err != nil {
		t.Fatal(err)
	}

	var startS, endS string
	s.DB().QueryRow(`SELECT start_time, end_time FROM traces`).Scan(&startS, &endS)
	if want := base.Add(-time.Second).UTC().Format(time.RFC3339Nano); startS != want {
		t.Errorf("start = %s, want %s", startS, want)
	}
	if want := base.Add(5 * time.Second).UTC().Format(time.RFC3339Nano); endS != want {
		t.Errorf("end = %s, want %s", endS, want)
	}
}

func TestCumulativeOrderIndependence(t *testing.T) {
	t.Run("parent first", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()
		// Root -> child -> grandchild chain; tokens on the grandchild.
		must(t, s.InsertSpan(ctx, span("root", "", trace.KindChain, trace.StatusOK, 0, 0, base, base), "proj"))
		must(t, s.InsertSpan(ctx, span("mid", "root", trace.KindChain, trace.StatusOK, 0, 0, base, base), "proj"))
		must(t, s.InsertSpan(ctx, span("leaf", "mid", trace.KindLLM, trace.StatusError, 7, 3, base, base), "proj"))

		checkCumulative(t, s, "root", 1, 7, 3)
		checkCumulative(t, s, "mid", 1, 7, 3)
		checkCumulative(t, s, "leaf", 1, 7, 3)
	})
	t.Run("child first", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()
		// Reverse order: leaf, then mid, then root.
		must(t, s.InsertSpan(ctx, span("leaf", "mid", trace.KindLLM, trace.StatusError, 7, 3, base, base), "proj"))
		must(t, s.InsertSpan(ctx, span("mid", "root", trace.KindChain, trace.StatusOK, 0, 0, base, base), "proj"))
		must(t, s.InsertSpan(ctx, span("root", "", trace.KindChain, trace.StatusOK, 0, 0, base, base), "proj"))

		checkCumulative(t, s, "root", 1, 7, 3)
		checkCumulative(t, s, "mid", 1, 7, 3)
		checkCumulative(t, s, "leaf", 1, 7, 3)
	})
	t.Run("random interleaving", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()
		// Two leaf LLM spans under different branches, root last.
		must(t, s.InsertSpan(ctx, span("l1", "m1", trace.KindLLM, trace.StatusOK, 5, 0, base, base), "proj"))
		must(t, s.InsertSpan(ctx, span("root", "", trace.KindChain, trace.StatusOK, 0, 0, base, base), "proj"))
		must(t, s.InsertSpan(ctx, span("m1", "root", trace.KindChain, trace.StatusOK, 0, 0, base, base), "proj"))
		must(t, s.InsertSpan(ctx, span("l2", "m2", trace.KindLLM, trace.StatusError, 0, 4, base, base), "proj"))
		must(t, s.InsertSpan(ctx, span("m2", "root", trace.KindChain, trace.StatusOK, 0, 0, base, base), "proj"))

		checkCumulative(t, s, "root", 1, 5, 4)
		checkCumulative(t, s, "m1", 0, 5, 0)
		checkCumulative(t, s, "m2", 1, 0, 4)
	})
}

func checkCumulative(t *testing.T, s *store.Store, spanID string, wantErr, wantP, wantC int) {
	t.Helper()
	var cumErr, cumP, cumC int
	if err := s.DB().QueryRow(`
        SELECT cumulative_error_count,
               cumulative_llm_token_count_prompt,
               cumulative_llm_token_count_completion
        FROM spans WHERE span_id = ?`, spanID).
		Scan(&cumErr, &cumP, &cumC); err != nil {
		t.Fatalf("span %s: %v", spanID, err)
	}
	if cumErr != wantErr || cumP != wantP || cumC != wantC {
		t.Errorf("span %s cumulative = (%d,%d,%d), want (%d,%d,%d)",
			spanID, cumErr, cumP, cumC, wantErr, wantP, wantC)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
