package attrs_test

import (
	"reflect"
	"testing"

	"github.com/skymoon8/tracewell/internal/attrs"
)

func TestLoadJSONStrings(t *testing.T) {
	in := []attrs.KV{
		{"input.value", "plain"},
		{"metadata", `{"env":"dev","tags":["a"]}`},
		{"document.metadata", `{"title":"doc"}`},
		{"tool.parameters", `{"q":"x"}`},
		{"llm.prompt_template.variables", `{"topic":"cats"}`},
		// not a JSON-string key: untouched even if it looks like JSON
		{"input.value2", `{"nope":true}`},
		// invalid JSON: passes through
		{"metadata", `{broken`},
	}
	got := attrs.LoadJSONStrings(in)
	if got[0].Value != "plain" {
		t.Errorf("plain value altered: %v", got[0].Value)
	}
	if v, ok := got[1].Value.(map[string]any); !ok || v["env"] != "dev" {
		t.Errorf("metadata not decoded: %#v", got[1].Value)
	}
	if v, ok := got[2].Value.(map[string]any); !ok || v["title"] != "doc" {
		t.Errorf("document.metadata not decoded: %#v", got[2].Value)
	}
	if v, ok := got[3].Value.(map[string]any); !ok || v["q"] != "x" {
		t.Errorf("tool.parameters not decoded: %#v", got[3].Value)
	}
	if v, ok := got[4].Value.(map[string]any); !ok || v["topic"] != "cats" {
		t.Errorf("prompt template variables not decoded: %#v", got[4].Value)
	}
	if _, ok := got[5].Value.(string); !ok {
		t.Errorf("non-convention key was decoded: %#v", got[5].Value)
	}
	if v, ok := got[6].Value.(string); !ok || v != "{broken" {
		t.Errorf("invalid JSON not preserved: %#v", got[6].Value)
	}

	// Input slice must not be mutated.
	if in[1].Value != `{"env":"dev","tags":["a"]}` {
		t.Errorf("input slice was mutated: %#v", in[1].Value)
	}
}

func TestLoadJSONStringsNullDropped(t *testing.T) {
	got := attrs.LoadJSONStrings([]attrs.KV{{"metadata", `null`}})
	if got[0].Value != nil {
		t.Errorf("JSON null should map to nil, got %#v", got[0].Value)
	}
}

func TestEndToEndIngestionShapedInput(t *testing.T) {
	// Simulates a realistic span attribute set: mixed JSON strings and
	// flat conventions, then unflatten.
	pairs := attrs.LoadJSONStrings([]attrs.KV{
		{"openinference.span.kind", "RETRIEVER"},
		{"input.value", "query"},
		{"retrieval.documents.0.document.content", "paris is the capital of france"},
		{"retrieval.documents.0.document.id", "1"},
		{"retrieval.documents.0.document.score", 0.98},
		{"retrieval.documents.1.document.content", "the eiffel tower"},
		{"retrieval.documents.1.document.id", "2"},
		{"metadata", `{"user":"u1","channel":"web"}`},
	})
	got := attrs.Unflatten(pairs)

	docs, ok := got["retrieval"].(map[string]any)["documents"].([]any)
	if !ok || len(docs) != 2 {
		t.Fatalf("documents = %#v", got["retrieval"])
	}
	if d := docs[0].(map[string]any)["document"]; d.(map[string]any)["id"] != "1" {
		t.Errorf("doc 0 = %#v", d)
	}
	if docs[1].(map[string]any)["document"].(map[string]any)["content"] != "the eiffel tower" {
		t.Errorf("doc 1 content wrong: %#v", docs[1])
	}
	meta := got["metadata"].(map[string]any)
	if meta["user"] != "u1" || !reflect.DeepEqual(meta["tags"], nil) {
		if meta["channel"] != "web" {
			t.Errorf("metadata = %#v", meta)
		}
	}
}
