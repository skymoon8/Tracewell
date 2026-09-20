// Package semconv extracts LLM-domain structures from span attributes
// following OpenInference-style semantic conventions.
package semconv

import (
	"strings"
)

// Convention attribute keys used for extraction.
const (
	KeySpanKind               = "openinference.span.kind"
	KeyInputValue             = "input.value"
	KeyInputMimeType          = "input.mime_type"
	KeyOutputValue            = "output.value"
	KeyOutputMimeType         = "output.mime_type"
	KeyLLMModelName           = "llm.model_name"
	KeyLLMInvocationParams    = "llm.invocation_parameters"
	KeyTokenCountPrompt       = "llm.token_count.prompt"
	KeyTokenCountCompletion   = "llm.token_count.completion"
	KeyTokenCountTotal        = "llm.token_count.total"
	KeyTokenCountPromptCache  = "llm.token_count.prompt_details.cache_read"
	KeyTokenCountPromptCacheW = "llm.token_count.prompt_details.cache_write"
	KeySessionID              = "session.id"
	KeyUserID                 = "user.id"

	// GenAI (OTel semconv) keys synthesized into the conventions above.
	KeyGenAIOperationName = "gen_ai.operation.name"
	KeyGenAIRequestModel  = "gen_ai.request.model"
	KeyGenAIResponseModel = "gen_ai.response.model"
	KeyGenAIInputTokens   = "gen_ai.usage.input_tokens"
	KeyGenAIOutputTokens  = "gen_ai.usage.output_tokens"
	KeyGenAICacheRead     = "gen_ai.usage.cache_read.input_tokens"
	KeyGenAICacheWrite    = "gen_ai.usage.cache_creation.input_tokens"
)

// MimeType narrows IO values to the two formats that matter for LLM
// payloads. Unrecognized values fall back to Text rather than erroring.
type MimeType string

const (
	MimeTypeText MimeType = "text/plain"
	MimeTypeJSON MimeType = "application/json"
)

// mimeTypeFromAttribute maps a raw mime_type attribute value.
func mimeTypeFromAttribute(v any) MimeType {
	s, ok := v.(string)
	if !ok {
		return MimeTypeText
	}
	switch s {
	case "application/json", "text/json", "json":
		return MimeTypeJSON
	default:
		return MimeTypeText
	}
}

// IOValue is a structured input or output captured on a span.
type IOValue struct {
	Value    string
	MimeType MimeType
}

// TokenUsage is the token accounting of an LLM call. Detail counts
// (cache read/write) are subsets of Prompt.
type TokenUsage struct {
	Prompt     int
	Completion int
	Total      int
	CacheRead  int
	CacheWrite int
}

// Extracted is the LLM-domain view of a span's attributes. Fields are
// nil/empty when the corresponding conventions are absent; extraction
// never fails.
type Extracted struct {
	Input            *IOValue
	Output           *IOValue
	TokenUsage       *TokenUsage
	ModelName        string
	SessionID        string
	UserID           string
	InvocationParams string
}

// Extract pulls domain structures from a nested attribute map.
func Extract(attrs map[string]any) Extracted {
	e := Extracted{}
	e.Input = extractIO(attrs, KeyInputValue, KeyInputMimeType)
	e.Output = extractIO(attrs, KeyOutputValue, KeyOutputMimeType)
	e.TokenUsage = extractTokenUsage(attrs)
	e.ModelName = asString(attrs, KeyLLMModelName)
	e.SessionID = asString(attrs, KeySessionID)
	e.UserID = asString(attrs, KeyUserID)
	e.InvocationParams = asString(attrs, KeyLLMInvocationParams)
	return e
}

func extractIO(attrs map[string]any, valueKey, mimeKey string) *IOValue {
	v, ok := attrs[valueKey]
	if !ok || v == nil {
		return nil
	}
	io := &IOValue{MimeType: mimeTypeFromAttribute(attrs[mimeKey])}
	switch x := v.(type) {
	case string:
		io.Value = x
	case []any, map[string]any:
		// Structured values are serialized as JSON with the JSON
		// mime type, mirroring how exporters emit complex payloads.
		if b, err := marshalJSON(x); err == nil {
			io.Value = b
			io.MimeType = MimeTypeJSON
		}
	default:
		io.Value = asString(attrs, valueKey)
	}
	if io.Value == "" {
		return nil
	}
	return io
}

func extractTokenUsage(attrs map[string]any) *TokenUsage {
	prompt, pOK := asInt(attrs, KeyTokenCountPrompt)
	completion, cOK := asInt(attrs, KeyTokenCountCompletion)
	total, tOK := asInt(attrs, KeyTokenCountTotal)
	cacheRead, _ := asInt(attrs, KeyTokenCountPromptCache)
	cacheWrite, _ := asInt(attrs, KeyTokenCountPromptCacheW)
	if !pOK && !cOK && !tOK && cacheRead == 0 && cacheWrite == 0 {
		return nil
	}
	u := &TokenUsage{
		Prompt:     prompt,
		Completion: completion,
		Total:      total,
		CacheRead:  cacheRead,
		CacheWrite: cacheWrite,
	}
	// Derive total when absent: prompt + completion, falling back to
	// whichever side exists.
	if !tOK {
		switch {
		case pOK && cOK:
			u.Total = prompt + completion
		case pOK:
			u.Total = prompt
		case cOK:
			u.Total = completion
		}
	}
	return u
}

// asString returns a string value at a top-level key, or "".
func asString(attrs map[string]any, key string) string {
	if v, ok := attrs[key].(string); ok {
		return v
	}
	return ""
}

// asInt coerces an attribute value to an int. Floats truncate; numeric
// strings parse; anything else yields ok=false. Failure never surfaces
// as an error: observability data is best-effort by contract.
func asInt(attrs map[string]any, key string) (int, bool) {
	switch v := attrs[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case string:
		s := strings.TrimSpace(v)
		if n, err := parseInt(s); err == nil {
			return n, true
		}
	}
	return 0, false
}
