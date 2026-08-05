package ops

import (
	"context"
	"strconv"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// withResponse swaps the package LLM caller for the duration of a test.
func withResponse(t *testing.T, response string) {
	t.Helper()
	previous := customLLMCaller
	setLLMCaller(func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		return response, nil
	})
	t.Cleanup(func() { customLLMCaller = previous })
}

// The four *WithMetadata operations each returned the raw response as their
// result with a literal Confidence of 0.7, empty metadata, and a nil error when
// the body would not parse. A caller reading the documented metadata got an
// invented number and an empty list, with nothing to distinguish it from a real
// result. They also used json.Unmarshal directly, so any fenced response -- the
// normal shape from a provider without JSON mode -- took that path.
func TestTextMetadataOperationsDoNotFailOpen(t *testing.T) {
	malformed := []struct {
		name string
		body string
	}{
		{"prose", "Here is a nice summary of the document."},
		{"truncated_json", `{"text": "partial`},
		{"empty", ""},
		{"whitespace", "   \n\t "},
		{"html_error_page", "<html><body>502 Bad Gateway</body></html>"},
		{"json_array_not_object", `["not", "an", "object"]`},
	}

	for _, tc := range malformed {
		t.Run("summarize/"+tc.name, func(t *testing.T) {
			withResponse(t, tc.body)
			result, err := SummarizeWithMetadata("some input", NewSummarizeOptions())
			if err == nil {
				t.Fatalf("expected an error, got result %+v", result)
			}
			if result.Confidence != 0 {
				t.Errorf("a failed call must not report a confidence, got %v", result.Confidence)
			}
		})

		t.Run("rewrite/"+tc.name, func(t *testing.T) {
			withResponse(t, tc.body)
			if _, err := RewriteWithMetadata("some input", NewRewriteOptions()); err == nil {
				t.Fatal("expected an error")
			}
		})

		t.Run("translate/"+tc.name, func(t *testing.T) {
			withResponse(t, tc.body)
			if _, err := TranslateWithMetadata("some input", NewTranslateOptions()); err == nil {
				t.Fatal("expected an error")
			}
		})

		t.Run("expand/"+tc.name, func(t *testing.T) {
			withResponse(t, tc.body)
			if _, err := ExpandWithMetadata("some input", NewExpandOptions()); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// A fenced body is the normal shape from a provider without JSON mode. It must
// parse rather than fall through to the removed fallback.
func TestTextMetadataOperationsAcceptFencedJSON(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"fenced_with_language", "```json\n{\"text\":\"a summary\",\"key_points\":[\"one\",\"two\"],\"confidence\":0.9}\n```"},
		{"fenced_bare", "```\n{\"text\":\"a summary\",\"key_points\":[\"one\"],\"confidence\":0.8}\n```"},
		{"plain", `{"text":"a summary","key_points":["one"],"confidence":0.5}`},
		{"leading_whitespace", "\n\n  {\"text\":\"a summary\",\"confidence\":0.4}"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withResponse(t, tc.body)
			result, err := SummarizeWithMetadata("the original input text", NewSummarizeOptions())
			if err != nil {
				t.Fatalf("fenced JSON must parse: %v", err)
			}
			if result.Text != "a summary" {
				t.Errorf("Text = %q, want %q", result.Text, "a summary")
			}
			// The confidence must be the model's reported value, never a literal.
			if result.Confidence == 0.7 {
				t.Error("0.7 is the removed fallback literal; it must not reappear")
			}
		})
	}
}

// The reported confidence must come from the body, not from the code.
func TestSummarizeConfidenceComesFromTheResponse(t *testing.T) {
	for _, want := range []float64{0.0, 0.25, 0.5, 0.99, 1.0} {
		withResponse(t, `{"text":"s","confidence":`+strconv.FormatFloat(want, 'g', -1, 64)+`}`)
		result, err := SummarizeWithMetadata("input", NewSummarizeOptions())
		if err != nil {
			t.Fatalf("SummarizeWithMetadata: %v", err)
		}
		if result.Confidence != want {
			t.Errorf("Confidence = %v, want %v", result.Confidence, want)
		}
	}
}

// A provider error must surface, not be converted into a text result.
func TestTextMetadataOperationsPropagateProviderErrors(t *testing.T) {
	previous := customLLMCaller
	setLLMCaller(func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		return "", context.DeadlineExceeded
	})
	t.Cleanup(func() { customLLMCaller = previous })

	if _, err := SummarizeWithMetadata("input", NewSummarizeOptions()); err == nil {
		t.Error("summarize must propagate a provider error")
	}
	if _, err := RewriteWithMetadata("input", NewRewriteOptions()); err == nil {
		t.Error("rewrite must propagate a provider error")
	}
}

// The metadata fields the operation documents must actually be populated.
func TestSummarizeMetadataFieldsArePopulated(t *testing.T) {
	withResponse(t, `{"text":"short","key_points":["a","b","c"],"confidence":0.9}`)
	result, err := SummarizeWithMetadata("a much longer original input string", NewSummarizeOptions())
	if err != nil {
		t.Fatalf("SummarizeWithMetadata: %v", err)
	}
	if len(result.KeyPoints) != 3 {
		t.Errorf("KeyPoints = %v, want three entries", result.KeyPoints)
	}
	if result.CompressionRatio <= 0 || result.CompressionRatio >= 1 {
		t.Errorf("CompressionRatio = %v, want a fraction below 1 for a shorter summary", result.CompressionRatio)
	}
}

// No fallback literal may survive anywhere in the family.
func TestNoFallbackConfidenceLiteralRemains(t *testing.T) {
	withResponse(t, "definitely not json")
	for name, call := range map[string]func() (float64, error){
		"summarize": func() (float64, error) {
			r, err := SummarizeWithMetadata("x", NewSummarizeOptions())
			return r.Confidence, err
		},
		"rewrite": func() (float64, error) {
			r, err := RewriteWithMetadata("x", NewRewriteOptions())
			return r.Confidence, err
		},
		"translate": func() (float64, error) {
			r, err := TranslateWithMetadata("x", NewTranslateOptions())
			return r.Confidence, err
		},
		"expand": func() (float64, error) {
			r, err := ExpandWithMetadata("x", NewExpandOptions())
			return r.Confidence, err
		},
	} {
		confidence, err := call()
		if err == nil {
			t.Errorf("%s: expected an error", name)
		}
		if confidence != 0 {
			t.Errorf("%s: confidence %v reported on a failed call", name, confidence)
		}
	}
}
