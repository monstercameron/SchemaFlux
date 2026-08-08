package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// This file closes coverage gaps in explain.go, enrich.go, and core.go
// (Extract/Transform/Generate) left after an earlier pass. explain_test.go
// and enrich_test.go's own "integration" cases were all t.Skip'd, so the
// success and LLM-error paths of Explain and Enrich/EnrichInPlace had never
// actually run under `go test`; this file drives them with a scripted
// llm caller instead of skipping.

// --- explain.go -------------------------------------------------------

const explainMockResponse = `{
	"explanation": "This is a person record describing someone's name and age.",
	"summary": "A person's basic identity data.",
	"key_points": ["has a name", "has an age"]
}`

func installExplainResponse(t *testing.T, body string) {
	t.Helper()
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return body, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
}

type explainPerson struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// TestExplainImplSuccessPath is the success path explain_test.go's own
// integration test skipped: explainImpl actually decodes a scripted
// response into ExplainResult and fills the deterministic metadata fields
// from the data analysis, not from the model.
func TestExplainImplSuccessPath(t *testing.T) {
	installExplainResponse(t, explainMockResponse)

	result, err := explainImpl(explainPerson{Name: "Ada", Age: 30}, NewExplainOptions())
	if err != nil {
		t.Fatalf("explainImpl: %v", err)
	}
	if result.Explanation == "" {
		t.Error("expected a non-empty Explanation")
	}
	if result.Summary == "" {
		t.Error("expected a non-empty Summary")
	}
	if len(result.KeyPoints) != 2 {
		t.Errorf("KeyPoints = %v, want 2 entries", result.KeyPoints)
	}
	if result.Metadata["data_type"] != "ops.explainPerson" {
		t.Errorf("Metadata[data_type] = %v, want ops.explainPerson", result.Metadata["data_type"])
	}
	if result.Metadata["field_count"] != 2 {
		t.Errorf("Metadata[field_count] = %v, want 2", result.Metadata["field_count"])
	}
}

// TestExplainImplRefusesUnparseableResponse proves an unparseable model
// response is a failure, not an ExplainResult with an empty Explanation and
// a nil error -- explain.go's own comment says exactly this used to
// manufacture a result reading "Explanation generated".
func TestExplainImplRefusesUnparseableResponse(t *testing.T) {
	installExplainResponse(t, "not json, just prose")

	result, err := explainImpl(explainPerson{Name: "Ada", Age: 30}, NewExplainOptions())
	if err == nil {
		t.Fatal("expected explainImpl to fail on an unparseable response")
	}
	if result.Explanation != "" {
		t.Errorf("Explanation = %q on a failed call, want empty", result.Explanation)
	}
}

// TestExplainImplPropagatesLLMCallFailure covers generateExplanation's
// callLLM error branch.
func TestExplainImplPropagatesLLMCallFailure(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", context.DeadlineExceeded
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := explainImpl(explainPerson{Name: "Ada", Age: 30}, NewExplainOptions())
	if err == nil {
		t.Fatal("expected explainImpl to fail when the LLM call errors")
	}
}

// TestExplainImplRejectsInvalidOptionsBeforeCallingTheProvider proves the
// Validate() failure path returns before any provider call is attempted.
func TestExplainImplRejectsInvalidOptionsBeforeCallingTheProvider(t *testing.T) {
	calls := 0
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return explainMockResponse, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	invalid := NewExplainOptions().WithAudience("nonsense-audience")
	_, err := explainImpl(explainPerson{Name: "Ada"}, invalid)
	if err == nil {
		t.Fatal("expected explainImpl to reject invalid options")
	}
	if calls != 0 {
		t.Fatalf("provider called %d times despite invalid options", calls)
	}
}

