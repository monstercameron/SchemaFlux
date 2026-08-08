package tests

import (
	"context"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

// These five came from the client package's own tests. They drive the public
// builders, so they cannot live beside the client: the client package is
// imported BY the root package, and a test there importing the root back would
// be an import cycle. They were black-box tests wearing a white-box location.

func TestExtractingBuilderAppliesCommonAndOperationOptions(t *testing.T) {
	ctx := context.Background()
	var seen schemaflux.ExtractOptions
	_ = schemaflux.Extracting[struct {
		Name string `json:"name"`
	}]("John Doe").
		Strict().
		Smart().
		Steer("focus on the contact record").
		Threshold(0.9).
		Context(ctx).
		RequestID("req-123").
		CorrelationID("corr-123").
		Partial(false).
		SchemaHints(map[string]string{"name": "Full legal name"}).
		Configure(func(opts schemaflux.ExtractOptions) schemaflux.ExtractOptions {
			seen = opts
			return opts
		})

	if seen.CommonOptions.Mode != schemaflux.Strict {
		t.Fatalf("expected strict mode, got %v", seen.CommonOptions.Mode)
	}
	if seen.CommonOptions.Intelligence != schemaflux.Smart {
		t.Fatalf("expected smart intelligence, got %v", seen.CommonOptions.Intelligence)
	}
	if seen.CommonOptions.Steering != "focus on the contact record" {
		t.Fatalf("unexpected steering: %q", seen.CommonOptions.Steering)
	}
	if seen.CommonOptions.Threshold != 0.9 {
		t.Fatalf("expected threshold 0.9, got %v", seen.CommonOptions.Threshold)
	}
	if seen.CommonOptions.Context != ctx {
		t.Fatal("expected context to be preserved")
	}
	if seen.CommonOptions.RequestID != "req-123" {
		t.Fatalf("unexpected request id: %q", seen.CommonOptions.RequestID)
	}
	if seen.CommonOptions.CorrelationID != "corr-123" {
		t.Fatalf("unexpected correlation id: %q", seen.CommonOptions.CorrelationID)
	}
	if seen.AllowPartial {
		t.Fatal("expected partial extraction to be disabled")
	}
	if seen.SchemaHints["name"] != "Full legal name" {
		t.Fatalf("unexpected schema hint: %#v", seen.SchemaHints)
	}
}

func TestGenerateAndTransformBuildersAreConfigurable(t *testing.T) {
	var genOpts schemaflux.GenerateOptions
	_ = schemaflux.Generating[string]("write a release note").
		Creative().
		Quick().
		Style("concise").
		Count(2).
		Configure(func(opts schemaflux.GenerateOptions) schemaflux.GenerateOptions {
			genOpts = opts
			return opts
		})

	if genOpts.CommonOptions.Mode != schemaflux.Creative {
		t.Fatalf("expected creative mode, got %v", genOpts.CommonOptions.Mode)
	}
	if genOpts.CommonOptions.Intelligence != schemaflux.Quick {
		t.Fatalf("expected quick intelligence, got %v", genOpts.CommonOptions.Intelligence)
	}
	if genOpts.Style != "concise" {
		t.Fatalf("unexpected style: %q", genOpts.Style)
	}
	if genOpts.Count != 2 {
		t.Fatalf("unexpected count: %d", genOpts.Count)
	}

	type source struct{ Name string }
	type target struct{ Label string }

	var transformOpts schemaflux.TransformOptions
	_ = schemaflux.Transforming[source, target](source{Name: "foo"}).
		Strict().
		Fast().
		Merge("merge").
		Steer("preserve identifiers").
		Configure(func(opts schemaflux.TransformOptions) schemaflux.TransformOptions {
			transformOpts = opts
			return opts
		})

	if transformOpts.CommonOptions.Mode != schemaflux.Strict {
		t.Fatalf("expected strict mode, got %v", transformOpts.CommonOptions.Mode)
	}
	if transformOpts.CommonOptions.Intelligence != schemaflux.Fast {
		t.Fatalf("expected fast intelligence, got %v", transformOpts.CommonOptions.Intelligence)
	}
	if transformOpts.MergeStrategy != "merge" {
		t.Fatalf("unexpected merge strategy: %q", transformOpts.MergeStrategy)
	}
	if transformOpts.CommonOptions.Steering != "preserve identifiers" {
		t.Fatalf("unexpected steering: %q", transformOpts.CommonOptions.Steering)
	}
}

func TestCollectionBuildersStayFluent(t *testing.T) {
	var chooseOpts schemaflux.ChooseOptions
	_ = schemaflux.Choosing([]string{"a", "b"}).
		By("best quality", "lowest cost").
		Smart().
		Reasoning(false).
		Top(2).
		Steer("prefer durable options").
		Configure(func(opts schemaflux.ChooseOptions) schemaflux.ChooseOptions {
			chooseOpts = opts
			return opts
		})

	if len(chooseOpts.Criteria) != 2 {
		t.Fatalf("expected 2 criteria, got %d", len(chooseOpts.Criteria))
	}
	if chooseOpts.CommonOptions.Intelligence != schemaflux.Smart {
		t.Fatalf("expected smart intelligence, got %v", chooseOpts.CommonOptions.Intelligence)
	}
	if chooseOpts.RequireReasoning {
		t.Fatal("expected reasoning to be disabled")
	}
	if chooseOpts.TopN != 2 {
		t.Fatalf("expected top 2, got %d", chooseOpts.TopN)
	}
	if chooseOpts.CommonOptions.Steering != "prefer durable options" {
		t.Fatalf("unexpected steering: %q", chooseOpts.CommonOptions.Steering)
	}

	var filterOpts schemaflux.FilterOptions
	_ = schemaflux.Filtering([]string{"a", "b"}).
		By("urgent only").
		Quick().
		KeepMatching(false).
		MinConfidence(0.95).
		Steer("drop backlog items").
		Configure(func(opts schemaflux.FilterOptions) schemaflux.FilterOptions {
			filterOpts = opts
			return opts
		})

	if filterOpts.Criteria != "urgent only" {
		t.Fatalf("unexpected criteria: %q", filterOpts.Criteria)
	}
	if filterOpts.CommonOptions.Intelligence != schemaflux.Quick {
		t.Fatalf("expected quick intelligence, got %v", filterOpts.CommonOptions.Intelligence)
	}
	if filterOpts.KeepMatching {
		t.Fatal("expected keep matching to be false")
	}
	if filterOpts.MinConfidence != 0.95 {
		t.Fatalf("unexpected confidence: %v", filterOpts.MinConfidence)
	}
	if filterOpts.CommonOptions.Steering != "drop backlog items" {
		t.Fatalf("unexpected steering: %q", filterOpts.CommonOptions.Steering)
	}

	var sortOpts schemaflux.SortOptions
	_ = schemaflux.Sorting([]string{"a", "b"}).
		By("highest priority first").
		Fast().
		Desc().
		Steer("prioritize deadlines").
		Configure(func(opts schemaflux.SortOptions) schemaflux.SortOptions {
			sortOpts = opts
			return opts
		})

	if sortOpts.Criteria != "highest priority first" {
		t.Fatalf("unexpected criteria: %q", sortOpts.Criteria)
	}
	if sortOpts.CommonOptions.Intelligence != schemaflux.Fast {
		t.Fatalf("expected fast intelligence, got %v", sortOpts.CommonOptions.Intelligence)
	}
	if sortOpts.Direction != "descending" {
		t.Fatalf("unexpected direction: %q", sortOpts.Direction)
	}
	if sortOpts.CommonOptions.Steering != "prioritize deadlines" {
		t.Fatalf("unexpected steering: %q", sortOpts.CommonOptions.Steering)
	}
}

func TestExtendedBuildersExposeFluentConfiguration(t *testing.T) {
	var classifyOpts schemaflux.ClassifyOptions
	_ = schemaflux.Classifying[string, string]("refund requested").
		Categories("billing", "support").
		Smart().
		Steer("prefer the most actionable label").
		Configure(func(opts schemaflux.ClassifyOptions) schemaflux.ClassifyOptions {
			classifyOpts = opts
			return opts
		})
	if len(classifyOpts.Categories) != 2 {
		t.Fatalf("expected categories to be captured, got %#v", classifyOpts.Categories)
	}
	if classifyOpts.CommonOptions.Intelligence != schemaflux.Smart {
		t.Fatalf("expected smart intelligence, got %v", classifyOpts.CommonOptions.Intelligence)
	}

	var parseOpts schemaflux.ParseOptions
	_ = schemaflux.Parsing[map[string]any]("name|john").
		AllowLLMFallback(true).
		AutoFix(true).
		Quick().
		RequestID("parse-1").
		Configure(func(opts schemaflux.ParseOptions) schemaflux.ParseOptions {
			parseOpts = opts
			return opts
		})
	if !parseOpts.AllowLLMFallback || !parseOpts.AutoFix {
		t.Fatalf("expected parse flags to be enabled: %#v", parseOpts)
	}
	if parseOpts.OpOptions.Intelligence != schemaflux.Quick || parseOpts.OpOptions.RequestID != "parse-1" {
		t.Fatalf("expected parse op options to be updated: %#v", parseOpts.OpOptions)
	}

	var questionOpts schemaflux.QuestionOptions
	_ = schemaflux.Asking[string, string]("quarterly report", "What changed?").
		Strict().
		Steer("answer briefly").
		Configure(func(opts schemaflux.QuestionOptions) schemaflux.QuestionOptions {
			questionOpts = opts
			return opts
		})
	if questionOpts.Question != "What changed?" {
		t.Fatalf("unexpected question: %q", questionOpts.Question)
	}
	if questionOpts.CommonOptions.Mode != schemaflux.Strict || questionOpts.CommonOptions.Steering != "answer briefly" {
		t.Fatalf("unexpected question common options: %#v", questionOpts.CommonOptions)
	}
}

func TestDirectStyleBuildersExposeUnifiedControls(t *testing.T) {
	var resolveOpts schemaflux.ResolveOptions
	_ = schemaflux.Resolving([]string{"a", "b"}).
		Strategy("merge").
		Smart().
		RequestID("resolve-1").
		CorrelationID("corr-resolve").
		Steer("prefer the most complete record").
		Configure(func(opts schemaflux.ResolveOptions) schemaflux.ResolveOptions {
			resolveOpts = opts
			return opts
		})
	if resolveOpts.Strategy != "merge" {
		t.Fatalf("unexpected resolve strategy: %q", resolveOpts.Strategy)
	}
	if resolveOpts.RequestID != "resolve-1" || resolveOpts.CorrelationID != "corr-resolve" {
		t.Fatalf("unexpected resolve tracking fields: %#v", resolveOpts)
	}
	if resolveOpts.Intelligence != schemaflux.Smart || resolveOpts.Steering != "prefer the most complete record" {
		t.Fatalf("unexpected resolve direct options: %#v", resolveOpts)
	}

	var projectOpts schemaflux.ProjectOptions
	_ = schemaflux.Projecting[map[string]any, map[string]any](map[string]any{"id": 1}).
		Exclude("secret", "token").
		Fast().
		Configure(func(opts schemaflux.ProjectOptions) schemaflux.ProjectOptions {
			projectOpts = opts
			return opts
		})
	if len(projectOpts.Exclude) != 2 {
		t.Fatalf("expected projected exclude fields, got %#v", projectOpts.Exclude)
	}
	if projectOpts.Intelligence != schemaflux.Fast {
		t.Fatalf("expected fast intelligence, got %v", projectOpts.Intelligence)
	}
}

// --- from providerconfig_override_test.go ---

// Client.providerConfig copies a caller's schemaflux.ProviderConfig field by field, and a
// hand-written copy list cannot fail when somebody adds a field to the struct
// it copies. It had already silently lost two: EndpointPolicy (SEC-004) and
// HTTPClient (SC-007), both opt-in and both security-relevant, so a caller
// switching endpoint enforcement on through the public API got a nil policy and
// no enforcement, while the check one layer down looked implemented and tested.
//
// This is A-014's bug in a second place (applyDefaults, internal/ops), so it
// gets A-014's guard: walk the struct by reflection rather than naming fields,
// because the only moment a list needed to fail is the moment somebody extended
// the struct without extending the list.

// nonZeroProviderConfig fills every field with a distinguishable non-zero
// value. Adding a field to schemaflux.ProviderConfig without adding it here fails
// TestEveryProviderConfigFieldIsPopulatedInTheFixture, so the fixture cannot
// quietly fall behind the struct either.
