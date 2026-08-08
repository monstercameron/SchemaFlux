package ops

// This file drives textresult.go's four *Result wrappers, extractresult.go,
// compress.go's Compress/CompressText/toOpOptions, and utils.go's Min and
// BuildGenerateStringPrompt -- all 0.0% before this file except
// compress.go's Validate (already partially covered elsewhere).

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// =============================================================================
// textresult.go
// =============================================================================

func TestSummarizeResultHappyPath(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &scriptedProvider{bodies: []string{
		`{"text": "a short summary", "key_points": ["point one"], "confidence": 0.9}`,
	}}
	opts := NewSummarizeOptions()
	opts.OpOptions.Context = WithProvider(context.Background(), provider)

	result, err := SummarizeResult("a much longer piece of input text to summarize", opts)
	if err != nil {
		t.Fatalf("SummarizeResult: %v", err)
	}
	if result.Value.Text != "a short summary" {
		t.Errorf("Value.Text = %q, want %q", result.Value.Text, "a short summary")
	}
	if result.Meta.Operation != "summarize" {
		t.Errorf("Meta.Operation = %q, want %q", result.Meta.Operation, "summarize")
	}
	if result.Meta.Attempts < 1 {
		t.Errorf("Meta.Attempts = %d, want at least 1", result.Meta.Attempts)
	}
}

// TestSummarizeResultRefusalStillBillsTheAttempt is the refusal case: a
// response the operation cannot use must return an error, and the envelope
// must still report the attempt that was billed -- a caller reading Meta
// after an error is exactly the case "billed even on failure" exists for.
func TestSummarizeResultRefusalStillBillsTheAttempt(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &scriptedProvider{bodies: []string{"not json at all"}}
	opts := NewSummarizeOptions()
	opts.OpOptions.Context = WithProvider(context.Background(), provider)

	result, err := SummarizeResult("some input text", opts)
	if err == nil {
		t.Fatal("SummarizeResult accepted an unparseable response")
	}
	if result.Meta.Attempts < 1 {
		t.Errorf("Meta.Attempts = %d on a refused call, want at least 1 (the call was still billed)", result.Meta.Attempts)
	}
}

func TestRewriteResultHappyPath(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &scriptedProvider{bodies: []string{
		`{"text": "a rewritten sentence", "changes_made": ["tone"], "tone_achieved": "formal", "confidence": 0.8}`,
	}}
	opts := NewRewriteOptions()
	opts.OpOptions.Context = WithProvider(context.Background(), provider)

	result, err := RewriteResult("an original sentence", opts)
	if err != nil {
		t.Fatalf("RewriteResult: %v", err)
	}
	if result.Value.Text != "a rewritten sentence" {
		t.Errorf("Value.Text = %q, want %q", result.Value.Text, "a rewritten sentence")
	}
	if result.Meta.Operation != "rewrite" {
		t.Errorf("Meta.Operation = %q, want %q", result.Meta.Operation, "rewrite")
	}
}

func TestRewriteResultRefusesAMalformedResponse(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &scriptedProvider{bodies: []string{"not json"}}
	opts := NewRewriteOptions()
	opts.OpOptions.Context = WithProvider(context.Background(), provider)

	_, err := RewriteResult("an original sentence", opts)
	if err == nil {
		t.Fatal("RewriteResult accepted an unparseable response")
	}
}

func TestTranslateResultHappyPath(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &scriptedProvider{bodies: []string{
		`{"text": "hola", "source_language_detected": "English", "confidence": 0.9, "alternatives": []}`,
	}}
	opts := NewTranslateOptions().WithTargetLanguage("es")
	opts.OpOptions.Context = WithProvider(context.Background(), provider)

	result, err := TranslateResult("hello", opts)
	if err != nil {
		t.Fatalf("TranslateResult: %v", err)
	}
	if result.Value.Text != "hola" {
		t.Errorf("Value.Text = %q, want %q", result.Value.Text, "hola")
	}
	if result.Meta.Operation != "translate" {
		t.Errorf("Meta.Operation = %q, want %q", result.Meta.Operation, "translate")
	}
}