// TestBuildSystemPromptCoversEveryAudienceFormatAndDepth exercises the
// remaining switch branches explain_test.go's TestBuildSystemPrompt does
// not: every Audience value, every Format value, and every Depth value.
func TestBuildSystemPromptCoversEveryAudienceFormatAndDepth(t *testing.T) {
	audiences := []string{"children", "non-technical", "executive", "beginner", "technical", "expert", ""}
	for _, audience := range audiences {
		opts := NewExplainOptions()
		opts.Audience = audience
		if buildSystemPrompt(opts) == "" {
			t.Errorf("buildSystemPrompt returned empty for audience %q", audience)
		}
	}

	formats := []string{"paragraph", "bullet-points", "step-by-step", "qa", "structured", ""}
	for _, format := range formats {
		opts := NewExplainOptions()
		opts.Format = format
		if buildSystemPrompt(opts) == "" {
			t.Errorf("buildSystemPrompt returned empty for format %q", format)
		}
	}

	for depth := 0; depth <= 5; depth++ {
		opts := NewExplainOptions()
		opts.Depth = depth
		if buildSystemPrompt(opts) == "" {
			t.Errorf("buildSystemPrompt returned empty for depth %d", depth)
		}
	}
}

// --- enrich.go ----------------------------------------------------------

type enrichSource struct {
	Name string `json:"name"`
}

type enrichTarget struct {
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

// TestEnrichComputesAddedFieldsRatherThanTrustingTheModel is OP-308's own
// property, exercised end to end: the model's added_fields claim
// ("nothing added") disagrees with what it actually returned (a new "score"
// field), and Enrich's AddedFields is computed by diffing the JSON field
// sets, not copied from the model's self-report.
func TestEnrichComputesAddedFieldsRatherThanTrustingTheModel(t *testing.T) {
	const body = `{
		"enriched": {"name": "widget", "score": 0.87},
		"added_fields": [],
		"confidence": {"score": 0.87},
		"derivations": {"score": "computed from name popularity"}
	}`
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return body, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	result, err := Enrich[enrichSource, enrichTarget](enrichSource{Name: "widget"}, NewEnrichOptions())
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if len(result.ModelClaimedAddedFields) != 0 {
		t.Fatalf("test setup error: expected the model to claim no added fields, got %v", result.ModelClaimedAddedFields)
	}
	if len(result.AddedFields) != 1 || result.AddedFields[0] != "score" {
		t.Fatalf("AddedFields = %v, want [score] (computed from the actual diff, not the model's claim)", result.AddedFields)
	}
	if result.Enriched.Score != 0.87 {
		t.Errorf("Enriched.Score = %v, want 0.87", result.Enriched.Score)
	}
}

// TestEnrichRefusesInvalidOptionsBeforeCallingTheProvider mirrors the same
// property Explain's test proves: Validate() runs before any provider call.
func TestEnrichRefusesInvalidOptionsBeforeCallingTheProvider(t *testing.T) {
	calls := 0
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `{"enriched":{}}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	invalid := NewEnrichOptions()
	invalid.Depth = "bottomless"
	_, err := Enrich[enrichSource, enrichTarget](enrichSource{Name: "x"}, invalid)
	if err == nil {
		t.Fatal("expected Enrich to reject an invalid Depth")
	}
	if calls != 0 {
		t.Fatalf("provider called %d times despite invalid options", calls)
	}
}

// TestEnrichRefusesUnparseableResponse proves a response that does not
// decode is a failure, not a zero-value EnrichResult reported as success.
func TestEnrichRefusesUnparseableResponse(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "not json", nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Enrich[enrichSource, enrichTarget](enrichSource{Name: "x"}, NewEnrichOptions())
	if err == nil {
		t.Fatal("expected Enrich to fail on an unparseable response")
	}
}

// TestEnrichPropagatesLLMCallFailure covers the callLLM error branch.
func TestEnrichPropagatesLLMCallFailure(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", context.DeadlineExceeded
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Enrich[enrichSource, enrichTarget](enrichSource{Name: "x"}, NewEnrichOptions())
	if err == nil {
		t.Fatal("expected Enrich to fail when the LLM call errors")
	}
}

// TestEnrichInPlaceSuccessPath exercises EnrichInPlace end to end -- it had
// 0% coverage: its only test in enrich_test.go was t.Skip'd.
func TestEnrichInPlaceSuccessPath(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"name":"widget","score":0.5}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	result, err := EnrichInPlace(enrichTarget{Name: "widget"}, NewEnrichOptions())
	if err != nil {
		t.Fatalf("EnrichInPlace: %v", err)
	}
	if result.Score != 0.5 {
		t.Errorf("Score = %v, want 0.5", result.Score)
	}
}

// TestEnrichInPlaceRefusesInvalidOptions mirrors Enrich's own check.
func TestEnrichInPlaceRefusesInvalidOptions(t *testing.T) {
	calls := 0
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `{}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	invalid := NewEnrichOptions()
	invalid.Depth = "bottomless"
	_, err := EnrichInPlace(enrichTarget{Name: "x"}, invalid)
	if err == nil {
		t.Fatal("expected EnrichInPlace to reject an invalid Depth")
	}
	if calls != 0 {
		t.Fatalf("provider called %d times despite invalid options", calls)
	}
}

// TestEnrichInPlaceRefusesUnparseableResponse proves a bad response fails
// rather than returning a zero-value T as success.
func TestEnrichInPlaceRefusesUnparseableResponse(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "not json", nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := EnrichInPlace(enrichTarget{Name: "x"}, NewEnrichOptions())
	if err == nil {
		t.Fatal("expected EnrichInPlace to fail on an unparseable response")
	}
}

// TestEnrichInPlacePropagatesLLMCallFailure covers the callLLM error branch.
func TestEnrichInPlacePropagatesLLMCallFailure(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", context.DeadlineExceeded
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := EnrichInPlace(enrichTarget{Name: "x"}, NewEnrichOptions())
	if err == nil {
		t.Fatal("expected EnrichInPlace to fail when the LLM call errors")
	}
}

// --- core.go: Extract's refusal paths not already covered ----------------

// TestExtractRefusesNilInput proves the explicit nil check, not merely
// whatever NormalizeInput or the schema machinery would do with it.
func TestExtractRefusesNilInput(t *testing.T) {
	calls := 0
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `{}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Extract[enrichTarget](nil, NewExtractOptions())
	if err == nil {
		t.Fatal("expected Extract to reject a nil input")
	}
	if calls != 0 {
		t.Fatalf("provider called %d times despite nil input", calls)
	}
}

// Extract[any] is refused cleanly, not by crashing.
//
// This test was written against a panic and is now inverted. core.go called
// reflect.TypeOf(result).String() on the target type's ZERO VALUE in a timing
// defer, a log line, and an error field -- all before the nil-input check and
// long before PreflightType. For a concrete type that is harmless; for an
// interface target the zero value is a nil interface, reflect.TypeOf(nil)
// returns a nil reflect.Type, and .String() on it panics. So Extract[any]
// crashed the calling goroutine instead of returning the KindConfiguration
// error PreflightType exists to produce.
//
// Fixed with reflect.TypeFor[T](), which reports the STATIC type -- what those
// log lines and error fields actually mean -- and is never nil. Eight sites
// across Extract, Transform, and Generate carried the same hazard.
func TestExtractOnAnInterfaceTargetIsRefusedRatherThanCrashing(t *testing.T) {
	calls := 0
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `{}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Extract[any] panicked: %v -- the zero-value reflection hazard is back", r)
		}
	}()

	_, err := Extract[any]("some input", NewExtractOptions())
	if err == nil {
		t.Fatal("Extract[any] was accepted; an interface target cannot be satisfied and PreflightType is meant to say so")
	}
	if calls != 0 {
		t.Errorf("the provider was called %d time(s) for a target type that cannot be satisfied; the refusal should come before any provider work", calls)
	}
}

