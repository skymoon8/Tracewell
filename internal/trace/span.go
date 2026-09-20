// Package trace defines Tracewell's internal span model.
package trace

import "time"

// SpanKind classifies the type of work a span encapsulates.
// It mirrors the semantic-convention span kinds used by LLM
// instrumentation: every LLM application step falls into one of
// these categories, and unknown kinds degrade to KindUnknown rather
// than causing an error.
type SpanKind string

const (
	KindTool      SpanKind = "TOOL"
	KindChain     SpanKind = "CHAIN"
	KindLLM       SpanKind = "LLM"
	KindPrompt    SpanKind = "PROMPT"
	KindRetriever SpanKind = "RETRIEVER"
	KindEmbedding SpanKind = "EMBEDDING"
	KindAgent     SpanKind = "AGENT"
	KindReranker  SpanKind = "RERANKER"
	KindEvaluator SpanKind = "EVALUATOR"
	KindGuardrail SpanKind = "GUARDRAIL"
	KindUnknown   SpanKind = "UNKNOWN"
)

// StatusCode is the OTLP span status, narrowed to its three states.
type StatusCode string

const (
	StatusUnset StatusCode = "UNSET"
	StatusOK    StatusCode = "OK"
	StatusError StatusCode = "ERROR"
)

// Event is a timestamped annotation on a span (e.g. an exception).
type Event struct {
	Name       string
	Timestamp  time.Time
	Attributes map[string]any
}

// Span is Tracewell's decoded, protocol-independent representation of
// a single unit of work within a trace.
type Span struct {
	Name          string
	TraceID       string // hex-encoded
	SpanID        string // hex-encoded
	ParentID      string // hex-encoded; empty for root spans
	StartTime     time.Time
	EndTime       time.Time
	Attributes    map[string]any
	SpanKind      SpanKind
	StatusCode    StatusCode
	StatusMessage string
	Events        []Event
}

// SpanKindFromAttribute maps a raw "openinference.span.kind" attribute
// value to a SpanKind, tolerating any-letter-case input and falling
// back to KindUnknown for empty or unrecognized values.
func SpanKindFromAttribute(v any) SpanKind {
	s, ok := v.(string)
	if !ok || s == "" {
		return KindUnknown
	}
	k := SpanKind(toUpperASCII(s))
	switch k {
	case KindTool, KindChain, KindLLM, KindPrompt, KindRetriever,
		KindEmbedding, KindAgent, KindReranker, KindEvaluator, KindGuardrail:
		return k
	default:
		return KindUnknown
	}
}

func toUpperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}
