package fluent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// requestBase guards every setter with `if r.setX == nil { return r.lift(...) }`,
// so a builder that forgets to wire one gets a method that compiles, chains,
// and changes nothing. There is no compile error, no runtime warning, and no
// test that would notice.
//
// NegotiatingAdversarially shipped exactly that: it wired five setters and not
// setMode, so .Strict(), .TransformMode(), and .Creative() were dead. The field
// they would have written did not even exist on AdversarialOptions.
//
// This walks every constructor that builds a requestBase and fails any that
// leaves a setter the base declares unwired -- with one deliberate exception,
// optionalSetters below.
//
// setThreshold is that exception (FL-005): unifying commonRequest, opRequest,
// and directRequest into one requestBase (fluent_base.go) gave every builder
// in the package the same seven setter slots, including Threshold, which
// directRequest never had at all before this task -- .Threshold() did not
// exist as a method on any of fluent_advanced.go's eleven request types.
// Of those eleven, only AuditOptions (internal/ops/audit.go) actually has a
// Threshold field to write into; the other ten (NegotiateOptions,
// AdversarialOptions, ResolveOptions, DeriveOptions, ConformOptions,
// InterpolateOptions, ArbitrateOptions, ProjectOptions, ComposeOptions,
// PivotOptions) do not, and this package cannot add one -- internal/ops is
// out of this task's edit scope. Leaving setThreshold nil on those ten is not
// an oversight the way a forgotten setMode was: there is nowhere for the
// value to go, so .Threshold() on those ten types is documented as a no-op,
// exactly like every other requestBase setter already is when a construction
// site leaves it nil.
func TestEveryBuilderWiresEverySetter(t *testing.T) {
	optionalSetters := map[string]struct{}{"setThreshold": {}}
	fileSet := token.NewFileSet()
	files := parseFluentPackage(t, fileSet)

	// The setter fields the request bases declare.
	setters := map[string]struct{}{}
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			spec, ok := node.(*ast.TypeSpec)
			if !ok || !strings.HasSuffix(spec.Name.Name, "Request") && !strings.Contains(spec.Name.Name, "request") {
				return true
			}
			structType, ok := spec.Type.(*ast.StructType)
			if !ok || structType.Fields == nil {
				return true
			}
			for _, field := range structType.Fields.List {
				for _, name := range field.Names {
					if strings.HasPrefix(name.Name, "set") {
						setters[name.Name] = struct{}{}
					}
				}
			}
			return true
		})
	}
	if len(setters) < 4 {
		t.Fatalf("only %d setter fields found: %v; the walk is not seeing the bases", len(setters), setters)
	}

	// Every composite literal that assigns at least two setters is a builder
	// construction, and must assign all of them.
	type gap struct {
		function string
		missing  []string
	}
	var gaps []gap

	for _, file := range files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}

			ast.Inspect(function.Body, func(node ast.Node) bool {
				composite, ok := node.(*ast.CompositeLit)
				if !ok {
					return true
				}

				assigned := map[string]struct{}{}
				for _, element := range composite.Elts {
					pair, ok := element.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := pair.Key.(*ast.Ident)
					if !ok {
						continue
					}
					if _, isSetter := setters[key.Name]; isSetter {
						assigned[key.Name] = struct{}{}
					}
				}
				if len(assigned) < 2 {
					return true
				}

				var missing []string
				for setter := range setters {
					if _, optional := optionalSetters[setter]; optional {
						continue
					}
					if _, wired := assigned[setter]; !wired {
						missing = append(missing, setter)
					}
				}
				if len(missing) > 0 {
					sort.Strings(missing)
					gaps = append(gaps, gap{function: function.Name.Name, missing: missing})
				}
				return true
			})
		}
	}

	for _, g := range gaps {
		t.Errorf("%s leaves %s unwired: the matching builder methods compile, chain, and change nothing",
			g.function, strings.Join(g.missing, ", "))
	}
}

// The options struct behind every builder has to have somewhere to put a Mode
// and an Intelligence, or the setter cannot be wired even in principle.
func TestBuilderOptionsCanHoldModeAndIntelligence(t *testing.T) {
	// AdversarialOptions had no Mode field at all, which is why setMode was
	// missing rather than merely forgotten. Assert the shape the base assumes.
	request := NegotiatingAdversarially[string](AdversarialContext[string]{})

	strict := request.Strict()
	if strict.opts.Mode != Strict {
		t.Errorf("Strict() produced Mode %v, want Strict", strict.opts.Mode)
	}

	creative := request.Creative()
	if creative.opts.Mode != Creative {
		t.Errorf("Creative() produced Mode %v, want Creative", creative.opts.Mode)
	}

	transform := request.TransformMode()
	if transform.opts.Mode != TransformMode {
		t.Errorf("TransformMode() produced Mode %v, want TransformMode", transform.opts.Mode)
	}
}