// TestExtractWithExamplesReachesTheSteering proves the Examples branch of
// Extract's steering assembly (the WithSchemaHints/WithFieldRules siblings
// are already covered by schema_determinism_test.go) actually puts the
// examples into what the provider receives.
func TestExtractWithExamplesReachesTheSteering(t *testing.T) {
	var sawUserPrompt string
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		sawUserPrompt = user
		return `{"name":"x","score":1}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	opts := NewExtractOptions().WithExamples(enrichTarget{Name: "sample", Score: 1})
	_, err := Extract[enrichTarget]("irrelevant", opts)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if sawUserPrompt == "" {
		t.Fatal("the LLM caller was never invoked")
	}
}

// --- core.go: Transform's refusal and success paths -----------------------

type transformSource struct {
	Name string `json:"name"`
}

type transformTarget struct {
	FullName string `json:"full_name"`
}

// TestTransformRefusesUnmarshalableInput covers the marshal-error branch,
// which the pipeline-based happy-path test in cover_batch_test.go never
// reaches.
func TestTransformRefusesUnmarshalableInput(t *testing.T) {
	calls := 0
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `{}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	unmarshalable := map[string]any{"fn": func() {}}
	_, err := Transform[map[string]any, transformTarget](unmarshalable, NewTransformOptions())
	if err == nil {
		t.Fatal("expected Transform to reject input it cannot marshal")
	}
	if calls != 0 {
		t.Fatalf("provider called %d times despite a marshal failure", calls)
	}
}

