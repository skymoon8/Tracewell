package semconv

import (
	"encoding/json"
	"strconv"
	"strings"
)

func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseInt(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// genaiMapping maps OTel GenAI semconv keys to the convention keys
// they synthesize. Only high-value, low-complexity mappings are
// implemented: model names, token usage, and span-kind inference.
// Message/tool/document mappings are out of scope.
var genaiSpanKinds = map[string]string{
	"chat":            "LLM",
	"text_completion": "LLM",
	"embeddings":      "EMBEDDING",
	"execute_tool":    "TOOL",
	"create_agent":    "AGENT",
	"invoke_agent":    "AGENT",
	"retrieval":       "RETRIEVER",
}

// SynthesizeFromGenAI builds convention attributes from any gen_ai.*
// attributes present in the flat map. When both forms exist, the
// explicitly set convention attribute wins: synthesized values never
// overwrite.
//
// Returns nil when the map contains no gen_ai keys, so spans without
// GenAI semconv pay a single map-scan cost.
func SynthesizeFromGenAI(flat map[string]any) map[string]any {
	has := false
	for k := range flat {
		if strings.HasPrefix(k, "gen_ai.") {
			has = true
			break
		}
	}
	if !has {
		return nil
	}

	out := map[string]any{}
	if kind, ok := flat[KeyGenAIOperationName].(string); ok {
		if mapped, ok := genaiSpanKinds[strings.ToLower(kind)]; ok {
			out[KeySpanKind] = mapped
		}
	}
	if model, ok := flat[KeyGenAIRequestModel].(string); ok {
		out[KeyLLMModelName] = model
	} else if model, ok := flat[KeyGenAIResponseModel].(string); ok {
		out[KeyLLMModelName] = model
	}

	prompt, pOK := asInt(flat, KeyGenAIInputTokens)
	completion, cOK := asInt(flat, KeyGenAIOutputTokens)
	if pOK {
		out[KeyTokenCountPrompt] = prompt
	}
	if cOK {
		out[KeyTokenCountCompletion] = completion
	}
	if pOK && cOK {
		out[KeyTokenCountTotal] = prompt + completion
	}
	if cr, ok := asInt(flat, KeyGenAICacheRead); ok {
		out[KeyTokenCountPromptCache] = cr
	}
	if cw, ok := asInt(flat, KeyGenAICacheWrite); ok {
		out[KeyTokenCountPromptCacheW] = cw
	}
	return out
}

// Merge synthesizes genai attributes into flat conventions, then into
// the nested view, with explicit convention keys winning over
// synthesized ones.
func Merge(nested map[string]any, flat map[string]any) Extracted {
	syn := SynthesizeFromGenAI(flat)
	if len(syn) == 0 {
		return Extract(nested)
	}
	// Build a merged flat view. Explicit convention attributes (the
	// nested view) are laid down last so they win over synthesized
	// genai values.
	merged := map[string]any{}
	for k, v := range syn {
		merged[k] = v
	}
	for k, v := range flattenTopLevel(nested) {
		merged[k] = v
	}
	return Extract(merged)
}

// flattenTopLevel walks a nested attribute map and yields dotted keys
// for scalar leaves, used to reconcile synthesized keys with nested
// conventions during extraction.
func flattenTopLevel(nested map[string]any) map[string]any {
	out := map[string]any{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			switch x := v.(type) {
			case map[string]any:
				walk(key, x)
			default:
				out[key] = x
			}
		}
	}
	walk("", nested)
	return out
}
