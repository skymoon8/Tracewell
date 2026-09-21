package collector

import (
	"encoding/hex"
	"time"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/skymoon8/tracewell/internal/attrs"
	"github.com/skymoon8/tracewell/internal/semconv"
	"github.com/skymoon8/tracewell/internal/trace"
)

// spanKindAttr is the resource attribute key instrumentation libraries
// use to declare what kind of work a span represents.
const spanKindAttr = "openinference.span.kind"

// projectAttr is the resource attribute key naming the destination
// project for a batch of spans.
const projectAttr = "openinference.project.name"

// defaultProject receives spans whose resource carries no project name.
const defaultProject = "default"

// SpanWithProject pairs a decoded span with the project its resource
// declared. Spans from different resources in one export may target
// different projects.
type SpanWithProject struct {
	Project string
	Span    trace.Span
}

// DecodeSpans converts the spans of an OTLP export request into
// Tracewell's internal model. Malformed attribute values are skipped
// rather than rejected: observability data must never be dropped
// wholesale because one field is unexpected.
//
// Attributes are ingested through the standard pipeline: JSON-string
// conventions are parsed, then flat dotted keys are expanded into
// nested maps and arrays. Each span is tagged with the project name
// declared by its resource, falling back to the default project.
func DecodeSpans(req *collectorpb.ExportTraceServiceRequest) []SpanWithProject {
	var out []SpanWithProject
	for _, rs := range req.GetResourceSpans() {
		project := projectName(rs.GetResource().GetAttributes())
		for _, ss := range rs.GetScopeSpans() {
			for _, s := range ss.GetSpans() {
				out = append(out, SpanWithProject{
					Project: project,
					Span:    decodeSpan(s),
				})
			}
		}
	}
	return out
}

// projectName extracts the project name from resource attributes,
// defaulting when the attribute is absent or empty.
func projectName(kvs []*commonpb.KeyValue) string {
	for _, kv := range kvs {
		if kv.GetKey() == projectAttr {
			if s := kv.GetValue().GetStringValue(); s != "" {
				return s
			}
		}
	}
	return defaultProject
}

func decodeSpan(s *tracepb.Span) trace.Span {
	flat := decodeAttributes(s.GetAttributes())
	pairs := make([]attrs.KV, 0, len(flat))
	for k, v := range flat {
		pairs = append(pairs, attrs.KV{Key: k, Value: v})
	}
	// Span kind is classified from the flat attribute map before
	// expansion: the flat view is the authoritative protocol shape.
	nested := attrs.Unflatten(attrs.LoadJSONStrings(pairs))
	ex := semconv.Merge(nested, flat)
	span := trace.Span{
		Name:     s.GetName(),
		TraceID:  hex.EncodeToString(s.GetTraceId()),
		SpanID:   hex.EncodeToString(s.GetSpanId()),
		ParentID: hex.EncodeToString(s.GetParentSpanId()),
		// OTLP timestamps are uint64 nanoseconds since the epoch;
		// time.Unix(0, n) keeps full precision with no float rounding.
		StartTime:     unixNano(s.GetStartTimeUnixNano()),
		EndTime:       unixNano(s.GetEndTimeUnixNano()),
		Attributes:    nested,
		SpanKind:      trace.SpanKindFromAttribute(flat[spanKindAttr]),
		StatusCode:    decodeStatus(s.GetStatus().GetCode()),
		StatusMessage: s.GetStatus().GetMessage(),
	}
	applyExtracted(&span, ex)
	for _, e := range s.GetEvents() {
		span.Events = append(span.Events, trace.Event{
			Name:       e.GetName(),
			Timestamp:  unixNano(e.GetTimeUnixNano()),
			Attributes: decodeAttributes(e.GetAttributes()),
		})
	}
	return span
}

// applyExtracted copies semconv extraction results onto a span,
// converting to the trace-package's own value types.
func applyExtracted(span *trace.Span, ex semconv.Extracted) {
	if ex.Input != nil {
		span.Input = &trace.IOValue{Value: ex.Input.Value, MimeType: string(ex.Input.MimeType)}
	}
	if ex.Output != nil {
		span.Output = &trace.IOValue{Value: ex.Output.Value, MimeType: string(ex.Output.MimeType)}
	}
	if ex.TokenUsage != nil {
		span.TokenUsage = &trace.TokenUsage{
			Prompt:     ex.TokenUsage.Prompt,
			Completion: ex.TokenUsage.Completion,
			Total:      ex.TokenUsage.Total,
			CacheRead:  ex.TokenUsage.CacheRead,
			CacheWrite: ex.TokenUsage.CacheWrite,
		}
	}
	span.ModelName = ex.ModelName
	span.SessionID = ex.SessionID
	span.UserID = ex.UserID
	span.InvocationParams = ex.InvocationParams
}

// unixNano converts OTLP's uint64 nanosecond timestamp to a time.Time.
func unixNano(n uint64) time.Time {
	return time.Unix(0, int64(n)).UTC()
}

func decodeStatus(c tracepb.Status_StatusCode) trace.StatusCode {
	switch c {
	case tracepb.Status_STATUS_CODE_OK:
		return trace.StatusOK
	case tracepb.Status_STATUS_CODE_ERROR:
		return trace.StatusError
	default:
		return trace.StatusUnset
	}
}

// decodeAttributes flattens a repeated KeyValue list into a plain map.
// Values decode recursively: arrays become slices, nested kvlists
// become maps, and unset values are omitted.
func decodeAttributes(kvs []*commonpb.KeyValue) map[string]any {
	m := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		if v, ok := decodeValue(kv.GetValue()); ok {
			m[kv.GetKey()] = v
		}
	}
	return m
}

func decodeValue(v *commonpb.AnyValue) (any, bool) {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue, true
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue, true
	case *commonpb.AnyValue_IntValue:
		return x.IntValue, true
	case *commonpb.AnyValue_DoubleValue:
		return x.DoubleValue, true
	case *commonpb.AnyValue_ArrayValue:
		out := make([]any, 0, len(x.ArrayValue.GetValues()))
		for _, e := range x.ArrayValue.GetValues() {
			if ev, ok := decodeValue(e); ok {
				out = append(out, ev)
			}
		}
		return out, true
	case *commonpb.AnyValue_KvlistValue:
		return decodeAttributes(x.KvlistValue.GetValues()), true
	case *commonpb.AnyValue_BytesValue:
		return x.BytesValue, true
	default:
		return nil, false
	}
}