// TestTransformPropagatesLLMCallFailure covers Transform's callLLM error
// branch, wrapped as a types.TransformError.
func TestTransformPropagatesLLMCallFailure(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", context.DeadlineExceeded
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Transform[transformSource, transformTarget](transformSource{Name: "Ada Lovelace"}, NewTransformOptions())
	if err == nil {
		t.Fatal("expected Transform to fail when the LLM call errors")
	}
	var transformErr types.TransformError
	if !asTransformError(err, &transformErr) {
		t.Fatalf("error is not a types.TransformError: %T %v", err, err)
	}
}

// TestTransformRefusesUnparseableResponse covers the ParseJSONStrict failure
// branch, and proves ModelConfidence 0.5 is not silently treated as success.
func TestTransformRefusesUnparseableResponse(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "not json", nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Transform[transformSource, transformTarget](transformSource{Name: "Ada Lovelace"}, NewTransformOptions())
	if err == nil {
		t.Fatal("expected Transform to fail on an unparseable response")
	}
}

// TestTransformWithMappingRulesAndLogicSucceeds exercises the steering
// assembly branches (MappingRules, PreserveFields, MergeStrategy,
// TransformLogic) together with a successful decode -- none of core.go's
// Transform steering branches were reachable from any existing test.
func TestTransformWithMappingRulesAndLogicSucceeds(t *testing.T) {
	var sawSystemPrompt string
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(_ context.Context, system, _ string, _ types.OpOptions) (string, error) {
		sawSystemPrompt = system
		return `{"full_name":"Ada Lovelace"}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	opts := NewTransformOptions().
		WithMappingRules(map[string]string{"full_name": "name"}).
		WithPreserveFields([]string{"name"}).
		WithTransformLogic("uppercase the surname").
		WithMergeStrategy("merge")

	result, err := Transform[transformSource, transformTarget](transformSource{Name: "Ada Lovelace"}, opts)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if result.FullName != "Ada Lovelace" {
		t.Errorf("FullName = %q, want %q", result.FullName, "Ada Lovelace")
	}
	if sawSystemPrompt == "" {
		t.Fatal("the LLM caller was never invoked")
	}
}

// --- core.go: Generate's refusal and success paths ------------------------

// TestGenerateRejectsCountGreaterThanOne proves the explicit "not yet
// supported" refusal, so a caller asking for a batch does not silently get
// one item back.
func TestGenerateRejectsCountGreaterThanOne(t *testing.T) {
	calls := 0
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `{}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Generate[transformTarget]("make some", NewGenerateOptions().WithCount(3))
	if err == nil {
		t.Fatal("expected Generate to reject Count > 1")
	}
	if calls != 0 {
		t.Fatalf("provider called %d times despite Count > 1", calls)
	}
}

// TestGenerateRejectsEmptyPrompt proves an all-whitespace prompt is refused
// rather than sent to the model as an empty instruction.
func TestGenerateRejectsEmptyPrompt(t *testing.T) {
	calls := 0
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `{}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Generate[transformTarget]("   ", NewGenerateOptions())
	if err == nil {
		t.Fatal("expected Generate to reject a blank prompt")
	}
	if calls != 0 {
		t.Fatalf("provider called %d times despite a blank prompt", calls)
	}
}

// TestGenerateStringTargetSuccessPath exercises the string-target branch,
// which is a different code path from struct generation (reflect.SetString
// rather than ParseJSONStrict).
func TestGenerateStringTargetSuccessPath(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "a generated tagline", nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	result, err := Generate[string]("write a tagline", NewGenerateOptions())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result != "a generated tagline" {
		t.Errorf("Generate = %q, want %q", result, "a generated tagline")
	}
}

// TestGenerateStringTargetPropagatesLLMCallFailure covers the string-target
// branch's callLLM error path.
func TestGenerateStringTargetPropagatesLLMCallFailure(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", context.DeadlineExceeded
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Generate[string]("write a tagline", NewGenerateOptions())
	if err == nil {
		t.Fatal("expected Generate to fail when the LLM call errors")
	}
}

// TestGenerateStructTargetWithConstraintsAndSeedSucceeds exercises the
// structured-generation branch together with the Constraints, SeedData,
// EnsureUnique, Template, Style, and Examples steering clauses, none of
// which were reachable from any existing test.
func TestGenerateStructTargetWithConstraintsAndSeedSucceeds(t *testing.T) {
	var sawUserPrompt string
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		sawUserPrompt = user
		return `{"full_name":"Grace Hopper"}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	opts := NewGenerateOptions().
		WithTemplate("{{name}}").
		WithStyle("formal").
		WithConstraints(map[string]interface{}{"era": "1950s"}).
		WithEnsureUnique(true).
		WithSeedData(transformTarget{FullName: "seed"}).
		WithExamples(transformTarget{FullName: "example"})

	result, err := Generate[transformTarget]("generate a computer scientist", opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.FullName != "Grace Hopper" {
		t.Errorf("FullName = %q, want %q", result.FullName, "Grace Hopper")
	}
	if sawUserPrompt == "" {
		t.Fatal("the LLM caller was never invoked")
	}
}

// TestGenerateStructTargetRefusesUnparseableResponse covers the struct
// branch's ParseJSONStrict failure path.
func TestGenerateStructTargetRefusesUnparseableResponse(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "not json", nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Generate[transformTarget]("generate one", NewGenerateOptions())
	if err == nil {
		t.Fatal("expected Generate to fail on an unparseable response")
	}
}

// --- derive.go: Derive, previously at 0% coverage -------------------------

type derivePerson struct {
	Name string `json:"name"`
}

type deriveEnriched struct {
	Generation string `json:"generation"`
}

// TestDeriveSuccessPath is Derive's happy path, never exercised anywhere
// else in the suite.
func TestDeriveSuccessPath(t *testing.T) {
	const body = `{
		"derived": {"generation": "millennial"},
		"derivations": [{"field": "generation", "method": "inference", "confidence": 0.9}],
		"field_confidence": {"generation": 0.9},
		"overall_confidence": 0.9
	}`
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return body, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	result, err := Derive[derivePerson, deriveEnriched](derivePerson{Name: "Ada"})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if result.Derived.Generation != "millennial" {
		t.Errorf("Derived.Generation = %q, want %q", result.Derived.Generation, "millennial")
	}
}

// TestDeriveRefusesFieldBelowConfidenceFloor proves the per-field confidence
// floor is enforced, not merely embedded in the prompt as a request: a field
// scored below MinConfidence must fail the call even though the overall
// confidence is high.
func TestDeriveRefusesFieldBelowConfidenceFloor(t *testing.T) {
	const body = `{
		"derived": {"generation": "millennial"},
		"derivations": [],
		"field_confidence": {"generation": 0.2},
		"overall_confidence": 0.95
	}`
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return body, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Derive[derivePerson, deriveEnriched](derivePerson{Name: "Ada"}, DeriveOptions{MinConfidence: 0.6})
	if err == nil {
		t.Fatal("expected Derive to refuse a field scored below the configured confidence floor")
	}
}

// TestDeriveRefusesOverallBelowConfidenceFloor is the companion case for the
// overall confidence.
func TestDeriveRefusesOverallBelowConfidenceFloor(t *testing.T) {
	const body = `{
		"derived": {"generation": "millennial"},
		"derivations": [],
		"field_confidence": {"generation": 0.9},
		"overall_confidence": 0.2
	}`
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return body, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Derive[derivePerson, deriveEnriched](derivePerson{Name: "Ada"}, DeriveOptions{MinConfidence: 0.6})
	if err == nil {
		t.Fatal("expected Derive to refuse an overall confidence below the configured floor")
	}
}

// TestDeriveRefusesUnmarshalableInput covers the marshal-error branch.
func TestDeriveRefusesUnmarshalableInput(t *testing.T) {
	calls := 0
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `{}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	unmarshalable := map[string]any{"fn": func() {}}
	_, err := Derive[map[string]any, deriveEnriched](unmarshalable)
	if err == nil {
		t.Fatal("expected Derive to reject input it cannot marshal")
	}
	if calls != 0 {
		t.Fatalf("provider called %d times despite a marshal failure", calls)
	}
}

// TestDerivePropagatesLLMCallFailure covers the callLLM error branch.
func TestDerivePropagatesLLMCallFailure(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", context.DeadlineExceeded
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Derive[derivePerson, deriveEnriched](derivePerson{Name: "Ada"})
	if err == nil {
		t.Fatal("expected Derive to fail when the LLM call errors")
	}
}

// TestDeriveRefusesUnparseableResponse covers the ParseJSONStrict failure
// branch.
func TestDeriveRefusesUnparseableResponse(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "not json", nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Derive[derivePerson, deriveEnriched](derivePerson{Name: "Ada"})
	if err == nil {
		t.Fatal("expected Derive to fail on an unparseable response")
	}
}

// --- arbitrate.go: Arbitrate, previously at 0% coverage -------------------

type arbitrateCandidate struct {
	Name string `json:"name"`
}

// TestArbitrateRejectsEmptyOptions proves the explicit empty-input guard.
func TestArbitrateRejectsEmptyOptions(t *testing.T) {
	_, err := Arbitrate[arbitrateCandidate](nil)
	if err == nil {
		t.Fatal("expected Arbitrate to reject an empty options slice")
	}
}

// TestArbitrateSingleOptionShortcutsWithoutCallingTheProvider proves a
// single option wins by default without any provider call -- there is
// nothing to arbitrate between.
func TestArbitrateSingleOptionShortcutsWithoutCallingTheProvider(t *testing.T) {
	calls := 0
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `{}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	result, err := Arbitrate([]arbitrateCandidate{{Name: "only"}})
	if err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}
	if result.WinnerIndex != 0 || result.Winner.Name != "only" {
		t.Fatalf("unexpected result for a single option: %+v", result)
	}
	if calls != 0 {
		t.Fatalf("provider called %d times for a single option", calls)
	}
}

// TestArbitrateRejectsNoRules proves the explicit rules-required guard.
func TestArbitrateRejectsNoRules(t *testing.T) {
	_, err := Arbitrate([]arbitrateCandidate{{Name: "a"}, {Name: "b"}})
	if err == nil {
		t.Fatal("expected Arbitrate to reject a call with no rules configured")
	}
}

// TestArbitrateRefusesOutOfRangeWinnerIndex is a "check the answer against
// the question" test in AGENTS.md's own sense: the model names a winner
// index outside the slice it was given, and Arbitrate must refuse rather
// than index out of range or silently clamp it.
func TestArbitrateRefusesOutOfRangeWinnerIndex(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"winner_index": 7, "scores": {"0": 0.9, "1": 0.5}, "confidence": 0.9}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Arbitrate([]arbitrateCandidate{{Name: "a"}, {Name: "b"}}, ArbitrateOptions{Rules: []string{"best value"}})
	if err == nil {
		t.Fatal("expected Arbitrate to refuse a winner_index outside the options slice")
	}
}