func parseFluentPackage(t *testing.T, fileSet *token.FileSet) []*ast.File {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		t.Fatal("no source files parsed")
	}
	return files
}

// A builder method that chains but changes nothing is the failure mode this
// package is prone to, so the common controls are checked on a real builder
// rather than only by parsing the source.
func TestCommonControlsActuallySet(t *testing.T) {
	base := NegotiatingAdversarially[string](AdversarialContext[string]{})

	t.Run("mode", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			apply func(AdversarialNegotiationRequest[string]) AdversarialNegotiationRequest[string]
			want  Mode
		}{
			{"strict", func(r AdversarialNegotiationRequest[string]) AdversarialNegotiationRequest[string] { return r.Strict() }, Strict},
			{"transform", func(r AdversarialNegotiationRequest[string]) AdversarialNegotiationRequest[string] {
				return r.TransformMode()
			}, TransformMode},
			{"creative", func(r AdversarialNegotiationRequest[string]) AdversarialNegotiationRequest[string] {
				return r.Creative()
			}, Creative},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := tc.apply(base).opts.Mode; got != tc.want {
					t.Errorf("Mode = %v, want %v", got, tc.want)
				}
			})
		}
	})

	t.Run("intelligence", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			apply func(AdversarialNegotiationRequest[string]) AdversarialNegotiationRequest[string]
			want  Speed
		}{
			{"smart", func(r AdversarialNegotiationRequest[string]) AdversarialNegotiationRequest[string] { return r.Smart() }, Smart},
			{"fast", func(r AdversarialNegotiationRequest[string]) AdversarialNegotiationRequest[string] { return r.Fast() }, Fast},
			{"quick", func(r AdversarialNegotiationRequest[string]) AdversarialNegotiationRequest[string] { return r.Quick() }, Quick},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := tc.apply(base).opts.Intelligence; got != tc.want {
					t.Errorf("Intelligence = %v, want %v", got, tc.want)
				}
			})
		}
	})

	t.Run("steering", func(t *testing.T) {
		if got := base.Steer("be terse").opts.Steering; got != "be terse" {
			t.Errorf("Steering = %q", got)
		}
	})

	t.Run("request_id", func(t *testing.T) {
		if got := base.RequestID("req-1").opts.RequestID; got != "req-1" {
			t.Errorf("RequestID = %q", got)
		}
	})

	t.Run("correlation_id", func(t *testing.T) {
		if got := base.CorrelationID("corr-1").opts.CorrelationID; got != "corr-1" {
			t.Errorf("CorrelationID = %q", got)
		}
	})

	t.Run("strategy", func(t *testing.T) {
		if got := base.Strategy("aggressive").opts.Strategy; got != "aggressive" {
			t.Errorf("Strategy = %q", got)
		}
	})
}

// Chaining several controls must keep all of them: each method returns a new
// value, so a lift that dropped a field would lose everything set before it.
func TestChainedControlsAllSurvive(t *testing.T) {
	built := NegotiatingAdversarially[string](AdversarialContext[string]{}).
		Strict().
		Quick().
		Steer("be terse").
		RequestID("req-9").
		CorrelationID("corr-9").
		Strategy("accommodating")

	if built.opts.Mode != Strict {
		t.Errorf("Mode = %v, want Strict", built.opts.Mode)
	}
	if built.opts.Intelligence != Quick {
		t.Errorf("Intelligence = %v, want Quick", built.opts.Intelligence)
	}
	if built.opts.Steering != "be terse" {
		t.Errorf("Steering = %q", built.opts.Steering)
	}
	if built.opts.RequestID != "req-9" {
		t.Errorf("RequestID = %q", built.opts.RequestID)
	}
	if built.opts.CorrelationID != "corr-9" {
		t.Errorf("CorrelationID = %q", built.opts.CorrelationID)
	}
	if built.opts.Strategy != "accommodating" {
		t.Errorf("Strategy = %q", built.opts.Strategy)
	}
}

// Strict and Smart are the zero values of their types, so a builder that
// "helpfully" skips zero values cannot express them. F-01 is the finding; this
// is the builder-side check that it stays expressible.
func TestZeroValuedControlsAreExpressible(t *testing.T) {
	built := NegotiatingAdversarially[string](AdversarialContext[string]{}).Creative().Fast()
	if built.opts.Mode != Creative || built.opts.Intelligence != Fast {
		t.Fatalf("setup failed: %+v", built.opts)
	}

	// Now set them back to the zero values and check they took.
	back := built.Strict().Smart()
	if back.opts.Mode != Strict {
		t.Errorf("Mode = %v; Strict is the zero value and must still be settable", back.opts.Mode)
	}
	if back.opts.Intelligence != Smart {
		t.Errorf("Intelligence = %v; Smart is the zero value and must still be settable", back.opts.Intelligence)
	}
}
