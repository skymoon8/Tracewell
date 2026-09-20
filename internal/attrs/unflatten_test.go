package attrs_test

import (
	"reflect"
	"testing"

	"github.com/skymoon8/tracewell/internal/attrs"
)

func TestUnflattenBasics(t *testing.T) {
	cases := []struct {
		name  string
		pairs []attrs.KV
		want  map[string]any
	}{
		{
			name:  "single key",
			pairs: []attrs.KV{{"a", 1}},
			want:  map[string]any{"a": 1},
		},
		{
			name: "simple nesting",
			pairs: []attrs.KV{
				{"llm.token_count.completion", 123},
			},
			want: map[string]any{
				"llm": map[string]any{"token_count": map[string]any{"completion": 123}},
			},
		},
		{
			name: "nil values skipped",
			pairs: []attrs.KV{
				{"a", nil},
				{"b", 2},
			},
			want: map[string]any{"b": 2},
		},
		{
			name: "whitespace trimmed and empty segments dropped",
			pairs: []attrs.KV{
				{" a .. b ", 1},
			},
			want: map[string]any{"a": map[string]any{"b": 1}},
		},
		{
			name: "last write wins",
			pairs: []attrs.KV{
				{"a", 1},
				{"a", 2},
			},
			want: map[string]any{"a": 2},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := attrs.Unflatten(c.pairs)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestUnflattenArraysOnlyForMappings(t *testing.T) {
	cases := []struct {
		name  string
		pairs []attrs.KV
		want  map[string]any
	}{
		{
			name: "digit segments continuing into mappings become array indexes",
			pairs: []attrs.KV{
				{"documents.0.content", "A"},
				{"documents.1.content", "B"},
			},
			want: map[string]any{
				"documents": []any{
					map[string]any{"content": "A"},
					map[string]any{"content": "B"},
				},
			},
		},
		{
			name: "digit segments with scalar leaves stay dict keys",
			pairs: []attrs.KV{
				{"tags.0", "python"},
				{"tags.1", "ai"},
			},
			want: map[string]any{
				"tags": map[string]any{"0": "python", "1": "ai"},
			},
		},
		{
			name: "indices promoted when a later key extends into a mapping",
			pairs: []attrs.KV{
				{"docs.0", "scalar first"},
				{"docs.1.content", "nested"},
			},
			// "docs.0" ends with a scalar -> demoted to branch key "0".
			// "docs.1.content" extends -> "1" is an index. Mixed content
			// falls back to branches for stability.
			want: map[string]any{
				"docs": map[string]any{
					"0": "scalar first",
					"1": map[string]any{"content": "nested"},
				},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := attrs.Unflatten(c.pairs)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestUnflattenEdgeCases(t *testing.T) {
	cases := []struct {
		name  string
		pairs []attrs.KV
		want  map[string]any
	}{
		{
			name: "leading zero normalized",
			pairs: []attrs.KV{
				{"a.00", "x"},
				{"a.0", "y"},
			},
			want: map[string]any{"a": map[string]any{"0": "y"}},
		},
		{
			name: "negative numbers are string keys",
			pairs: []attrs.KV{
				{"a.-1", "x"},
			},
			want: map[string]any{"a": map[string]any{"-1": "x"}},
		},
		{
			name: "alphanumeric keys are string keys",
			pairs: []attrs.KV{
				{"a.0a", "x"},
			},
			want: map[string]any{"a": map[string]any{"0a": "x"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := attrs.Unflatten(c.pairs)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestUnflattenTerminalValueNodes(t *testing.T) {
	cases := []struct {
		name  string
		pairs []attrs.KV
		want  map[string]any
	}{
		{
			name: "extension of valued node becomes literal dotted key",
			pairs: []attrs.KV{
				{"a", map[string]any{"b": 1}},
				{"a.c", 2},
			},
			want: map[string]any{
				"a":   map[string]any{"b": 1},
				"a.c": 2,
			},
		},
		{
			name: "valued node with children keeps both",
			pairs: []attrs.KV{
				{"a.b", 1},
				{"a", "scalar"},
				{"a.c", 2},
			},
			want: map[string]any{
				"a.b": 1,
				"a":   "scalar",
				"a.c": 2,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := attrs.Unflatten(c.pairs)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestUnflattenSemanticPrefixesAtomic(t *testing.T) {
	// Semantic conventions containing dots must not be split.
	cases := []struct {
		name  string
		pairs []attrs.KV
		want  map[string]any
	}{
		{
			name: "token count prefix stays atomic",
			pairs: []attrs.KV{
				{"llm.token_count.prompt_details.cache_read", 5},
			},
			want: map[string]any{
				"llm": map[string]any{
					"token_count": map[string]any{
						"prompt_details": map[string]any{"cache_read": 5},
					},
				},
			},
		},
		{
			name: "prompt_details is the convention; prompt must not swallow it",
			pairs: []attrs.KV{
				{"llm.token_count.prompt", 10},
			},
			want: map[string]any{
				"llm": map[string]any{
					"token_count": map[string]any{"prompt": 10},
				},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := attrs.Unflatten(c.pairs)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %#v, want %#v", got, c.want)
			}
		})
	}
}