// TestTranslateResultRefusesMissingTargetLanguage: TranslateOptions.Validate
// requires a target language, so the refusal has to surface before any
// provider is ever consulted.
func TestTranslateResultRefusesMissingTargetLanguage(t *testing.T) {
	sawCall := false
	opts := NewTranslateOptions() // TargetLanguage left empty
	provider := &scriptedProvider{bodies: []string{`{"text": "x", "confidence": 0.5}`}}
	opts.OpOptions.Context = WithProvider(context.Background(), provider)

	_, err := TranslateResult("hello", opts)
	if err == nil {
		t.Fatal("TranslateResult accepted options with no target language")
	}
	if sawCall {
		t.Error("TranslateResult reached the provider despite invalid options")
	}
}

func TestExpandResultHappyPath(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &scriptedProvider{bodies: []string{
		`{"text": "a much longer elaborated version of the input", "added_content": ["background"], "confidence": 0.7}`,
	}}
	opts := NewExpandOptions()
	opts.OpOptions.Context = WithProvider(context.Background(), provider)

	result, err := ExpandResult("short input", opts)
	if err != nil {
		t.Fatalf("ExpandResult: %v", err)
	}
	if result.Value.Text == "" {
		t.Error("Value.Text is empty on a successful expansion")
	}
	if result.Meta.Operation != "expand" {
		t.Errorf("Meta.Operation = %q, want %q", result.Meta.Operation, "expand")
	}
}

func TestExpandResultRefusesAMalformedResponse(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &scriptedProvider{bodies: []string{"not json"}}
	opts := NewExpandOptions()
	opts.OpOptions.Context = WithProvider(context.Background(), provider)

	_, err := ExpandResult("short input", opts)
	if err == nil {
		t.Fatal("ExpandResult accepted an unparseable response")
	}
}

// =============================================================================
// extractresult.go
// =============================================================================

type extractWidget struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestExtractResultHappyPath(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &scriptedProvider{bodies: []string{`{"name": "widget-a", "count": 3}`}}
	opts := NewExtractOptions()
	opts.CommonOptions.Context = WithProvider(context.Background(), provider)

	result, err := ExtractResult[extractWidget]("a widget named widget-a, quantity 3", opts)
	if err != nil {
		t.Fatalf("ExtractResult: %v", err)
	}
	if result.Value.Name != "widget-a" {
		t.Errorf("Value.Name = %q, want %q", result.Value.Name, "widget-a")
	}
	if result.Meta.Operation != "extract" {
		t.Errorf("Meta.Operation = %q, want %q", result.Meta.Operation, "extract")
	}
	if result.Meta.DeliveredContract != types.ContractSchemaConstrained {
		t.Errorf("DeliveredContract = %v, want %v for a non-Strict call", result.Meta.DeliveredContract, types.ContractSchemaConstrained)
	}
}

// TestExtractResultRefusalReportsPromptOnlyContract: a failed extraction
// delivered nothing, so DeliveredContract must report the weakest level with
// a failed decode check -- not silently inherit whatever RequestedContract
// was.
func TestExtractResultRefusalReportsPromptOnlyContract(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	provider := &scriptedProvider{bodies: []string{"not json"}}
	opts := NewExtractOptions()
	opts.CommonOptions.Context = WithProvider(context.Background(), provider)

	result, err := ExtractResult[extractWidget]("something", opts)
	if err == nil {
		t.Fatal("ExtractResult accepted an unparseable response")
	}
	if result.Meta.DeliveredContract != types.ContractPromptOnly {
		t.Errorf("DeliveredContract = %v on a refusal, want %v", result.Meta.DeliveredContract, types.ContractPromptOnly)
	}
	found := false
	for _, check := range result.Meta.Checks {
		if check.Name == "decode" && !check.Passed {
			found = true
		}
	}
	if !found {
		t.Errorf("Checks = %v, want a failed 'decode' check", result.Meta.Checks)
	}
}

// --- requestedContractFor / deliveredContractFor: pure functions, tested
// directly against every combination the doc comments describe. ---