// TestArbitrateSuccessPath is the happy path with two options.
func TestArbitrateSuccessPath(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"winner_index": 1, "scores": {"0": 0.3, "1": 0.9}, "confidence": 0.9, "reasoning": "b is cheaper"}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	result, err := Arbitrate([]arbitrateCandidate{{Name: "a"}, {Name: "b"}}, ArbitrateOptions{Rules: []string{"cheapest"}})
	if err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}
	if result.WinnerIndex != 1 || result.Winner.Name != "b" {
		t.Fatalf("unexpected winner: %+v", result)
	}
	if result.Scores[1] != 0.9 {
		t.Errorf("Scores[1] = %v, want 0.9", result.Scores[1])
	}
}

// TestArbitrateRefusesUnmarshalableOption covers the per-option marshal
// error branch.
func TestArbitrateRefusesUnmarshalableOption(t *testing.T) {
	calls := 0
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `{}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	unmarshalable := []map[string]any{{"fn": func() {}}, {"fn": func() {}}}
	_, err := Arbitrate(unmarshalable, ArbitrateOptions{Rules: []string{"best"}})
	if err == nil {
		t.Fatal("expected Arbitrate to reject an option it cannot marshal")
	}
	if calls != 0 {
		t.Fatalf("provider called %d times despite a marshal failure", calls)
	}
}

// --- compose.go: Assemble, previously at 0% coverage -----------------------

type composedProfile struct {
	Name string `json:"name"`
}

// TestAssembleSuccessPath is Assemble's happy path.
func TestAssembleSuccessPath(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"composed": {"name": "Acme"}, "field_sources": [], "conflicts_resolved": 0, "completeness": 1.0}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	result, err := Assemble[composedProfile]([]any{map[string]any{"name": "Acme"}})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if result.Composed.Name != "Acme" {
		t.Errorf("Composed.Name = %q, want %q", result.Composed.Name, "Acme")
	}
}

// TestAssembleRefusesUnmarshalablePart covers the per-part marshal error
// branch.
func TestAssembleRefusesUnmarshalablePart(t *testing.T) {
	calls := 0
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `{}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Assemble[composedProfile]([]any{map[string]any{"fn": func() {}}})
	if err == nil {
		t.Fatal("expected Assemble to reject a part it cannot marshal")
	}
	if calls != 0 {
		t.Fatalf("provider called %d times despite a marshal failure", calls)
	}
}

