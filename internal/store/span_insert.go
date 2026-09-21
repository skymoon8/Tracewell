package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/skymoon8/tracewell/internal/trace"
)

// Span fields derived from attributes for persistence.
type spanColumns struct {
	attributesJSON string
	eventsJSON     string
	tokenPrompt    sql.NullInt64
	tokenComp      sql.NullInt64
}

// projectCache memoizes project name -> rowid lookups. Projects are
// few and immutable in the current model, so a plain mutex-protected
// map avoids a SELECT on every span batch.
type projectCache struct {
	mu  sync.Mutex
	ids map[string]int64
}

func (c *projectCache) get(name string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.ids[name]
	return id, ok
}

func (c *projectCache) put(name string, id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids[name] = id
}

// InsertSpan persists one span, creating or updating its project and
// trace rows as needed. It is safe for concurrent use.
//
// Semantics:
//
//   - The trace's time range expands to cover the span (out-of-order
//     arrivals widen the range).
//   - Duplicate span_ids are silently ignored: OTLP exporters retry,
//     so idempotency is required, and ON CONFLICT DO NOTHING provides
//     it in one statement.
//   - Cumulative counters (error count, LLM tokens) stay consistent
//     regardless of arrival order: a span absorbs its existing direct
//     children's cumulative totals when it lands after them, and
//     propagates its own totals to all ancestors when it lands after
//     them (recursive CTE).
func (s *Store) InsertSpan(ctx context.Context, span trace.Span, projectName string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit

	if err := s.insertSpanTx(ctx, tx, span, projectName); err != nil {
		return err
	}
	return tx.Commit()
}