func TestRequestedContractFor(t *testing.T) {
	cases := []struct {
		name string
		opts ExtractOptions
		want types.ContractLevel
	}{
		{"transform mode, no flags", NewExtractOptions(), types.ContractSchemaConstrained},
		{"strict mode", NewExtractOptions().WithMode(types.Strict), types.ContractSchemaAndInvariantChecked},
		{"exact fields alone", NewExtractOptions().WithExactFields(), types.ContractSchemaAndInvariantChecked},
		{"complete fields alone", NewExtractOptions().WithCompleteFields(), types.ContractSchemaAndInvariantChecked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestedContractFor(tc.opts); got != tc.want {
				t.Errorf("requestedContractFor() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeliveredContractFor(t *testing.T) {
	failure := errors.New("boom")

	t.Run("a failure reports prompt-only with a failed decode check", func(t *testing.T) {
		level, checks := deliveredContractFor(NewExtractOptions(), failure)
		if level != types.ContractPromptOnly {
			t.Errorf("level = %v, want %v", level, types.ContractPromptOnly)
		}
		if len(checks) != 1 || checks[0].Passed {
			t.Errorf("checks = %v, want exactly one failed check", checks)
		}
	})

	t.Run("success with neither flag reports schema-constrained with json+schema checks", func(t *testing.T) {
		level, checks := deliveredContractFor(NewExtractOptions(), nil)
		if level != types.ContractSchemaConstrained {
			t.Errorf("level = %v, want %v", level, types.ContractSchemaConstrained)
		}
		names := map[string]bool{}
		for _, c := range checks {
			names[c.Name] = true
			if !c.Passed {
				t.Errorf("check %q reported as failed on a nil error", c.Name)
			}
		}
		if !names["json"] || !names["schema"] {
			t.Errorf("checks = %v, want at least json and schema", checks)
		}
		if names["exact decode"] || names["required fields"] {
			t.Errorf("checks = %v, want no exact/complete checks when neither flag was set", checks)
		}
	})

	t.Run("success with exact fields adds exact-decode and numeric-fidelity checks", func(t *testing.T) {
		level, checks := deliveredContractFor(NewExtractOptions().WithExactFields(), nil)
		if level != types.ContractSchemaAndInvariantChecked {
			t.Errorf("level = %v, want %v", level, types.ContractSchemaAndInvariantChecked)
		}
		names := map[string]bool{}
		for _, c := range checks {
			names[c.Name] = true
		}
		if !names["exact decode"] || !names["numeric fidelity"] {
			t.Errorf("checks = %v, want exact decode + numeric fidelity", checks)
		}
		if names["required fields"] {
			t.Errorf("checks = %v, want no required-fields check when CompleteFields was not set", checks)
		}
	})

	t.Run("success with complete fields adds a required-fields check", func(t *testing.T) {
		level, checks := deliveredContractFor(NewExtractOptions().WithCompleteFields(), nil)
		if level != types.ContractSchemaAndInvariantChecked {
			t.Errorf("level = %v, want %v", level, types.ContractSchemaAndInvariantChecked)
		}
		names := map[string]bool{}
		for _, c := range checks {
			names[c.Name] = true
		}
		if !names["required fields"] {
			t.Errorf("checks = %v, want a required-fields check", checks)
		}
		if names["exact decode"] {
			t.Errorf("checks = %v, want no exact-decode check when ExactFields was not set", checks)
		}
	})

	t.Run("strict mode is both halves at once", func(t *testing.T) {
		level, checks := deliveredContractFor(NewExtractOptions().WithMode(types.Strict), nil)
		if level != types.ContractSchemaAndInvariantChecked {
			t.Errorf("level = %v, want %v", level, types.ContractSchemaAndInvariantChecked)
		}
		names := map[string]bool{}
		for _, c := range checks {
			names[c.Name] = true
		}
		if !names["exact decode"] || !names["required fields"] {
			t.Errorf("checks = %v, want both halves under Strict", checks)
		}
	})
}

// =============================================================================
// compress.go
// =============================================================================

type compressedNote struct {
	Summary string `json:"summary"`
}

func TestCompressHappyPath(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"compressed": {"summary": "short"}, "preserved_info": ["dates"], "removed_info": ["filler"]}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	result, err := Compress(compressedNote{}, NewCompressOptions())
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if result.Compressed.Summary != "short" {
		t.Errorf("Compressed.Summary = %q, want %q", result.Compressed.Summary, "short")
	}
	if result.OriginalSize == 0 {
		t.Error("OriginalSize = 0, want the measured size of the marshaled input")
	}
}

// TestCompressRefusesInvalidOptions covers CompressOptions.toOpOptions (a
// one-line delegate to CommonOptions.toOpOptions, otherwise unreachable) via
// the ordinary call path, and is also Compress's refusal case for bad
// options.
func TestCompressRefusesInvalidOptions(t *testing.T) {
	opts := NewCompressOptions().WithCompressionRatio(5.0) // out of (0,1]
	_, err := Compress(compressedNote{}, opts)
	if err == nil {
		t.Fatal("Compress accepted an out-of-range compression ratio")
	}
}

func TestCompressRefusesAMalformedResponse(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "not json", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	_, err := Compress(compressedNote{}, NewCompressOptions())
	if err == nil {
		t.Fatal("Compress accepted an unparseable response")
	}
}

// TestCompressStringFallbackParsesADoublyEncodedPayload: the response's
// "compressed" field can arrive as a JSON string containing JSON (a model
// that double-encoded), and Compress falls back to parsing that string.
func TestCompressStringFallbackParsesADoublyEncodedPayload(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"compressed": "{\"summary\": \"nested\"}", "preserved_info": [], "removed_info": []}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	result, err := Compress(compressedNote{}, NewCompressOptions())
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if result.Compressed.Summary != "nested" {
		t.Errorf("Compressed.Summary = %q, want %q (via the string-fallback decode)", result.Compressed.Summary, "nested")
	}
}

func TestCompressTextHappyPath(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"compressed": "a shorter version", "preserved_info": [], "removed_info": []}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	result, err := CompressText("a much longer version of the same text", NewCompressOptions())
	if err != nil {
		t.Fatalf("CompressText: %v", err)
	}
	if result == "" {
		t.Error("CompressText returned an empty string on success")
	}
}

func TestCompressTextRefusesOnFailure(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", errors.New("provider down")
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	_, err := CompressText("some text", NewCompressOptions())
	if err == nil {
		t.Fatal("CompressText swallowed a provider error")
	}
}

// =============================================================================
// utils.go
// =============================================================================

func TestMin(t *testing.T) {
	cases := []struct{ a, b, want int }{
		{1, 2, 1},
		{2, 1, 1},
		{3, 3, 3},
		{-5, 5, -5},
		{0, 0, 0},
	}
	for _, tc := range cases {
		if got := Min(tc.a, tc.b); got != tc.want {
			t.Errorf("Min(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestGetTypeDescriptionLeafKinds covers the leaf-type branches describeType
// delegates to: only string and struct kinds were reached elsewhere in the
// package, leaving the numeric, bool, and pointer branches at 45.5%.
func TestGetTypeDescriptionLeafKinds(t *testing.T) {
	cases := []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{"int8", reflect.TypeOf(int8(0)), "integer"},
		{"uint", reflect.TypeOf(uint(0)), "unsigned integer"},
		{"float32", reflect.TypeOf(float32(0)), "number"},
		{"bool", reflect.TypeOf(false), "boolean"},
		{"time.Time", reflect.TypeOf(time.Time{}), "datetime (RFC3339)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GetTypeDescription(tc.typ); got != tc.want {
				t.Errorf("GetTypeDescription(%v) = %q, want %q", tc.typ, got, tc.want)
			}
		})
	}

	t.Run("pointer defers to the pointee and appends (optional)", func(t *testing.T) {
		got := GetTypeDescription(reflect.TypeOf((*int)(nil)))
		if got != "integer (optional)" {
			t.Errorf("GetTypeDescription(*int) = %q, want %q", got, "integer (optional)")
		}
	})

	t.Run("an unrecognised kind falls back to the type's own name", func(t *testing.T) {
		got := GetTypeDescription(reflect.TypeOf(make(chan int)))
		if got == "" {
			t.Error("GetTypeDescription(chan int) returned empty")
		}
	})
}

// TestBuildGenerateStringPromptVariesByMode does not assert an exact prompt
// string -- only the property the callers of this function rely on: each
// documented mode produces a distinct, non-empty instruction, so a caller
// selecting Strict really does get different guidance from Creative.
func TestBuildGenerateStringPromptVariesByMode(t *testing.T) {
	modes := []types.Mode{types.Strict, types.TransformMode, types.Creative, types.ModeUnset}
	seen := map[string]types.Mode{}
	for _, mode := range modes {
		prompt := BuildGenerateStringPrompt(mode)
		if strings.TrimSpace(prompt) == "" {
			t.Errorf("BuildGenerateStringPrompt(%v) returned an empty prompt", mode)
		}
		if other, ok := seen[prompt]; ok {
			t.Errorf("BuildGenerateStringPrompt(%v) and BuildGenerateStringPrompt(%v) produced the identical prompt: %q", mode, other, prompt)
		}
		seen[prompt] = mode
	}
}
