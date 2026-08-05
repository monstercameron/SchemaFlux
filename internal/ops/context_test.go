package ops

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Every options struct accepts a Context and every builder exposes a
// Context(...) method. Twenty-nine operations then wrote
// context.WithTimeout(context.Background(), ...) and ignored it, so caller
// cancellation did nothing — an abandoned HTTP request kept paying for tokens —
// and a deadline the caller set was replaced by the library's own.

// cancelledContext returns a context that is already done.
func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// observeContext records whether the operation's call saw a cancelled context.
func observeContext(t *testing.T, run func()) (called bool, sawCancellation bool) {
	t.Helper()

	previous := customLLMCaller
	setLLMCaller(func(ctx context.Context, _, _ string, _ types.OpOptions) (string, error) {
		called = true
		if ctx != nil && ctx.Err() != nil {
			sawCancellation = true
			return "", ctx.Err()
		}
		return "{}", nil
	})
	defer func() { customLLMCaller = previous }()

	run()
	return called, sawCancellation
}

// A cancelled caller context has to reach the provider call. These drive the
// operations through their real entry points with the context set the way a
// caller sets it.
func TestCallerContextReachesTheProviderCall(t *testing.T) {
	type record struct {
		Alpha string `json:"alpha"`
	}
	items := []record{{Alpha: "a"}, {Alpha: "b"}}

	cases := []struct {
		name string
		run  func(context.Context)
	}{
		{"Extract", func(ctx context.Context) {
			opts := NewExtractOptions()
			opts.OpOptions.Context = ctx
			_, _ = Extract[record]("input", opts)
		}},
		{"Choose", func(ctx context.Context) {
			opts := NewChooseOptions().WithSteering("pick")
			opts.OpOptions.Context = ctx
			_, _ = Choose(items, opts)
		}},
		{"Filter", func(ctx context.Context) {
			opts := NewFilterOptions().WithCriteria("any")
			opts.OpOptions.Context = ctx
			_, _ = Filter(items, opts)
		}},
		{"Sort", func(ctx context.Context) {
			opts := NewSortOptions().WithSteering("by alpha")
			opts.OpOptions.Context = ctx
			_, _ = Sort(items, opts)
		}},
		{"Summarize", func(ctx context.Context) {
			opts := NewSummarizeOptions()
			opts.OpOptions.Context = ctx
			_, _ = SummarizeWithMetadata("some text", opts)
		}},
		{"Rewrite", func(ctx context.Context) {
			opts := NewRewriteOptions()
			opts.OpOptions.Context = ctx
			_, _ = RewriteWithMetadata("some text", opts)
		}},
		{"Translate", func(ctx context.Context) {
			opts := NewTranslateOptions().WithTargetLanguage("fr")
			opts.OpOptions.Context = ctx
			_, _ = TranslateWithMetadata("some text", opts)
		}},
		{"Expand", func(ctx context.Context) {
			opts := NewExpandOptions()
			opts.OpOptions.Context = ctx
			_, _ = ExpandWithMetadata("some text", opts)
		}},
		{"Validate", func(ctx context.Context) {
			// ValidateOptions embeds CommonOptions rather than OpOptions, which
			// is F-05's inconsistency showing through: the same setting lives
			// at a different path depending on the operation.
			opts := NewValidateOptions().WithRules("any")
			opts.CommonOptions.Context = ctx
			_, _ = Validate(record{}, opts)
		}},
		{"Explain", func(ctx context.Context) {
			opts := NewExplainOptions()
			opts.OpOptions.Context = ctx
			_, _ = Explain(items[0], opts)
		}},
		{"Parse", func(ctx context.Context) {
			opts := NewParseOptions()
			opts.OpOptions.Context = ctx
			_, _ = Parse[record]("some input", opts)
		}},
		{"Infer", func(ctx context.Context) {
			opts := NewInferOptions()
			opts.OpOptions.Context = ctx
			_, _ = Infer(items[0], opts)
		}},
		{"Suggest", func(ctx context.Context) {
			opts := NewSuggestOptions()
			opts.OpOptions.Context = ctx
			_, _ = Suggest[string]("input", opts)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called, sawCancellation := observeContext(t, func() { tc.run(cancelledContext()) })
			if !called {
				t.Skip("this operation made no provider call in this configuration")
			}
			if !sawCancellation {
				t.Error("the operation's call did not see the caller's cancellation")
			}
		})
	}
}

