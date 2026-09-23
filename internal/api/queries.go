// Package api implements the read-side REST query API.
package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Project is a read model for the projects table.
type Project struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Trace is a read model for one trace with derived aggregates.
type Trace struct {
	ID              int64     `json:"id"`
	TraceID         string    `json:"trace_id"`
	SessionID       *string   `json:"session_id,omitempty"`
	StartTime       time.Time `json:"start_time"`
	EndTime         time.Time `json:"end_time"`
	LatencyMs       float64   `json:"latency_ms"`
	TokenPrompt     int       `json:"token_count_prompt"`
	TokenCompletion int       `json:"token_count_completion"`
	TokenTotal      int       `json:"token_count_total"`
	HasError        bool      `json:"has_error"`
}

// ListProjects returns projects ordered newest-first. The returned
// nextCursor is 0 when the list is exhausted.
func (q *Queries) ListProjects(ctx context.Context, limit int, cursor int64) ([]Project, int64, error) {
	rows, err := q.db.QueryContext(ctx, `
        SELECT id, name, created_at, updated_at
        FROM projects
        WHERE (? = 0 OR id <= ?)
        ORDER BY id DESC
        LIMIT ?`,
		cursor, cursor, limit+1)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	projects, next, err := scanPage(rows, limit, func(r rowScanner) (Project, error) {
		var p Project
		var created, updated string
		if err := r.Scan(&p.ID, &p.Name, &created, &updated); err != nil {
			return Project{}, err
		}
		if p.CreatedAt, err = parseTime(created); err != nil {
			return Project{}, fmt.Errorf("project created_at: %w", err)
		}
		p.UpdatedAt, _ = parseTime(updated)
		return p, nil
	})
	return projects, next, err
}

// FindProject resolves a project by numeric rowid or by name. The
// dual lookup mirrors client behavior: exporters reference projects
// by name, tools reference them by id.
func (q *Queries) FindProject(ctx context.Context, identifier string) (Project, error) {
	var p Project
	var created, updated string
	scan := func(err error) (Project, error) {
		if errors.Is(err, sql.ErrNoRows) {
			return Project{}, errNotFound
		}
		if err != nil {
			return Project{}, err
		}
		var perr error
		if p.CreatedAt, perr = parseTime(created); perr != nil {
			return Project{}, perr
		}
		p.UpdatedAt, _ = parseTime(updated)
		return p, nil
	}
	if id, err := strconv.ParseInt(identifier, 10, 64); err == nil {
		err := q.db.QueryRowContext(ctx, `
            SELECT id, name, created_at, updated_at FROM projects WHERE id = ?`, id).
			Scan(&p.ID, &p.Name, &created, &updated)
		return scan(err)
	}
	err := q.db.QueryRowContext(ctx, `
        SELECT id, name, created_at, updated_at FROM projects WHERE name = ?`, identifier).
		Scan(&p.ID, &p.Name, &created, &updated)
	return scan(err)
}

// TraceFilter narrows a project's trace listing.
type TraceFilter struct {
	StartTime *time.Time // inclusive lower bound on trace start
	EndTime   *time.Time // exclusive upper bound on trace start
	Sort      string     // "start_time" or "latency_ms"
	Order     string     // "asc" or "desc"
	HasError  *bool      // nil = no filter
	Limit     int
	Cursor    string // opaque "sortValue:rowid" or empty for first page
}

// traceCursor is the pagination cursor for traces. It carries the
// sort value plus the rowid so pages are stable when many traces
// share the same sort value.
type traceCursor struct {
	SortValue string
	RowID     int64
}

func encodeTraceCursor(c traceCursor) string {
	return c.SortValue + ":" + strconv.FormatInt(c.RowID, 10)
}

// decodeTraceCursor splits at the last colon: RFC3339 timestamps and
// float latencies never contain one, so a plain Rfind is safe.
func decodeTraceCursor(s string) (string, int64, error) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return "", 0, errBadCursor
	}
	rowid, err := strconv.ParseInt(s[i+1:], 10, 64)
	if err != nil {
		return "", 0, errBadCursor
	}
	return s[:i], rowid, nil
}

