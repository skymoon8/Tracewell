package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// Span is a read model for one persisted span.
type Span struct {
	ID            int64            `json:"id"`
	TraceID       string           `json:"trace_id"`
	SpanID        string           `json:"span_id"`
	ParentID      *string          `json:"parent_id"`
	Name          string           `json:"name"`
	SpanKind      string           `json:"span_kind"`
	StartTime     string           `json:"start_time"`
	EndTime       string           `json:"end_time"`
	StatusCode    string           `json:"status_code"`
	StatusMessage string           `json:"status_message"`
	LatencyMs     float64          `json:"latency_ms"`
	CumError      int              `json:"cumulative_error_count"`
	CumTokP       int              `json:"cumulative_llm_token_count_prompt"`
	CumTokC       int              `json:"cumulative_llm_token_count_completion"`
	SelfTokP      *int             `json:"llm_token_count_prompt,omitempty"`
	SelfTokC      *int             `json:"llm_token_count_completion,omitempty"`
	Attributes    map[string]any   `json:"attributes"`
	Events        []map[string]any `json:"events,omitempty"`
}

// SpanFilter narrows a project's span listing. Slice fields map to
// repeated query parameters (key appearing multiple times).
type SpanFilter struct {
	TraceIDs    []string
	SpanIDs     []string
	ParentID    *string // "null" (literal) selects root spans
	Names       []string
	SpanKinds   []string
	StatusCodes []string
	Limit       int
	Cursor      int64 // rowid of the first row of the next page; 0 = start
}

// spanLatency computes latency inline with the `s.` alias.
const spanLatency = "(julianday(s.end_time) - julianday(s.start_time)) * 86400000.0"

// ListSpans returns one page of a project's spans, newest first,
// joined with traces to expose the trace id string.
func (q *Queries) ListSpans(ctx context.Context, projectRowID int64, f SpanFilter) ([]Span, int64, error) {
	var args []any
	where := "t.project_rowid = ?"
	args = append(args, projectRowID)

	if len(f.TraceIDs) > 0 {
		where += " AND t.trace_id IN (" + makePlaceholders(len(f.TraceIDs)) + ")"
		args = appendArgs(args, f.TraceIDs)
	}
	if len(f.SpanIDs) > 0 {
		where += " AND s.span_id IN (" + makePlaceholders(len(f.SpanIDs)) + ")"
		args = appendArgs(args, f.SpanIDs)
	}
	if f.ParentID != nil {
		if *f.ParentID == "null" {
			where += " AND s.parent_id IS NULL"
		} else {
			where += " AND s.parent_id = ?"
			args = append(args, *f.ParentID)
		}
	}
	if len(f.Names) > 0 {
		where += " AND s.name IN (" + makePlaceholders(len(f.Names)) + ")"
		args = appendArgs(args, f.Names)
	}
	if len(f.SpanKinds) > 0 {
		where += " AND s.span_kind IN (" + makePlaceholders(len(f.SpanKinds)) + ")"
		args = appendArgs(args, f.SpanKinds)
	}
	if len(f.StatusCodes) > 0 {
		where += " AND s.status_code IN (" + makePlaceholders(len(f.StatusCodes)) + ")"
		args = appendArgs(args, f.StatusCodes)
	}
	if f.Cursor != 0 {
		where += " AND s.id <= ?"
		args = append(args, f.Cursor)
	}

	rows, err := q.db.QueryContext(ctx, `
        SELECT s.id, t.trace_id, s.span_id, s.parent_id, s.name, s.span_kind,
               s.start_time, s.end_time, s.status_code, s.status_message,
               `+spanLatency+`,
               s.cumulative_error_count,
               s.cumulative_llm_token_count_prompt,
               s.cumulative_llm_token_count_completion,
               s.llm_token_count_prompt, s.llm_token_count_completion
        FROM spans s
        JOIN traces t ON t.id = s.trace_rowid
        WHERE `+where+`
        ORDER BY s.id DESC
        LIMIT ?`, append(args, f.Limit+1)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	spans, next, err := scanPage(rows, f.Limit, func(r rowScanner) (Span, error) { return scanSpan(r, false) })
	if err != nil || next == 0 {
		return spans, 0, err
	}
	return spans, spans[len(spans)-1].ID, nil
}

// ListTraceSpans returns all spans of one trace ordered by start time,
// the payload a waterfall view needs. One trace has tens of spans in
// the common case, so pagination is unnecessary here.
func (q *Queries) ListTraceSpans(ctx context.Context, projectRowID int64, traceID string) ([]Span, error) {
	rows, err := q.db.QueryContext(ctx, `
        SELECT s.id, t.trace_id, s.span_id, s.parent_id, s.name, s.span_kind,
               s.start_time, s.end_time, s.status_code, s.status_message,
               `+spanLatency+`,
               s.cumulative_error_count,
               s.cumulative_llm_token_count_prompt,
               s.cumulative_llm_token_count_completion,
               s.llm_token_count_prompt, s.llm_token_count_completion,
               s.attributes, s.events
        FROM spans s
        JOIN traces t ON t.id = s.trace_rowid
        WHERE t.project_rowid = ? AND t.trace_id = ?
        ORDER BY s.start_time ASC, s.id ASC`,
		projectRowID, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Span
	for rows.Next() {
		sp, err := scanSpan(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// scanSpan reads one span row. WithJSON additionally loads the
// attributes and events columns; the list path skips those potentially
// large blobs unless the caller asks for them.
func scanSpan(r rowScanner, withJSON bool) (Span, error) {
	var (
		sp         Span
		parentID   sql.NullString
		tokP, tokC sql.NullInt64
	)
	dest := []any{
		&sp.ID, &sp.TraceID, &sp.SpanID, &parentID, &sp.Name, &sp.SpanKind,
		&sp.StartTime, &sp.EndTime, &sp.StatusCode, &sp.StatusMessage,
		&sp.LatencyMs,
		&sp.CumError, &sp.CumTokP, &sp.CumTokC,
		&tokP, &tokC,
	}
	if withJSON {
		var attrsJSON, eventsJSON string
		dest = append(dest, &attrsJSON, &eventsJSON)
		if err := r.Scan(dest...); err != nil {
			return Span{}, err
		}
		var err error
		if sp.Attributes, err = unmarshalObject(attrsJSON); err != nil {
			return Span{}, fmt.Errorf("span %s attributes: %w", sp.SpanID, err)
		}
		if sp.Events, err = unmarshalArray(eventsJSON); err != nil {
			return Span{}, fmt.Errorf("span %s events: %w", sp.SpanID, err)
		}
	} else {
		if err := r.Scan(dest...); err != nil {
			return Span{}, err
		}
	}
	if parentID.Valid {
		v := parentID.String
		sp.ParentID = &v
	}
	if tokP.Valid {
		v := int(tokP.Int64)
		sp.SelfTokP = &v
	}
	if tokC.Valid {
		v := int(tokC.Int64)
		sp.SelfTokC = &v
	}
	return sp, nil
}

func appendArgs(args []any, vals []string) []any {
	for _, v := range vals {
		args = append(args, v)
	}
	return args
}

// unmarshalObject decodes a JSON object column, tolerating empty input.
func unmarshalObject(s string) (map[string]any, error) {
	if strings.TrimSpace(s) == "" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// unmarshalArray decodes a JSON array column, tolerating empty input.
func unmarshalArray(s string) ([]map[string]any, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var a []map[string]any
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		return nil, err
	}
	return a, nil
}