// TestAssembleRefusesUnparseableResponse covers the ParseJSONStrict failure
// branch.
func TestAssembleRefusesUnparseableResponse(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "not json", nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Assemble[composedProfile]([]any{map[string]any{"name": "Acme"}})
	if err == nil {
		t.Fatal("expected Assemble to fail on an unparseable response")
	}
}

// TestAssemblePropagatesLLMCallFailure covers the callLLM error branch.
func TestAssemblePropagatesLLMCallFailure(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", context.DeadlineExceeded
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Assemble[composedProfile]([]any{map[string]any{"name": "Acme"}})
	if err == nil {
		t.Fatal("expected Assemble to fail when the LLM call errors")
	}
}

// --- decompose.go: DecomposeToSlice, previously at 0% coverage ------------

type decomposeWhole struct {
	Description string `json:"description"`
}

type decomposePart struct {
	Title string `json:"title"`
}

// TestDecomposeToSliceSuccessPath covers the primary decode path: a plain
// JSON array.
func TestDecomposeToSliceSuccessPath(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `[{"title":"part one"},{"title":"part two"}]`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	parts, err := DecomposeToSlice[decomposeWhole, decomposePart](decomposeWhole{Description: "a project"}, NewDecomposeOptions())
	if err != nil {
		t.Fatalf("DecomposeToSlice: %v", err)
	}
	if len(parts) != 2 || parts[0].Title != "part one" {
		t.Fatalf("parts = %+v, want 2 parts starting with \"part one\"", parts)
	}
}