// insertSpanTx persists one span inside an open transaction. It is
// shared by the single-span and batch paths so both get identical
// semantics.
//
// Semantics:
//
//   - The trace's time range expands to cover the span (out-of-order
//     arrivals widen the range).
//   - Duplicate span_ids are silently ignored: OTLP exporters retry,
//     so idempotency is required, and ON CONFLICT DO NOTHING provides
//     it in one statement.
//   - Cumulative counters (error count, LLM tokens) stay consistent
//     regardless of arrival order: a span absorbs its existing direct
//     children's cumulative totals when it lands after them, and
//     propagates its absorbed cumulative totals to all ancestors when
//     it lands after them (recursive CTE).
func (s *Store) insertSpanTx(ctx context.Context, tx *sql.Tx, span trace.Span, projectName string) error {
	cols, err := spanToColumns(span)
	if err != nil {
		return fmt.Errorf("serialize span %s: %w", span.SpanID, err)
	}

	projectRowID, err := s.ensureProject(ctx, tx, projectName)
	if err != nil {
		return fmt.Errorf("ensure project: %w", err)
	}
	traceRowID, err := s.upsertTrace(ctx, tx, span, projectRowID)
	if err != nil {
		return fmt.Errorf("upsert trace: %w", err)
	}

	// Absorb descendants that arrived first: they carry their own
	// subtrees' totals in their cumulative columns.
	var childErr, childTokP, childTokC sql.NullInt64
	err = tx.QueryRowContext(ctx, `
        SELECT SUM(cumulative_error_count),
               SUM(cumulative_llm_token_count_prompt),
               SUM(cumulative_llm_token_count_completion)
        FROM spans WHERE trace_rowid = ? AND parent_id = ?`,
		traceRowID, span.SpanID).Scan(&childErr, &childTokP, &childTokC)
	if err != nil {
		return fmt.Errorf("sum children: %w", err)
	}

	selfErr, selfTokP, selfTokC := selfTotals(span)
	cumErr := selfErr + nullSum(childErr)
	cumTokP := selfTokP + nullSum(childTokP)
	cumTokC := selfTokC + nullSum(childTokC)

	res, err := tx.ExecContext(ctx, `
        INSERT INTO spans (
            trace_rowid, span_id, parent_id, name, span_kind,
            start_time, end_time, attributes, events,
            status_code, status_message,
            cumulative_error_count,
            cumulative_llm_token_count_prompt,
            cumulative_llm_token_count_completion,
            llm_token_count_prompt, llm_token_count_completion
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(span_id) DO NOTHING`,
		traceRowID, span.SpanID, nullString(span.ParentID), span.Name, span.SpanKind,
		formatTime(span.StartTime), formatTime(span.EndTime), cols.attributesJSON, cols.eventsJSON,
		span.StatusCode, span.StatusMessage,
		cumErr, cumTokP, cumTokC,
		cols.tokenPrompt, cols.tokenComp)
	if err != nil {
		return fmt.Errorf("insert span: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Duplicate span: everything above was a no-op for state; the
		// caller commits the transaction.
		return nil
	}

	// Propagate the absorbed cumulative totals to ancestors that
	// landed first. Descendants that arrived before this span had no
	// ancestor to propagate to (their parents were missing), so their
	// contributions reached this span only via the child-sum above and
	// must ride along here; using self totals alone would drop them.
	if err := propagateToAncestors(ctx, tx, traceRowID, span.ParentID, cumErr, cumTokP, cumTokC); err != nil {
		return fmt.Errorf("propagate to ancestors: %w", err)
	}
	return nil
}

// upsertTrace finds the trace by trace_id, expanding its time range,
// or creates it under the given project.
func (s *Store) upsertTrace(ctx context.Context, tx *sql.Tx, span trace.Span, projectRowID int64) (int64, error) {
	var (
		id           int64
		startS, endS string
	)
	err := tx.QueryRowContext(ctx,
		`SELECT id, start_time, end_time FROM traces WHERE trace_id = ?`,
		span.TraceID).Scan(&id, &startS, &endS)
	switch {
	case err == nil:
		// Existing trace: widen the range if the span extends it.
		if span.StartTime.UTC().Format(time.RFC3339Nano) < startS {
			if _, err := tx.ExecContext(ctx,
				`UPDATE traces SET start_time = ? WHERE id = ?`,
				formatTime(span.StartTime), id); err != nil {
				return 0, err
			}
		}
		if span.EndTime.UTC().Format(time.RFC3339Nano) > endS {
			if _, err := tx.ExecContext(ctx,
				`UPDATE traces SET end_time = ? WHERE id = ?`,
				formatTime(span.EndTime), id); err != nil {
				return 0, err
			}
		}
		return id, nil
	case err == sql.ErrNoRows:
		res, err := tx.ExecContext(ctx, `
            INSERT INTO traces (project_rowid, trace_id, session_id, start_time, end_time)
            VALUES (?, ?, ?, ?, ?)`,
			projectRowID, span.TraceID, nullString(span.SessionID),
			formatTime(span.StartTime), formatTime(span.EndTime))
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	default:
		return 0, err
	}
}

// ensureProject returns the rowid for a project, creating it on first
// sight. The in-memory cache covers the common repeat case.
func (s *Store) ensureProject(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	if id, ok := s.projects.get(name); ok {
		return id, nil
	}
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE name = ?`, name).Scan(&id)
	switch {
	case err == nil:
	case err == sql.ErrNoRows:
		now := formatTime(time.Now())
		res, err := tx.ExecContext(ctx, `
            INSERT INTO projects (name, created_at, updated_at) VALUES (?, ?, ?)`,
			name, now, now)
		if err != nil {
			return 0, err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return 0, err
		}
	default:
		return 0, err
	}
	s.projects.put(name, id)
	return id, nil
}

// propagateToAncestors adds the span's cumulative totals to every
// ancestor that was persisted before it. The recursive CTE walks
// parent_id up to the root within the same trace.
func propagateToAncestors(ctx context.Context, tx *sql.Tx, traceRowID int64, parentID string, errCount, tokP, tokC int) error {
	if parentID == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
        WITH RECURSIVE ancestors(rowid) AS (
            SELECT id FROM spans
            WHERE trace_rowid = ? AND span_id = ?
            UNION ALL
            SELECT p.id FROM spans p
            JOIN ancestors a ON p.trace_rowid = ? AND p.span_id = (
                SELECT parent_id FROM spans WHERE id = a.rowid)
        )
        UPDATE spans SET
            cumulative_error_count = cumulative_error_count + ?,
            cumulative_llm_token_count_prompt = cumulative_llm_token_count_prompt + ?,
            cumulative_llm_token_count_completion = cumulative_llm_token_count_completion + ?
        WHERE id IN (SELECT rowid FROM ancestors)`,
		traceRowID, parentID, traceRowID, errCount, tokP, tokC)
	return err
}

// selfTotals returns the span's own contributions to cumulative
// counters: 1 error if ERROR status, LLM tokens only on LLM spans.
func selfTotals(span trace.Span) (errCount, tokP, tokC int) {
	if span.StatusCode == trace.StatusError {
		errCount = 1
	}
	if span.SpanKind == trace.KindLLM && span.TokenUsage != nil {
		tokP = span.TokenUsage.Prompt
		tokC = span.TokenUsage.Completion
	}
	return errCount, tokP, tokC
}

func spanToColumns(span trace.Span) (spanColumns, error) {
	var c spanColumns
	b, err := json.Marshal(span.Attributes)
	if err != nil {
		return c, fmt.Errorf("attributes: %w", err)
	}
	c.attributesJSON = string(b)
	b, err = json.Marshal(span.Events)
	if err != nil {
		return c, fmt.Errorf("events: %w", err)
	}
	c.eventsJSON = string(b)
	if span.SpanKind == trace.KindLLM && span.TokenUsage != nil {
		c.tokenPrompt = sql.NullInt64{Int64: int64(span.TokenUsage.Prompt), Valid: true}
		c.tokenComp = sql.NullInt64{Int64: int64(span.TokenUsage.Completion), Valid: true}
	}
	return c, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullSum(n sql.NullInt64) int {
	if !n.Valid {
		return 0
	}
	return int(n.Int64)
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
