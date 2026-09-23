package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/skymoon8/tracewell/internal/api"
	"github.com/skymoon8/tracewell/internal/store"
	"github.com/skymoon8/tracewell/internal/trace"
)

// newAPIStore opens a temp-DB store and returns it with the API mux.
func newAPIStore(t *testing.T) (*store.Store, http.Handler) {
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
	return s, api.Handler(db)
}

func mkSpan(id, parent, traceID string, kind trace.SpanKind, status trace.StatusCode, p, c int, start, end time.Time) trace.Span {
	s := trace.Span{
		Name:       "span-" + id,
		TraceID:    traceID,
		SpanID:     id,
		ParentID:   parent,
		StartTime:  start,
		EndTime:    end,
		SpanKind:   kind,
		StatusCode: status,
		Attributes: map[string]any{},
	}
	if kind == trace.KindLLM {
		s.TokenUsage = &trace.TokenUsage{Prompt: p, Completion: c, Total: p + c}
	}
	return s
}

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// seedTwoTraces inserts: trace A (root+LLM child, error), trace B (root LLM, ok).
func seedTwoTraces(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	must(t, s.InsertSpan(ctx, mkSpan("a-root", "", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", trace.KindChain, trace.StatusError, 0, 0, t0, t0.Add(2*time.Second)), "proj-a"))
	must(t, s.InsertSpan(ctx, mkSpan("a-llm", "a-root", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", trace.KindLLM, trace.StatusOK, 10, 5, t0, t0.Add(time.Second)), "proj-a"))
	must(t, s.InsertSpan(ctx, mkSpan("b-root", "", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", trace.KindLLM, trace.StatusOK, 7, 3, t0.Add(10*time.Second), t0.Add(11*time.Second)), "proj-a"))
}

func getJSON(t *testing.T, h http.Handler, path string) (int, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	if rr.Code != 200 {
		return rr.Code, nil
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return rr.Code, body
}

func TestListProjects(t *testing.T) {
	s, h := newAPIStore(t)
	must(t, s.InsertSpan(context.Background(), mkSpan("x", "", "cccccccccccccccccccccccccccccccc", trace.KindChain, trace.StatusOK, 0, 0, t0, t0), "proj-a"))
	must(t, s.InsertSpan(context.Background(), mkSpan("y", "", "dddddddddddddddddddddddddddddddd", trace.KindChain, trace.StatusOK, 0, 0, t0, t0), "proj-b"))

	code, body := getJSON(t, h, "/v1/projects")
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	data := body["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("projects = %d, want 2", len(data))
	}
	// Newest first: proj-b was inserted second.
	if data[0].(map[string]any)["name"] != "proj-b" {
		t.Errorf("first project = %v, want proj-b", data[0])
	}
}

func TestFindProjectByIDOrName(t *testing.T) {
	s, h := newAPIStore(t)
	must(t, s.InsertSpan(context.Background(), mkSpan("x", "", "cccccccccccccccccccccccccccccccc", trace.KindChain, trace.StatusOK, 0, 0, t0, t0), "proj-a"))

	// By name.
	code, body := getJSON(t, h, "/v1/projects/proj-a")
	if code != 200 || body["data"].(map[string]any)["name"] != "proj-a" {
		t.Fatalf("by name: code=%d body=%v", code, body)
	}

	// By rowid.
	id := int64(body["data"].(map[string]any)["id"].(float64))
	code, body = getJSON(t, h, fmt.Sprintf("/v1/projects/%d", id))
	if code != 200 || body["data"].(map[string]any)["name"] != "proj-a" {
		t.Fatalf("by id: code=%d body=%v", code, body)
	}

	// Unknown -> 404.
	code, _ = getJSON(t, h, "/v1/projects/nope")
	if code != 404 {
		t.Errorf("unknown project code = %d, want 404", code)
	}
}

func TestListTraces(t *testing.T) {
	s, h := newAPIStore(t)
	seedTwoTraces(t, s)

	code, body := getJSON(t, h, "/v1/projects/proj-a/traces")
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	data := body["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("traces = %d, want 2", len(data))
	}
	first := data[0].(map[string]any)
	// Default order: start_time desc -> trace B first.
	if first["trace_id"] != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("first trace = %v", first["trace_id"])
	}
	// Token aggregation from LLM spans: trace B has prompt 7, completion 3.
	if first["token_count_prompt"].(float64) != 7 || first["token_count_completion"].(float64) != 3 {
		t.Errorf("token counts = %v", first)
	}
	// Latency: trace B spans 1s.
	if got := first["latency_ms"].(float64); got < 999 || got > 1001 {
		t.Errorf("latency_ms = %v, want ~1000", got)
	}

	// error=true filters to trace A only.
	code, body = getJSON(t, h, "/v1/projects/proj-a/traces?error=true")
	data = body["data"].([]any)
	if code != 200 || len(data) != 1 || data[0].(map[string]any)["trace_id"] != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("error=true: code=%d data=%v", code, data)
	}
}

func TestListTracesPagination(t *testing.T) {
	s, h := newAPIStore(t)
	seedTwoTraces(t, s)

	// Page 1: only one trace (desc order -> B).
	code, body := getJSON(t, h, "/v1/projects/proj-a/traces?limit=1")
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	data := body["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("page1 = %d rows, want 1", len(data))
	}
	next := body["next_cursor"].(string)
	if next == "" {
		t.Fatal("page1 next_cursor empty, want a cursor")
	}

	// Page 2: the other trace, no further cursor.
	code, body = getJSON(t, h, "/v1/projects/proj-a/traces?limit=1&cursor="+next)
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	data = body["data"].([]any)
	if len(data) != 1 || data[0].(map[string]any)["trace_id"] != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("page2 = %v", data)
	}
	if body["next_cursor"].(string) != "" {
		t.Errorf("page2 next_cursor = %q, want empty", body["next_cursor"])
	}

	// Invalid cursor -> 422.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/projects/proj-a/traces?cursor=bogus", nil))
	if rr.Code != 422 {
		t.Errorf("bad cursor code = %d, want 422", rr.Code)
	}
}

func TestListSpansFilters(t *testing.T) {
	s, h := newAPIStore(t)
	seedTwoTraces(t, s)

	// All spans: a-root, a-llm, b-root -> 3 rows, newest id first.
	code, body := getJSON(t, h, "/v1/projects/proj-a/spans")
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	data := body["data"].([]any)
	if len(data) != 3 {
		t.Fatalf("spans = %d, want 3", len(data))
	}

	// Root spans only.
	code, body = getJSON(t, h, "/v1/projects/proj-a/spans?parent_id=null")
	data = body["data"].([]any)
	if code != 200 || len(data) != 2 {
		t.Fatalf("root spans = %d, want 2 (code %d)", len(data), code)
	}

	// Filter by kind.
	code, body = getJSON(t, h, "/v1/projects/proj-a/spans?span_kind=LLM")
	data = body["data"].([]any)
	if code != 200 || len(data) != 2 {
		t.Fatalf("LLM spans = %d, want 2", len(data))
	}

	// Filter by trace id and span id together.
	code, body = getJSON(t, h, "/v1/projects/proj-a/spans?trace_id=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&span_id=a-llm")
	data = body["data"].([]any)
	if code != 200 || len(data) != 1 {
		t.Fatalf("filtered spans = %d, want 1", len(data))
	}
	row := data[0].(map[string]any)
	if row["span_kind"] != "LLM" || row["trace_id"] != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("span row = %v", row)
	}
	// Self token columns present on the LLM span.
	if row["llm_token_count_prompt"].(float64) != 10 {
		t.Errorf("self prompt tokens = %v", row["llm_token_count_prompt"])
	}

	// Unknown project -> 404.
	code, _ = getJSON(t, h, "/v1/projects/nope/spans")
	if code != 404 {
		t.Errorf("unknown project code = %d, want 404", code)
	}
}

func TestListTraceSpans(t *testing.T) {
	s, h := newAPIStore(t)
	seedTwoTraces(t, s)

	code, body := getJSON(t, h, "/v1/projects/proj-a/traces/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/spans")
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	data := body["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("trace spans = %d, want 2", len(data))
	}

	// Ordered by start time: the root span (t0) precedes the LLM child
	// (also t0; tie broken by rowid, root inserted first).
	first := data[0].(map[string]any)
	if first["span_id"] != "a-root" {
		t.Errorf("first span = %v, want a-root", first["span_id"])
	}
	// Full detail path includes attributes; the store writes at least {}.
	if _, ok := first["attributes"].(map[string]any); !ok {
		t.Errorf("attributes missing on span row: %v", first)
	}

	// Empty trace -> 200 with no rows (Go marshals a nil slice as null).
	code, body = getJSON(t, h, "/v1/projects/proj-a/traces/ffffffffffffffffffffffffffffffff/spans")
	if code != 200 || body["data"] != nil {
		t.Errorf("unknown trace: code=%d body=%v", code, body)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