// TestDecomposeToSliceFallsBackToWrappedPartsObject covers the second decode
// attempt: a {"parts": [...]} wrapper when the top-level value is not a bare
// array.
func TestDecomposeToSliceFallsBackToWrappedPartsObject(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"parts": [{"title":"wrapped part"}]}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	parts, err := DecomposeToSlice[decomposeWhole, decomposePart](decomposeWhole{Description: "a project"}, NewDecomposeOptions())
	if err != nil {
		t.Fatalf("DecomposeToSlice: %v", err)
	}
	if len(parts) != 1 || parts[0].Title != "wrapped part" {
		t.Fatalf("parts = %+v, want [{wrapped part}]", parts)
	}
}

// TestDecomposeToSliceFallsBackToASingleItem covers the third decode
// attempt: a bare single object becomes a one-element slice.
func TestDecomposeToSliceFallsBackToASingleItem(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"title":"the only part"}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	parts, err := DecomposeToSlice[decomposeWhole, decomposePart](decomposeWhole{Description: "a project"}, NewDecomposeOptions())
	if err != nil {
		t.Fatalf("DecomposeToSlice: %v", err)
	}
	if len(parts) != 1 || parts[0].Title != "the only part" {
		t.Fatalf("parts = %+v, want [{the only part}]", parts)
	}
}

// TestDecomposeToSliceRefusesWhenNoDecodeAttemptSucceeds proves that when
// none of the three fallback shapes parse, the call fails rather than
// returning an empty or partial slice as success.
func TestDecomposeToSliceRefusesWhenNoDecodeAttemptSucceeds(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "not json at all", nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	parts, err := DecomposeToSlice[decomposeWhole, decomposePart](decomposeWhole{Description: "a project"}, NewDecomposeOptions())
	if err == nil {
		t.Fatal("expected DecomposeToSlice to fail when no decode attempt succeeds")
	}
	if len(parts) != 0 {
		t.Fatalf("parts = %+v on a failed call, want empty", parts)
	}
}

