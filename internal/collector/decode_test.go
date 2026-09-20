package collector_test

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb2 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/skymoon8/tracewell/internal/collector"
	"github.com/skymoon8/tracewell/internal/trace"
)

// buildRequest builds an OTLP export request containing one LLM span
// with a variety of attribute value types, as instrumentation
// libraries would emit them.
func buildRequest(t *testing.T) *tracepb.ExportTraceServiceRequest {
	t.Helper()
	kv := func(k string, v *commonpb.AnyValue) *commonpb.KeyValue {
		return &commonpb.KeyValue{Key: k, Value: v}
	}
	str := func(s string) *commonpb.AnyValue {
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: s}}
	}
	req := &tracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb2.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					kv("service.name", str("test-app")),
				},
			},
			ScopeSpans: []*tracepb2.ScopeSpans{{
				Spans: []*tracepb2.Span{{
					TraceId:           []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
					SpanId:            []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x00, 0x11},
					Name:              "chat completion",
					Kind:              tracepb2.Span_SPAN_KIND_INTERNAL,
					StartTimeUnixNano: 1_700_000_000_000_000_000,
					EndTimeUnixNano:   1_700_000_000_500_000_000,
					Attributes: []*commonpb.KeyValue{
						kv("openinference.span.kind", str("LLM")),
						kv("input.value", str("What is the capital of France?")),
						kv("llm.token_count.prompt", &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 9}}),
						kv("llm.token_count.completion", &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: 7}}),
						kv("retrieval.documents", &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{
							ArrayValue: &commonpb.ArrayValue{Values: []*commonpb.AnyValue{str("doc-1"), str("doc-2")}},
						}}),
						kv("metadata", &commonpb.AnyValue{Value: &commonpb.AnyValue_KvlistValue{
							KvlistValue: &commonpb.KeyValueList{Values: []*commonpb.KeyValue{
								kv("env", str("dev")),
							}},
						}}),
					},
					Status: &tracepb2.Status{Code: tracepb2.Status_STATUS_CODE_ERROR, Message: "boom"},
					Events: []*tracepb2.Span_Event{{
						Name:         "exception",
						TimeUnixNano: 1_700_000_000_250_000_000,
						Attributes: []*commonpb.KeyValue{
							kv("exception.message", str("rate limited")),
						},
					}},
				}},
			}},
		}},
	}
	return req
}

func TestDecodeSpans(t *testing.T) {
	spans := collector.DecodeSpans(buildRequest(t))
	if len(spans) != 1 {
		t.Fatalf("span count = %d, want 1", len(spans))
	}
	s := spans[0]

	if s.Name != "chat completion" {
		t.Errorf("name = %q", s.Name)
	}
	// 16-byte-ish trace id is hex-encoded; here 8 bytes -> 16 hex chars.
	if s.TraceID != "0102030405060708" {
		t.Errorf("trace id = %q", s.TraceID)
	}
	if s.SpanID != "aabbccddeeff0011" {
		t.Errorf("span id = %q", s.SpanID)
	}
	if s.ParentID != "" {
		t.Errorf("parent id = %q, want empty for root span", s.ParentID)
	}
	if s.SpanKind != trace.KindLLM {
		t.Errorf("span kind = %q, want %q", s.SpanKind, trace.KindLLM)
	}
	if s.StatusCode != trace.StatusError || s.StatusMessage != "boom" {
		t.Errorf("status = %q/%q, want error/boom", s.StatusCode, s.StatusMessage)
	}
	if want := time.Unix(0, 1_700_000_000_000_000_000).UTC(); !s.StartTime.Equal(want) {
		t.Errorf("start time = %v, want %v", s.StartTime, want)
	}
	if want := time.Unix(0, 1_700_000_000_500_000_000).UTC(); !s.EndTime.Equal(want) {
		t.Errorf("end time = %v, want %v", s.EndTime, want)
	}

	// Attributes materialize as a nested structure: dotted keys expand
	// into maps, and convention values survive with their types.
	llm, ok := s.Attributes["llm"].(map[string]any)
	if !ok {
		t.Fatalf("llm attributes = %#v", s.Attributes["llm"])
	}
	tc, ok := llm["token_count"].(map[string]any)
	if !ok {
		t.Fatalf("llm.token_count = %#v", llm["token_count"])
	}
	if tc["prompt"] != int64(9) {
		t.Errorf("token count prompt = %v (%T)", tc["prompt"], tc["prompt"])
	}
	if tc["completion"] != float64(7) {
		t.Errorf("token count completion = %v (%T)", tc["completion"], tc["completion"])
	}
	in, ok := s.Attributes["input"].(map[string]any)
	if !ok || in["value"] != "What is the capital of France?" {
		t.Errorf("input = %#v", s.Attributes["input"])
	}
	if docs, ok := s.Attributes["retrieval"].(map[string]any)["documents"].([]any); !ok || len(docs) != 2 {
		t.Errorf("retrieval.documents = %#v", s.Attributes["retrieval"])
	}
	if meta, ok := s.Attributes["metadata"].(map[string]any); !ok || meta["env"] != "dev" {
		t.Errorf("metadata = %#v", s.Attributes["metadata"])
	}

	// Events decode with their attributes.
	if len(s.Events) != 1 {
		t.Fatalf("event count = %d, want 1", len(s.Events))
	}
	if s.Events[0].Name != "exception" || s.Events[0].Attributes["exception.message"] != "rate limited" {
		t.Errorf("event = %+v", s.Events[0])
	}
}

