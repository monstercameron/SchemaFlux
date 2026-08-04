package schemaflux

import (
	"context"
	"testing"
)

func TestNewCommonOptionsDefaults(t *testing.T) {
	opts := NewCommonOptions()

	if opts.Mode != TransformMode {
		t.Fatalf("expected default mode %v, got %v", TransformMode, opts.Mode)
	}
	if opts.Intelligence != Fast {
		t.Fatalf("expected default intelligence %v, got %v", Fast, opts.Intelligence)
	}
}

func TestExtractingBuilderAppliesCommonAndOperationOptions(t *testing.T) {
	ctx := context.Background()
	var seen ExtractOptions
	_ = Extracting[struct {
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
		Configure(func(opts ExtractOptions) ExtractOptions {
			seen = opts
			return opts
		})

	if seen.CommonOptions.Mode != Strict {
		t.Fatalf("expected strict mode, got %v", seen.CommonOptions.Mode)
	}
	if seen.CommonOptions.Intelligence != Smart {
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
	var genOpts GenerateOptions
	_ = Generating[string]("write a release note").
		Creative().
		Quick().
		Style("concise").
		Count(2).
		Configure(func(opts GenerateOptions) GenerateOptions {
			genOpts = opts
			return opts
		})

	if genOpts.CommonOptions.Mode != Creative {
		t.Fatalf("expected creative mode, got %v", genOpts.CommonOptions.Mode)
	}
	if genOpts.CommonOptions.Intelligence != Quick {
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

	var transformOpts TransformOptions
	_ = Transforming[source, target](source{Name: "foo"}).
		Strict().
		Fast().
		Merge("merge").
		Steer("preserve identifiers").
		Configure(func(opts TransformOptions) TransformOptions {
			transformOpts = opts
			return opts
		})

	if transformOpts.CommonOptions.Mode != Strict {
		t.Fatalf("expected strict mode, got %v", transformOpts.CommonOptions.Mode)
	}
	if transformOpts.CommonOptions.Intelligence != Fast {
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
	var chooseOpts ChooseOptions
	_ = Choosing([]string{"a", "b"}).
		By("best quality", "lowest cost").
		Smart().
		Reasoning(false).
		Top(2).
		Steer("prefer durable options").
		Configure(func(opts ChooseOptions) ChooseOptions {
			chooseOpts = opts
			return opts
		})

	if len(chooseOpts.Criteria) != 2 {
		t.Fatalf("expected 2 criteria, got %d", len(chooseOpts.Criteria))
	}
	if chooseOpts.CommonOptions.Intelligence != Smart {
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

	var filterOpts FilterOptions
	_ = Filtering([]string{"a", "b"}).
		By("urgent only").
		Quick().
		KeepMatching(false).
		MinConfidence(0.95).
		Steer("drop backlog items").
		Configure(func(opts FilterOptions) FilterOptions {
			filterOpts = opts
			return opts
		})

	if filterOpts.Criteria != "urgent only" {
		t.Fatalf("unexpected criteria: %q", filterOpts.Criteria)
	}
	if filterOpts.CommonOptions.Intelligence != Quick {
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

	var sortOpts SortOptions
	_ = Sorting([]string{"a", "b"}).
		By("highest priority first").
		Fast().
		Desc().
		Steer("prioritize deadlines").
		Configure(func(opts SortOptions) SortOptions {
			sortOpts = opts
			return opts
		})

	if sortOpts.Criteria != "highest priority first" {
		t.Fatalf("unexpected criteria: %q", sortOpts.Criteria)
	}
	if sortOpts.CommonOptions.Intelligence != Fast {
		t.Fatalf("expected fast intelligence, got %v", sortOpts.CommonOptions.Intelligence)
	}
	if sortOpts.Direction != "descending" {
		t.Fatalf("unexpected direction: %q", sortOpts.Direction)
	}
	if sortOpts.CommonOptions.Steering != "prioritize deadlines" {
		t.Fatalf("unexpected steering: %q", sortOpts.CommonOptions.Steering)
	}
}

func TestCollectionOptionTypesExposeCommonBuilders(t *testing.T) {
	ctx := context.Background()

	chooseOpts := NewChooseOptions().
		WithMode(Strict).
		WithIntelligence(Smart).
		WithSteering("best fit").
		WithThreshold(0.8).
		WithContext(ctx).
		WithRequestID("choose-1")
	if chooseOpts.CommonOptions.Mode != Strict || chooseOpts.CommonOptions.Intelligence != Smart || chooseOpts.CommonOptions.Steering != "best fit" || chooseOpts.CommonOptions.Threshold != 0.8 || chooseOpts.CommonOptions.Context != ctx || chooseOpts.CommonOptions.RequestID != "choose-1" {
		t.Fatalf("choose options lost common builder state: %#v", chooseOpts)
	}

	filterOpts := NewFilterOptions().
		WithMode(Strict).
		WithIntelligence(Quick).
		WithSteering("keep only compliant").
		WithThreshold(0.7).
		WithContext(ctx).
		WithRequestID("filter-1")
	if filterOpts.CommonOptions.Mode != Strict || filterOpts.CommonOptions.Intelligence != Quick || filterOpts.CommonOptions.Steering != "keep only compliant" || filterOpts.CommonOptions.Threshold != 0.7 || filterOpts.CommonOptions.Context != ctx || filterOpts.CommonOptions.RequestID != "filter-1" {
		t.Fatalf("filter options lost common builder state: %#v", filterOpts)
	}

	sortOpts := NewSortOptions().
		WithMode(Strict).
		WithIntelligence(Fast).
		WithSteering("latest first").
		WithThreshold(0.6).
		WithContext(ctx).
		WithRequestID("sort-1")
	if sortOpts.CommonOptions.Mode != Strict || sortOpts.CommonOptions.Intelligence != Fast || sortOpts.CommonOptions.Steering != "latest first" || sortOpts.CommonOptions.Threshold != 0.6 || sortOpts.CommonOptions.Context != ctx || sortOpts.CommonOptions.RequestID != "sort-1" {
		t.Fatalf("sort options lost common builder state: %#v", sortOpts)
	}
}

func TestExtendedBuildersExposeFluentConfiguration(t *testing.T) {
	var classifyOpts ClassifyOptions
	_ = Classifying[string, string]("refund requested").
		Categories("billing", "support").
		Smart().
		Steer("prefer the most actionable label").
		Configure(func(opts ClassifyOptions) ClassifyOptions {
			classifyOpts = opts
			return opts
		})
	if len(classifyOpts.Categories) != 2 {
		t.Fatalf("expected categories to be captured, got %#v", classifyOpts.Categories)
	}
	if classifyOpts.CommonOptions.Intelligence != Smart {
		t.Fatalf("expected smart intelligence, got %v", classifyOpts.CommonOptions.Intelligence)
	}

	var parseOpts ParseOptions
	_ = Parsing[map[string]any]("name|john").
		AllowLLMFallback(true).
		AutoFix(true).
		Quick().
		RequestID("parse-1").
		Configure(func(opts ParseOptions) ParseOptions {
			parseOpts = opts
			return opts
		})
	if !parseOpts.AllowLLMFallback || !parseOpts.AutoFix {
		t.Fatalf("expected parse flags to be enabled: %#v", parseOpts)
	}
	if parseOpts.OpOptions.Intelligence != Quick || parseOpts.OpOptions.RequestID != "parse-1" {
		t.Fatalf("expected parse op options to be updated: %#v", parseOpts.OpOptions)
	}

	var questionOpts QuestionOptions
	_ = Asking[string, string]("quarterly report", "What changed?").
		Strict().
		Steer("answer briefly").
		Configure(func(opts QuestionOptions) QuestionOptions {
			questionOpts = opts
			return opts
		})
	if questionOpts.Question != "What changed?" {
		t.Fatalf("unexpected question: %q", questionOpts.Question)
	}
	if questionOpts.CommonOptions.Mode != Strict || questionOpts.CommonOptions.Steering != "answer briefly" {
		t.Fatalf("unexpected question common options: %#v", questionOpts.CommonOptions)
	}
}

func TestDirectStyleBuildersExposeUnifiedControls(t *testing.T) {
	var resolveOpts ResolveOptions
	_ = Resolving([]string{"a", "b"}).
		Strategy("merge").
		Smart().
		RequestID("resolve-1").
		CorrelationID("corr-resolve").
		Steer("prefer the most complete record").
		Configure(func(opts ResolveOptions) ResolveOptions {
			resolveOpts = opts
			return opts
		})
	if resolveOpts.Strategy != "merge" {
		t.Fatalf("unexpected resolve strategy: %q", resolveOpts.Strategy)
	}
	if resolveOpts.RequestID != "resolve-1" || resolveOpts.CorrelationID != "corr-resolve" {
		t.Fatalf("unexpected resolve tracking fields: %#v", resolveOpts)
	}
	if resolveOpts.Intelligence != Smart || resolveOpts.Steering != "prefer the most complete record" {
		t.Fatalf("unexpected resolve direct options: %#v", resolveOpts)
	}

	var projectOpts ProjectOptions
	_ = Projecting[map[string]any, map[string]any](map[string]any{"id": 1}).
		Exclude("secret", "token").
		Fast().
		Configure(func(opts ProjectOptions) ProjectOptions {
			projectOpts = opts
			return opts
		})
	if len(projectOpts.Exclude) != 2 {
		t.Fatalf("expected projected exclude fields, got %#v", projectOpts.Exclude)
	}
	if projectOpts.Intelligence != Fast {
		t.Fatalf("expected fast intelligence, got %v", projectOpts.Intelligence)
	}
}