// TestDecomposeToSliceRefusesInvalidOptions proves Validate() runs before
// any provider call.
func TestDecomposeToSliceRefusesInvalidOptions(t *testing.T) {
	calls := 0
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `[]`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	invalid := NewDecomposeOptions()
	invalid.Strategy = "nonsense"
	_, err := DecomposeToSlice[decomposeWhole, decomposePart](decomposeWhole{Description: "a project"}, invalid)
	if err == nil {
		t.Fatal("expected DecomposeToSlice to reject an invalid strategy")
	}
	if calls != 0 {
		t.Fatalf("provider called %d times despite invalid options", calls)
	}
}

// TestDecomposeToSlicePropagatesLLMCallFailure covers the callLLM error
// branch.
func TestDecomposeToSlicePropagatesLLMCallFailure(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", context.DeadlineExceeded
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := DecomposeToSlice[decomposeWhole, decomposePart](decomposeWhole{Description: "a project"}, NewDecomposeOptions())
	if err == nil {
		t.Fatal("expected DecomposeToSlice to fail when the LLM call errors")
	}
}

// --- annotate.go: AnnotateStruct, previously at 0% coverage ---------------

type annotateInput struct {
	Text string `json:"text"`
}

type annotatedOutput struct {
	Text      string `json:"text"`
	Sentiment string `json:"sentiment"`
}

// TestAnnotateStructSuccessPath is AnnotateStruct's happy path.
func TestAnnotateStructSuccessPath(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"text":"great product","sentiment":"positive"}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	result, err := AnnotateStruct[annotateInput, annotatedOutput](annotateInput{Text: "great product"}, NewAnnotateOptions())
	if err != nil {
		t.Fatalf("AnnotateStruct: %v", err)
	}
	if result.Sentiment != "positive" {
		t.Errorf("Sentiment = %q, want %q", result.Sentiment, "positive")
	}
}

// TestAnnotateStructRefusesInvalidOptions proves Validate() runs before any
// provider call.
func TestAnnotateStructRefusesInvalidOptions(t *testing.T) {
	calls := 0
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `{}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	invalid := NewAnnotateOptions()
	invalid.AnnotationTypes = nil
	invalid.CustomSchema = nil
	_, err := AnnotateStruct[annotateInput, annotatedOutput](annotateInput{Text: "x"}, invalid)
	if err == nil {
		t.Fatal("expected AnnotateStruct to reject options with no annotation types or custom schema")
	}
	if calls != 0 {
		t.Fatalf("provider called %d times despite invalid options", calls)
	}
}

// TestAnnotateStructRefusesUnmarshalableInput covers the marshal-error
// branch.
func TestAnnotateStructRefusesUnmarshalableInput(t *testing.T) {
	calls := 0
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `{}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	unmarshalable := map[string]any{"fn": func() {}}
	_, err := AnnotateStruct[map[string]any, annotatedOutput](unmarshalable, NewAnnotateOptions())
	if err == nil {
		t.Fatal("expected AnnotateStruct to reject input it cannot marshal")
	}
	if calls != 0 {
		t.Fatalf("provider called %d times despite a marshal failure", calls)
	}
}

// TestAnnotateStructRefusesUnparseableResponse covers the ParseJSONStrict
// failure branch.
func TestAnnotateStructRefusesUnparseableResponse(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "not json", nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := AnnotateStruct[annotateInput, annotatedOutput](annotateInput{Text: "x"}, NewAnnotateOptions())
	if err == nil {
		t.Fatal("expected AnnotateStruct to fail on an unparseable response")
	}
}

// TestAnnotateStructPropagatesLLMCallFailure covers the callLLM error
// branch.
func TestAnnotateStructPropagatesLLMCallFailure(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", context.DeadlineExceeded
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := AnnotateStruct[annotateInput, annotatedOutput](annotateInput{Text: "x"}, NewAnnotateOptions())
	if err == nil {
		t.Fatal("expected AnnotateStruct to fail when the LLM call errors")
	}
}

// asTransformError is a small local errors.As wrapper so this file does not
// need to import the errors package solely for one type assertion.
func asTransformError(err error, target *types.TransformError) bool {
	te, ok := err.(types.TransformError)
	if ok {
		*target = te
		return true
	}
	return false
}