func formatTimeArg(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// latencyExpr computes trace duration in milliseconds from the
// RFC3339 text timestamps. julianday() parses ISO strings natively and
// the difference of two julianday values is fractional days. The
// trace alias `t.` is required so the expression is unambiguous in
// ORDER BY when spans are joined in subqueries.
const latencyExpr = "(julianday(t.end_time) - julianday(t.start_time)) * 86400000.0"

// ListTraces returns one page of a project's traces with per-trace
// token aggregates. Aggregates are fetched for exactly the page's
// trace ids (one grouped query), not joined into the page query, so
// the page query stays a flat index scan.
//
// Keyset pagination compares (sort value, id) tuples so pages remain
// stable when many traces share a sort value: the cursor carries the
// last row's sort value and rowid.
func (q *Queries) ListTraces(ctx context.Context, projectRowID int64, f TraceFilter) ([]Trace, string, error) {
	var sortExpr string
	switch f.Sort {
	case "latency_ms":
		sortExpr = latencyExpr
	default:
		sortExpr = "t.start_time" // RFC3339Nano UTC text sorts chronologically
	}
	// cmp drives both the keyset comparison and the ORDER BY direction:
	// desc sorts and compares downward, asc upward.
	cmp := "<"
	orderDir := "DESC"
	if f.Order == "asc" {
		cmp = ">"
		orderDir = "ASC"
	}

	var args []any
	where := "t.project_rowid = ?"
	args = append(args, projectRowID)
	if f.StartTime != nil {
		where += " AND t.start_time >= ?"
		args = append(args, formatTimeArg(*f.StartTime))
	}
	if f.EndTime != nil {
		where += " AND t.start_time < ?"
		args = append(args, formatTimeArg(*f.EndTime))
	}
	if f.HasError != nil {
		if *f.HasError {
			where += " AND EXISTS(SELECT 1 FROM spans s WHERE s.trace_rowid = t.id AND s.status_code = 'ERROR')"
		} else {
			where += " AND NOT EXISTS(SELECT 1 FROM spans s WHERE s.trace_rowid = t.id AND s.status_code = 'ERROR')"
		}
	}
	if f.Cursor != "" {
		val, row, err := decodeTraceCursor(f.Cursor)
		if err != nil {
			return nil, "", errBadCursor
		}
		where += " AND (" + sortExpr + " " + cmp + " ? OR (" + sortExpr + " = ? AND t.id " + cmp + " ?))"
		args = append(args, val, val, row)
	}

	rows, err := q.db.QueryContext(ctx, `
        SELECT t.id, t.trace_id, t.session_id, t.start_time, t.end_time,
               `+latencyExpr+`,
               EXISTS(SELECT 1 FROM spans s
                      WHERE s.trace_rowid = t.id AND s.status_code = 'ERROR')
        FROM traces t
        WHERE `+where+`
        ORDER BY `+sortExpr+` `+orderDir+`, t.id `+orderDir+`
        LIMIT ?`, append(args, f.Limit+1)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	traces, next, err := scanPage(rows, f.Limit, func(r rowScanner) (Trace, error) {
		var (
			t            Trace
			session      sql.NullString
			startS, endS string
			latency      float64
			hasErr       bool
		)
		if err := r.Scan(&t.ID, &t.TraceID, &session, &startS, &endS, &latency, &hasErr); err != nil {
			return Trace{}, err
		}
		if t.StartTime, err = parseTime(startS); err != nil {
			return Trace{}, fmt.Errorf("trace start_time: %w", err)
		}
		if t.EndTime, err = parseTime(endS); err != nil {
			return Trace{}, fmt.Errorf("trace end_time: %w", err)
		}
		if session.Valid {
			t.SessionID = &session.String
		}
		t.LatencyMs = latency
		t.HasError = hasErr
		return t, nil
	})
	if err != nil || len(traces) == 0 {
		return traces, "", err
	}

	// Fill token totals for exactly this page in one grouped query.
	if err := q.fillTokenCounts(ctx, traces); err != nil {
		return nil, "", err
	}

	if next != 0 {
		last := traces[len(traces)-1]
		sortVal := last.StartTime.UTC().Format(time.RFC3339Nano)
		if f.Sort == "latency_ms" {
			sortVal = strconv.FormatFloat(last.LatencyMs, 'f', -1, 64)
		}
		return traces, encodeTraceCursor(traceCursor{SortValue: sortVal, RowID: last.ID}), nil
	}
	return traces, "", nil
}

// fillTokenCounts sums the self token columns of LLM spans per trace.
// LLM spans carry their own usage; summing self values across the
// trace gives its total LLM consumption regardless of tree shape.
func (q *Queries) fillTokenCounts(ctx context.Context, traces []Trace) error {
	ids := make([]any, len(traces))
	for i, t := range traces {
		ids[i] = t.ID
	}
	qmarks := makePlaceholders(len(ids))
	rows, err := q.db.QueryContext(ctx, `
        SELECT trace_rowid,
               SUM(COALESCE(llm_token_count_prompt, 0)),
               SUM(COALESCE(llm_token_count_completion, 0))
        FROM spans
        WHERE span_kind = 'LLM' AND trace_rowid IN (`+qmarks+`)
        GROUP BY trace_rowid`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()

	byTrace := map[int64][2]int{}
	for rows.Next() {
		var id, p, c int64
		if err := rows.Scan(&id, &p, &c); err != nil {
			return err
		}
		byTrace[id] = [2]int{int(p), int(c)}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range traces {
		if tc, ok := byTrace[traces[i].ID]; ok {
			traces[i].TokenPrompt = tc[0]
			traces[i].TokenCompletion = tc[1]
			traces[i].TokenTotal = tc[0] + tc[1]
		}
	}
	return nil
}
