package semconv_test

import (
	"reflect"
	"testing"

	"github.com/skymoon8/tracewell/internal/semconv"
)

func TestExtractIO(t *testing.T) {
	t.Run("string input with text mime", func(t *testing.T) {
		e := semconv.Extract(map[string]any{
			"input.value": "hello",
		})
		if e.Input == nil || e.Input.Value != "hello" || e.Input.MimeType != semconv.MimeTypeText {
			t.Errorf("input = %#v", e.Input)
		}
		if e.Output != nil {
			t.Errorf("output should be nil, got %#v", e.Output)
		}
	})
	t.Run("json mime respected", func(t *testing.T) {
		e := semconv.Extract(map[string]any{
			"input.value":     `{"q":1}`,
			"input.mime_type": "application/json",
		})
		if e.Input.MimeType != semconv.MimeTypeJSON {
			t.Errorf("mime = %v", e.Input.MimeType)
		}
	})
	t.Run("unrecognized mime falls back to text", func(t *testing.T) {
		e := semconv.Extract(map[string]any{
			"input.value":     "x",
			"input.mime_type": "banana",
		})
		if e.Input.MimeType != semconv.MimeTypeText {
			t.Errorf("mime = %v", e.Input.MimeType)
		}
	})
	t.Run("structured value becomes JSON", func(t *testing.T) {
		e := semconv.Extract(map[string]any{
			"input.value": map[string]any{"q": "x"},
		})
		if e.Input == nil || e.Input.MimeType != semconv.MimeTypeJSON || e.Input.Value != `{"q":"x"}` {
			t.Errorf("input = %#v", e.Input)
		}
	})
	t.Run("empty value yields nil", func(t *testing.T) {
		e := semconv.Extract(map[string]any{"input.value": ""})
		if e.Input != nil {
			t.Errorf("input = %#v", e.Input)
		}
	})
}

func TestExtractTokenUsage(t *testing.T) {
	t.Run("all fields", func(t *testing.T) {
		e := semconv.Extract(map[string]any{
			"llm.token_count.prompt":                    int64(10),
			"llm.token_count.completion":                int64(5),
			"llm.token_count.total":                     int64(15),
			"llm.token_count.prompt_details.cache_read": int64(4),
		})
		want := &semconv.TokenUsage{Prompt: 10, Completion: 5, Total: 15, CacheRead: 4}
		if !reflect.DeepEqual(e.TokenUsage, want) {
			t.Errorf("usage = %#v, want %#v", e.TokenUsage, want)
		}
	})
	t.Run("total derived when absent", func(t *testing.T) {
		e := semconv.Extract(map[string]any{
			"llm.token_count.prompt":     float64(10.9),
			"llm.token_count.completion": int64(5),
		})
		if e.TokenUsage == nil || e.TokenUsage.Total != 15 {
			t.Errorf("usage = %#v", e.TokenUsage)
		}
		if e.TokenUsage.Prompt != 10 {
			t.Errorf("float should truncate, got %d", e.TokenUsage.Prompt)
		}
	})
	t.Run("numeric strings coerce", func(t *testing.T) {
		e := semconv.Extract(map[string]any{
			"llm.token_count.prompt": "12",
		})
		if e.TokenUsage == nil || e.TokenUsage.Prompt != 12 || e.TokenUsage.Total != 12 {
			t.Errorf("usage = %#v", e.TokenUsage)
		}
	})
	t.Run("absent yields nil", func(t *testing.T) {
		e := semconv.Extract(map[string]any{"input.value": "x"})
		if e.TokenUsage != nil {
			t.Errorf("usage = %#v", e.TokenUsage)
		}
	})
}

func TestExtractMisc(t *testing.T) {
	e := semconv.Extract(map[string]any{
		"llm.model_name":            "gpt-x",
		"session.id":                "s1",
		"user.id":                   "u1",
		"llm.invocation_parameters": `{"temperature":0}`,
	})
	if e.ModelName != "gpt-x" || e.SessionID != "s1" || e.UserID != "u1" {
		t.Errorf("extracted = %#v", e)
	}
	if e.InvocationParams != `{"temperature":0}` {
		t.Errorf("invocation params = %q", e.InvocationParams)
	}
}

func TestSynthesizeFromGenAI(t *testing.T) {
	t.Run("no genai keys is free", func(t *testing.T) {
		if got := semconv.SynthesizeFromGenAI(map[string]any{"input.value": "x"}); got != nil {
			t.Errorf("want nil, got %#v", got)
		}
	})
	t.Run("usage and model synthesized", func(t *testing.T) {
		got := semconv.SynthesizeFromGenAI(map[string]any{
			"gen_ai.request.model":       "m1",
			"gen_ai.usage.input_tokens":  int64(3),
			"gen_ai.usage.output_tokens": float64(2),
		})
		want := map[string]any{
			"llm.model_name":             "m1",
			"llm.token_count.prompt":     3, // asInt normalizes to int
			"llm.token_count.completion": 2,
			"llm.token_count.total":      5,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %#v, want %#v", got, want)
		}
	})
	t.Run("operation name maps to span kind", func(t *testing.T) {
		got := semconv.SynthesizeFromGenAI(map[string]any{
			"gen_ai.operation.name": "chat",
		})
		if got["openinference.span.kind"] != "LLM" {
			t.Errorf("kind = %#v", got["openinference.span.kind"])
		}
	})
	t.Run("cache details synthesized", func(t *testing.T) {
		got := semconv.SynthesizeFromGenAI(map[string]any{
			"gen_ai.usage.cache_read.input_tokens":     int64(4),
			"gen_ai.usage.cache_creation.input_tokens": int64(1),
		})
		if got["llm.token_count.prompt_details.cache_read"] != 4 {
			t.Errorf("cache read = %#v", got)
		}
		if got["llm.token_count.prompt_details.cache_write"] != 1 {
			t.Errorf("cache write = %#v", got)
		}
	})
}

func TestMergeExplicitWins(t *testing.T) {
	nested := map[string]any{
		"llm": map[string]any{
			"model_name": "explicit-model",
			"token_count": map[string]any{
				"prompt": int64(10),
			},
		},
	}
	flat := map[string]any{
		"gen_ai.request.model":      "genai-model",
		"gen_ai.usage.input_tokens": int64(3),
	}
	e := semconv.Merge(nested, flat)
	if e.ModelName != "explicit-model" {
		t.Errorf("explicit model must win, got %q", e.ModelName)
	}
	if e.TokenUsage == nil || e.TokenUsage.Prompt != 10 || e.TokenUsage.Total != 10 {
		t.Errorf("usage = %#v (explicit prompt 10 wins, total stays 10)", e.TokenUsage)
	}
}