// Decide, Match, and Guard take a context.Context directly.
func TestControlFlowOperationsHonourTheirContext(t *testing.T) {
	cases := []struct {
		name string
		run  func(context.Context)
	}{
		{"Decide", func(ctx context.Context) {
			_, _, _ = Decide(ctx, "situation",
				[]Decision[string]{{Value: "a", Description: "first"}, {Value: "b", Description: "second"}})
		}},
		{"Match", func(ctx context.Context) {
			_, _ = Match(ctx, "input", Like("a condition", func() {}))
		}},
		{"Guard", func(ctx context.Context) {
			_ = Guard(ctx, "state", func(string) (bool, string) { return false, "failed" })
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called, sawCancellation := observeContext(t, func() { tc.run(cancelledContext()) })
			if !called {
				t.Skip("this operation made no provider call")
			}
			if !sawCancellation {
				t.Error("the operation's call did not see the caller's cancellation")
			}
		})
	}
}

// operationContext is the mechanism.
func TestOperationContext(t *testing.T) {
	t.Run("nil_caller_is_usable", func(t *testing.T) {
		ctx, cancel := operationContext(nil, time.Second)
		defer cancel()
		if ctx == nil || ctx.Err() != nil {
			t.Errorf("ctx = %v, err = %v", ctx, ctx.Err())
		}
	})

	t.Run("cancellation_propagates", func(t *testing.T) {
		ctx, cancel := operationContext(cancelledContext(), time.Hour)
		defer cancel()
		if ctx.Err() == nil {
			t.Error("a cancelled caller context must cancel the derived one")
		}
	})

	t.Run("caller_deadline_wins_when_sooner", func(t *testing.T) {
		caller, cancelCaller := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancelCaller()

		ctx, cancel := operationContext(caller, time.Hour)
		defer cancel()

		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("no deadline")
		}
		if time.Until(deadline) > time.Minute {
			t.Errorf("the library's hour replaced the caller's 10ms: %v", time.Until(deadline))
		}
	})

	t.Run("library_timeout_applies_when_caller_has_none", func(t *testing.T) {
		ctx, cancel := operationContext(context.Background(), 50*time.Millisecond)
		defer cancel()

		if _, ok := ctx.Deadline(); !ok {
			t.Error("no deadline was applied")
		}
	})

	t.Run("zero_timeout_falls_back_to_the_configured_one", func(t *testing.T) {
		ctx, cancel := operationContext(context.Background(), 0)
		defer cancel()

		if _, ok := ctx.Deadline(); !ok {
			t.Error("a zero timeout must still produce a deadline")
		}
	})
}

// The pattern is easy to reintroduce, so it is checked rather than remembered.
func TestNoOperationDiscardsTheCallerContext(t *testing.T) {
	fileSet := token.NewFileSet()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	offenders := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fileSet, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "WithTimeout" {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || pkg.Name != "context" || len(call.Args) == 0 {
				return true
			}

			// context.WithTimeout(context.Background(), ...) throws the
			// caller's context away.
			inner, ok := call.Args[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			innerSelector, ok := inner.Fun.(*ast.SelectorExpr)
			if !ok || innerSelector.Sel.Name != "Background" {
				return true
			}

			offenders++
			t.Errorf("%s:%d: context.WithTimeout(context.Background(), ...) discards the caller's context; use operationContext",
				name, fileSet.Position(call.Pos()).Line)
			return true
		})
	}

	if offenders == 0 {
		t.Log("no operation discards the caller's context")
	}
}
