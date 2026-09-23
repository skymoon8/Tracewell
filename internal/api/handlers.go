package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Queries exposes read-side operations over the store's database.
type Queries struct {
	db *sql.DB
}

// NewQueries binds read queries to the given handle.
func NewQueries(db *sql.DB) *Queries { return &Queries{db: db} }

// pageLimit bounds and defaults the limit query parameter, mirroring
// the 1..1000 contract of the read API.
func pageLimit(raw string) (int, error) {
	if raw == "" {
		return 100, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 1000 {
		return 0, errBadParam
	}
	return n, nil
}

// Handler returns the REST query API mounted under /v1/.
func Handler(db *sql.DB) http.Handler {
	q := NewQueries(db)
	mux := http.NewServeMux()
	mux.Handle("GET /v1/projects", projectList(q))
	mux.Handle("GET /v1/projects/{identifier}", projectGet(q))
	mux.Handle("GET /v1/projects/{identifier}/traces", traceList(q))
	mux.Handle("GET /v1/projects/{identifier}/spans", spanList(q))
	mux.Handle("GET /v1/projects/{identifier}/traces/{trace_id}/spans", traceSpanList(q))
	return mux
}

// writeJSON serializes v as JSON, mapping query-layer errors to status
// codes once at the boundary.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write response", "err", err)
	}
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	case errors.Is(err, errBadCursor), errors.Is(err, errBadParam):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
	default:
		slog.Error("query failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
}

// errBadParam marks a malformed query parameter (mapped to 422).
var errBadParam = errors.New("invalid query parameter")

func projectList(q *Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, err := pageLimit(r.URL.Query().Get("limit"))
		if err != nil {
			writeError(w, err)
			return
		}
		cursor, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
		projects, next, err := q.ListProjects(r.Context(), limit, cursor)
		if err != nil {
			writeError(w, err)
			return
		}
		nextCursor := ""
		if next != 0 {
			nextCursor = strconv.FormatInt(projects[len(projects)-1].ID, 10)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": projects, "next_cursor": nextCursor})
	}
}

func projectGet(q *Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := q.FindProject(r.Context(), r.PathValue("identifier"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": p})
	}
}

// parseTimeParam reads an ISO 8601 query parameter, returning nil when
// absent.
func parseTimeParam(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, errBadParam
	}
	return &t, nil
}

func traceList(q *Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		project, err := q.FindProject(r.Context(), r.PathValue("identifier"))
		if err != nil {
			writeError(w, err)
			return
		}
		params := r.URL.Query()
		limit, err := pageLimit(params.Get("limit"))
		if err != nil {
			writeError(w, err)
			return
		}
		start, err := parseTimeParam(params.Get("start_time"))
		if err != nil {
			writeError(w, err)
			return
		}
		end, err := parseTimeParam(params.Get("end_time"))
		if err != nil {
			writeError(w, err)
			return
		}
		f := TraceFilter{
			StartTime: start,
			EndTime:   end,
			Sort:      params.Get("sort"),
			Order:     params.Get("order"),
			Limit:     limit,
			Cursor:    params.Get("cursor"),
		}
		if f.Sort != "" && f.Sort != "start_time" && f.Sort != "latency_ms" {
			writeError(w, errBadParam)
			return
		}
		if f.Order != "" && f.Order != "asc" && f.Order != "desc" {
			writeError(w, errBadParam)
			return
		}
		if raw := params.Get("error"); raw != "" {
			hasErr, err := strconv.ParseBool(raw)
			if err != nil {
				writeError(w, errBadParam)
				return
			}
			f.HasError = &hasErr
		}

		traces, nextCursor, err := q.ListTraces(r.Context(), project.ID, f)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": traces, "next_cursor": nextCursor})
	}
}

// spanListParams maps repeated query parameters onto the filter's
// slice fields.
func spanListParams(params url.Values) SpanFilter {
	f := SpanFilter{
		TraceIDs:    params["trace_id"],
		SpanIDs:     params["span_id"],
		Names:       params["name"],
		SpanKinds:   params["span_kind"],
		StatusCodes: params["status_code"],
	}
	if raw := params.Get("parent_id"); params.Has("parent_id") {
		f.ParentID = &raw
	}
	return f
}

func spanList(q *Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		project, err := q.FindProject(r.Context(), r.PathValue("identifier"))
		if err != nil {
			writeError(w, err)
			return
		}
		params := r.URL.Query()
		limit, err := pageLimit(params.Get("limit"))
		if err != nil {
			writeError(w, err)
			return
		}
		f := spanListParams(params)
		f.Limit = limit
		if raw := params.Get("cursor"); raw != "" {
			f.Cursor, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || f.Cursor < 0 {
				writeError(w, errBadCursor)
				return
			}
		}

		spans, next, err := q.ListSpans(r.Context(), project.ID, f)
		if err != nil {
			writeError(w, err)
			return
		}
		nextCursor := ""
		if next != 0 {
			nextCursor = strconv.FormatInt(next, 10)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": spans, "next_cursor": nextCursor})
	}
}

func traceSpanList(q *Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		project, err := q.FindProject(r.Context(), r.PathValue("identifier"))
		if err != nil {
			writeError(w, err)
			return
		}
		spans, err := q.ListTraceSpans(r.Context(), project.ID, r.PathValue("trace_id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": spans})
	}
}
