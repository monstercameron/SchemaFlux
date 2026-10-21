package otel

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	oteltrace "go.opentelemetry.io/otel/trace"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// failOnUndrivenFunctions makes a function of the root that begins a call and has
// no driver below fail the test. It is off while the root is being written, when
// a function lands every few minutes and each would fail this package for whoever
// ran it next; the list the test logs is the work that is left, and turning this
// on is the check that none is forgotten once the root's functions are settled.
const failOnUndrivenFunctions = false

// The package comment says the functions of the root that ask a model have an
// operation span of their own. This drives each one listed below with an
// Observer installed, and reads the root's source for the functions that begin a
// call, so the ones that are not listed are known.
func TestEveryFunctionOfTheRootThatBeginsACallHasAnOperationSpan(t *testing.T) {
	type Category string
	type triage struct {
		Spam    bool   `ask:"Is this message spam?"`
		Team    string `ask:"Which team should handle it?" oneof:"billing,technical,shipping"`
		Urgency int    `ask:"How urgent is it?" scale:"1-5"`
	}
	labels := []string{"billing", "support"}
	drivers := map[string]func(ctx context.Context){
		"Extract": func(ctx context.Context) { _, _ = schemaflux.Extract[invoice](ctx, "Invoice from Acme") },
		"Summarize": func(ctx context.Context) {
			_, _ = schemaflux.Summarize(ctx, "A long text about an invoice from Acme.")
		},
		"SummarizeDetail": func(ctx context.Context) {
			_, _ = schemaflux.SummarizeDetail(ctx, "A long text about an invoice from Acme.")
		},
		"Classify": func(ctx context.Context) {
			_, _ = schemaflux.Classify(ctx, "Invoice from Acme", []Category{"billing", "support"})
		},
		"Is":       func(ctx context.Context) { _, _ = schemaflux.Is(ctx, "Invoice from Acme", "Is it an invoice?") },
		"IsDetail": func(ctx context.Context) { _, _ = schemaflux.IsDetail(ctx, "Invoice from Acme", "Is it an invoice?") },
		"Compress": func(ctx context.Context) { _, _ = schemaflux.Compress(ctx, "A long text about an invoice from Acme.") },
		"Expand":   func(ctx context.Context) { _, _ = schemaflux.Expand(ctx, "Invoice from Acme.") },
		"ExpandDetail": func(ctx context.Context) {
			_, _ = schemaflux.ExpandDetail(ctx, "Invoice from Acme.")
		},
		"Format": func(ctx context.Context) {
			_, _ = schemaflux.Format(ctx, invoice{Number: "A1", Vendor: "Acme"}, "Invoice {number}")
		},
		"Merge": func(ctx context.Context) {
			_, _ = schemaflux.Merge(ctx, []invoice{{Number: "A1", Vendor: "Acme"}, {Number: "A1", Vendor: "ACME"}}, "prefer the first")
		},
		"Normalize": func(ctx context.Context) { _, _ = schemaflux.Normalize(ctx, invoice{Number: "a1", Vendor: "acme"}) },
		"Rewrite":   func(ctx context.Context) { _, _ = schemaflux.Rewrite(ctx, "Invoice from Acme.") },
		"RewriteDetail": func(ctx context.Context) {
			_, _ = schemaflux.RewriteDetail(ctx, "Invoice from Acme.")
		},
		"Which": func(ctx context.Context) { _, _ = schemaflux.Which(ctx, "Invoice from Acme", "Which team?", labels) },
		"WhichDetail": func(ctx context.Context) {
			_, _ = schemaflux.WhichDetail(ctx, "Invoice from Acme", "Which team?", labels)
		},
		"WhichOrNone": func(ctx context.Context) {
			_, _, _ = schemaflux.WhichOrNone(ctx, "Invoice from Acme", "Which team?", labels)
		},
		"WhichOrNoneDetail": func(ctx context.Context) {
			_, _, _ = schemaflux.WhichOrNoneDetail(ctx, "Invoice from Acme", "Which team?", labels)
		},
		"Rate":         func(ctx context.Context) { _, _ = schemaflux.Rate(ctx, "Invoice from Acme", "How urgent?", 1, 5) },
		"RateDetail":   func(ctx context.Context) { _, _ = schemaflux.RateDetail(ctx, "Invoice from Acme", "How urgent?", 1, 5) },
		"Assess":       func(ctx context.Context) { _, _ = schemaflux.Assess[triage](ctx, "Invoice from Acme") },
		"AssessDetail": func(ctx context.Context) { _, _ = schemaflux.AssessDetail[triage](ctx, "Invoice from Acme") },
		"Choose":       func(ctx context.Context) { _, _ = schemaflux.Choose(ctx, []string{"billing", "support"}) },
		"Filter": func(ctx context.Context) {
			_, _ = schemaflux.Filter(ctx, []string{"an invoice", "a letter"}, "is an invoice")
		},
		"Sort": func(ctx context.Context) { _, _ = schemaflux.Sort(ctx, []string{"b", "a", "c"}, "alphabetical") },
		"Rank": func(ctx context.Context) {
			_, _ = schemaflux.Rank(ctx, []string{"an invoice", "a letter"}, "invoices")
		},
		"SemanticMatch": func(ctx context.Context) {
			_, _ = schemaflux.SemanticMatch(ctx, []string{"an invoice"}, []string{"a bill", "a letter"})
		},
		"Validate": func(ctx context.Context) {
			_, _ = schemaflux.Validate(ctx, invoice{Number: "A1", Vendor: "Acme"}, schemaflux.ValidateOptions{Rules: "the vendor is a company"})
		},
		"Verify": func(ctx context.Context) { _, _ = schemaflux.Verify(ctx, "Acme sent invoice A1.") },
		"Audit":  func(ctx context.Context) { _, _ = schemaflux.Audit(ctx, invoice{Number: "A1", Vendor: "Acme"}) },
		"Critique": func(ctx context.Context) {
			_, _ = schemaflux.Critique(ctx, "Acme sent invoice A1.", []string{"clear", "short"})
		},
		"Diff": func(ctx context.Context) {
			_, _ = schemaflux.Diff(ctx, invoice{Number: "A1", Vendor: "Acme"}, invoice{Number: "A2", Vendor: "Acme"})
		},
		"Cluster": func(ctx context.Context) {
			_, _ = schemaflux.Cluster(ctx, []string{"an invoice", "a bill", "a letter", "a note"})
		},
		"Compare": func(ctx context.Context) {
			_, _ = schemaflux.Compare(ctx, invoice{Number: "A1", Vendor: "Acme"}, invoice{Number: "A2", Vendor: "Acme"})
		},
		"Similar":   func(ctx context.Context) { _, _ = schemaflux.Similar(ctx, "an invoice", "a bill") },
		"Translate": func(ctx context.Context) { _, _ = schemaflux.Translate(ctx, "Invoice from Acme.", "French") },
		"TranslateDetail": func(ctx context.Context) {
			_, _ = schemaflux.TranslateDetail(ctx, "Invoice from Acme.", "French")
		},
	}

	// The functions of the root that begin a call, read from its source.
	beginning := map[string]bool{}
	files, err := filepath.Glob("../../*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no source of the root found: %v", err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil || !function.Name.IsExported() || function.Recv != nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok {
					if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Begin" {
						if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "bind" {
							beginning[function.Name.Name] = true
						}
					}
				}
				return true
			})
		}
	}
	var have, want []string
	for name := range beginning {
		have = append(have, name)
	}
	for name := range drivers {
		want = append(want, name)
	}
	slices.Sort(have)
	slices.Sort(want)
	for _, name := range want {
		if !beginning[name] {
			t.Errorf("%s is driven here and the root no longer has a function of that name that begins a call", name)
		}
	}
	var undriven []string
	for _, name := range have {
		if drivers[name] == nil {
			undriven = append(undriven, name)
		}
	}
	if len(undriven) > 0 {
		message := "the root's functions that begin a call and are not driven here: " + strings.Join(undriven, ", ")
		if failOnUndrivenFunctions {
			t.Error(message)
		} else {
			t.Log(message)
		}
	}

	tracerProvider, exporter := installed(t)
	model := &scripted{answer: func(int) (schemaflux.Response, error) {
		return schemaflux.Response{Content: `{"q1":true,"confidence":0.9}`}, nil
	}}
	for _, name := range want {
		t.Run(name, func(t *testing.T) {
			spans := underHostSpan(tracerProvider, exporter, model, drivers[name])
			var host, operation string
			var operations, requests int
			for _, span := range spans {
				switch {
				case span.Name == "caller":
					host = span.SpanContext.SpanID().String()
				case span.SpanKind == oteltrace.SpanKindInternal && strings.HasPrefix(span.Name, "schemaflux."):
					operations++
					operation = span.SpanContext.SpanID().String()
					if span.Parent.SpanID().String() != host && host != "" {
						t.Errorf("the operation span %q is not under the host's span", span.Name)
					}
				case span.SpanKind == oteltrace.SpanKindClient:
					requests++
				}
			}
			if operations != 1 {
				t.Fatalf("%d operation spans for one call of %s, want 1", operations, name)
			}
			if requests == 0 {
				t.Fatal("the call made no request: the test is not looking at a call that asks a model")
			}
			for _, span := range spans {
				if span.SpanKind == oteltrace.SpanKindClient && span.Parent.SpanID().String() != operation {
					t.Errorf("a request span of %s is not a child of its operation span", name)
				}
			}
		})
	}
}