func TestSpanKindFromAttribute(t *testing.T) {
	cases := []struct {
		in   any
		want trace.SpanKind
	}{
		{"llm", trace.KindLLM},     // lower case tolerated
		{"Chain", trace.KindChain}, // mixed case tolerated
		{"AGENT", trace.KindAgent},
		{"", trace.KindUnknown},
		{"bogus", trace.KindUnknown},
		{42, trace.KindUnknown}, // non-string tolerated
		{nil, trace.KindUnknown},
	}
	for _, c := range cases {
		if got := trace.SpanKindFromAttribute(c.in); got != c.want {
			t.Errorf("SpanKindFromAttribute(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHandler(t *testing.T) {
	newReq := func(t *testing.T, body []byte) *http.Request {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/x-protobuf")
		return req
	}

	t.Run("ok", func(t *testing.T) {
		body, err := proto.Marshal(buildRequest(t))
		if err != nil {
			t.Fatal(err)
		}
		var got []trace.Span
		rr := httptest.NewRecorder()
		collector.Handler(func(s []trace.Span) { got = s }).ServeHTTP(rr, newReq(t, body))

		if rr.Code != http.StatusOK {
			t.Fatalf("code = %d, body %q", rr.Code, rr.Body.String())
		}
		if ct := rr.Header().Get("Content-Type"); ct != "application/x-protobuf" {
			t.Errorf("content type = %q", ct)
		}
		if len(got) != 1 {
			t.Errorf("decoded span count = %d, want 1", len(got))
		}
	})

	t.Run("gzip encoded body", func(t *testing.T) {
		body, err := proto.Marshal(buildRequest(t))
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		if _, err := gz.Write(body); err != nil {
			t.Fatal(err)
		}
		gz.Close()

		var got []trace.Span
		rr := httptest.NewRecorder()
		req := newReq(t, buf.Bytes())
		req.Header.Set("Content-Encoding", "gzip")
		collector.Handler(func(s []trace.Span) { got = s }).ServeHTTP(rr, req)

		if rr.Code != http.StatusOK || len(got) != 1 {
			t.Fatalf("code = %d, spans = %d, body %q", rr.Code, len(got), rr.Body.String())
		}
	})

	t.Run("wrong content type is 415", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(nil))
		req.Header.Set("Content-Type", "application/json")
		collector.Handler(nil).ServeHTTP(rr, req)
		if rr.Code != http.StatusUnsupportedMediaType {
			t.Errorf("code = %d, want 415", rr.Code)
		}
	})

	t.Run("invalid protobuf is 422", func(t *testing.T) {
		rr := httptest.NewRecorder()
		collector.Handler(nil).ServeHTTP(rr, newReq(t, []byte("not a protobuf")))
		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("code = %d, want 422", rr.Code)
		}
	})

	t.Run("unsupported encoding is 415", func(t *testing.T) {
		body, _ := proto.Marshal(buildRequest(t))
		rr := httptest.NewRecorder()
		req := newReq(t, body)
		req.Header.Set("Content-Encoding", "br")
		collector.Handler(nil).ServeHTTP(rr, req)
		if rr.Code != http.StatusUnsupportedMediaType {
			t.Errorf("code = %d, want 415", rr.Code)
		}
	})

	t.Run("get method is 405", func(t *testing.T) {
		rr := httptest.NewRecorder()
		collector.Handler(nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/traces", nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("code = %d, want 405", rr.Code)
		}
	})
}
