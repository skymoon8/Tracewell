package attrs

import "encoding/json"

// jsonStringKeys lists attribute keys whose values arrive as JSON
// strings and are parsed into structured values at ingestion time.
// A value that fails to parse is left as-is; a value that parses to
// null is dropped entirely.
var jsonStringKeys = map[string]bool{
	"document.metadata":             true,
	"llm.prompt_template.variables": true,
	"metadata":                      true,
	"tool.parameters":               true,
}

// LoadJSONStrings returns a copy of pairs in which values of known
// JSON-string attributes are decoded into maps. Parse failures pass
// through unchanged; JSON null becomes absent.
func LoadJSONStrings(pairs []KV) []KV {
	out := make([]KV, len(pairs))
	copy(out, pairs)
	for i, kv := range out {
		if !jsonStringKeys[kv.Key] {
			continue
		}
		s, ok := kv.Value.(string)
		if !ok {
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(s), &decoded); err != nil {
			continue
		}
		if decoded == nil {
			out[i].Value = nil
			continue
		}
		out[i].Value = decoded
	}
	return out
}
