package mw

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// notCarried marks a setting of the wrapped package that this package does not
// carry, with the reason, which the test requires.
const notCarried = "not carried: "

// carried says, for every setting of the wrapped package (the functional
// options of mw, the fields of its option structs, and the fields of the
// breaker's configuration), which field of this package's options it is, or
// why it is not carried. A setting that is in neither list is a failure of the
// test below, which is how a setting added to the wrapped package cannot become
// unreachable without anyone saying so.
var carried = map[string]string{
	// retry.go
	"WithMaxAttempts": "RetryOptions.Retries",
	"WithBaseDelay":   "RetryOptions.BaseDelay",
	"WithMaxDelay":    "RetryOptions.MaxDelay",
	// ratelimit.go
	"WithBurst": "RateLimitOptions.Burst",
	"Reject":    "RateLimitOptions.Reject",
	// bulkhead.go
	"RejectWhenFull": "BulkheadOptions.Reject",
	// circuitbreaker.go
	"KeyByModel": "BreakerOptions.PerModel",
	// ops.CircuitBreakerConfig, which CircuitBreaker takes
	"CircuitBreakerConfig.FailureThreshold": "BreakerOptions.Failures",
	"CircuitBreakerConfig.SuccessThreshold": "BreakerOptions.Successes",
	"CircuitBreakerConfig.HalfOpenMaxCalls": "BreakerOptions.HalfOpenCalls",
	"CircuitBreakerConfig.OpenDuration":     "BreakerOptions.OpenFor",
	"CircuitBreakerConfig.Now":              notCarried + "a clock for tests: the breaker reads time.Now, which a testing/synctest bubble replaces",
	// budget.go
	"WithBudgetCostFunc":        "BudgetOptions.Cost",
	"AllowUnpricedCalls":        "BudgetOptions.AllowUnpriced",
	"WithBudgetRequestEstimate": notCarried + "a budget that learned the cost after the call bounded nothing, so this package's Budget always estimates from the request",
	// cache.go
	"WithCacheTTL":              "CacheOptions.TTL",
	"WithCacheStore":            "CacheOptions.Store",
	"WithCacheStats":            "CacheOptions.OnEvent",
	"CacheEvent.Hit":            "CacheEvent.Hit",
	"CacheEvent.Coalesced":      "CacheEvent.Coalesced",
	"CacheEvent.Key":            "CacheEvent.Key",
	"CacheEvent.Tenant":         "CacheEvent.Tenant",
	"CacheEvent.DataPolicy":     "CacheEvent.DataPolicy",
	"CacheEvent.Bypassed":       "CacheEvent.Bypassed",
	"CachePartition.Tenant":     "CachePartition.Tenant",
	"CachePartition.DataPolicy": "CachePartition.DataPolicy",
	// fallback.go
	"WithPrimaryCapabilities":               "FallbackOptions.Primary",
	"WithPrimaryDataPolicy":                 notCarried + "the alternates are held to the client's DataPolicy, which is one policy; a second label to match would be a second policy",
	"WithFallbackKinds":                     "FallbackOptions.On",
	"AllowSchemaDegradation":                "FallbackOptions.AllowSchemaDegradation",
	"FallbackCapabilities.NativeJSONSchema": "Capabilities.NativeJSONSchema",
	"FallbackDataPolicy.Classification":     notCarried + "the alternates are held to the client's DataPolicy, which is one policy",
	"FallbackRoute.Handler":                 "Route.Provider",
	"FallbackRoute.Capabilities":            "Route.Capabilities",
	"FallbackRoute.DataPolicy":              notCarried + "the alternates are held to the client's DataPolicy, which is one policy",
	"FallbackRoute.Model":                   "Route.Model",
	// hedge.go
	"WithHedgeObserver":    notCarried + "an observer is told per middleware, not per request; Response.Hedged says it for each request",
	"HedgeEvent.Hedged":    notCarried + "replaced by Response.Hedged",
	"HedgeEvent.SecondWon": notCarried + "the answer does not say which copy won: a Record does not need it and a program that wants it measures the two providers",
	// redact.go
	"WithoutBuiltins": "RedactOptions.NoBuiltins",
	"WithPattern":     "RedactOptions.Patterns",
	"WithMarker":      "RedactOptions.Marker",
	"WithFunc":        "RedactOptions.Func",
	// decisions.go
	"DecisionOptions.Floor":      "DecisionOptions.Floor",
	"DecisionOptions.Model":      "DecisionOptions.Model",
	"DecisionOptions.NoFailover": "DecisionOptions.NoFailover",
}

// optionTypes are this package's option types, by name.
var optionTypes = map[string]reflect.Type{
	"RetryOptions":     reflect.TypeFor[RetryOptions](),
	"RateLimitOptions": reflect.TypeFor[RateLimitOptions](),
	"BulkheadOptions":  reflect.TypeFor[BulkheadOptions](),
	"BreakerOptions":   reflect.TypeFor[BreakerOptions](),
	"BudgetOptions":    reflect.TypeFor[BudgetOptions](),
	"CacheOptions":     reflect.TypeFor[CacheOptions](),
	"CacheEvent":       reflect.TypeFor[CacheEvent](),
	"CachePartition":   reflect.TypeFor[CachePartition](),
	"FallbackOptions":  reflect.TypeFor[FallbackOptions](),
	"Capabilities":     reflect.TypeFor[Capabilities](),
	"Route":            reflect.TypeFor[Route](),
	"RedactOptions":    reflect.TypeFor[RedactOptions](),
	"DecisionOptions":  reflect.TypeFor[DecisionOptions](),
}

// parseSource parses the non-test Go files of the package in dir.
func parseSource(t *testing.T, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("%s could not be read: %v", dir, err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s could not be parsed: %v", name, err)
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		t.Fatalf("no Go files in %s", dir)
	}
	return files
}

// wrappedSettings reads the source of the wrapped package and lists its
// settings: each exported function that returns one of its option types, and
// each exported field of the option structs and the other settings types the
// carried table names, plus the fields of the breaker's configuration, which is
// in the internal package.
func wrappedSettings(t *testing.T) []string {
	t.Helper()
	files := parseSource(t, "../internal/runtime/mw")
	structs := map[string]bool{
		"DecisionOptions": true, "CacheEvent": true, "CachePartition": true, "FallbackCapabilities": true,
		"FallbackDataPolicy": true, "FallbackRoute": true, "HedgeEvent": true,
	}
	var settings []string
	for _, file := range files {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv != nil || !d.Name.IsExported() || d.Type.Results == nil || len(d.Type.Results.List) != 1 {
					continue
				}
				if id, ok := d.Type.Results.List[0].Type.(*ast.Ident); ok && strings.HasSuffix(id.Name, "Option") {
					settings = append(settings, d.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || !structs[ts.Name.Name] {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}
					for _, field := range st.Fields.List {
						for _, name := range field.Names {
							if name.IsExported() {
								settings = append(settings, ts.Name.Name+"."+name.Name)
							}
						}
					}
				}
			}
		}
	}
	config := reflect.TypeFor[ops.CircuitBreakerConfig]()
	for i := range config.NumField() {
		if config.Field(i).IsExported() {
			settings = append(settings, "CircuitBreakerConfig."+config.Field(i).Name)
		}
	}
	sort.Strings(settings)
	return settings
}

func TestNoSettingOfTheWrappedPackageIsSilentlyUnreachable(t *testing.T) {
	t.Parallel()

	settings := wrappedSettings(t)
	if len(settings) < 40 {
		t.Fatalf("only %d settings were found in the wrapped package, which has more: %v", len(settings), settings)
	}

	t.Run("every setting is carried to a field of this package or is listed as not carried with a reason", func(t *testing.T) {
		t.Parallel()
		for _, setting := range settings {
			target, listed := carried[setting]
			if !listed {
				t.Errorf("the wrapped package has the setting %s, which is neither carried nor listed as not carried", setting)
				continue
			}
			if strings.HasPrefix(target, notCarried) && len(strings.TrimPrefix(target, notCarried)) < 20 {
				t.Errorf("%s is not carried and the reason, %q, says too little", setting, target)
			}
		}
	})

	t.Run("nothing is listed that the wrapped package no longer has", func(t *testing.T) {
		t.Parallel()
		for setting := range carried {
			if !slices.Contains(settings, setting) {
				t.Errorf("%s is listed but the wrapped package has no such setting", setting)
			}
		}
	})

	t.Run("every field a setting is carried to exists", func(t *testing.T) {
		t.Parallel()
		for setting, target := range carried {
			if strings.HasPrefix(target, notCarried) {
				continue
			}
			typeName, field, _ := strings.Cut(target, ".")
			typ, ok := optionTypes[typeName]
			if !ok {
				t.Errorf("%s is carried to %s, and there is no type %s", setting, target, typeName)
				continue
			}
			if _, ok := typ.FieldByName(field); !ok {
				t.Errorf("%s is carried to %s, and %s has no field %s", setting, target, typeName, field)
			}
		}
	})

	t.Run("every field of this package's options is what some setting of the wrapped package is carried to", func(t *testing.T) {
		t.Parallel()
		targets := map[string]bool{}
		for _, target := range carried {
			targets[target] = true
		}
		for name, typ := range optionTypes {
			for i := range typ.NumField() {
				field := typ.Field(i)
				if field.IsExported() && !targets[name+"."+field.Name] {
					t.Errorf("%s.%s is carried from no setting of the wrapped package: nothing implements it", name, field.Name)
				}
			}
		}
	})
}

// effects holds, for every field of this package's options, a check that
// setting the field changes what the middleware does. A field that changes
// nothing is a defect, and the test below fails for a field with no entry.
var effects = map[string]func(t *testing.T){
	"RetryOptions.Retries": func(t *testing.T) {
		calls := func(o RetryOptions) int {
			var p schemafluxtest.Provider
			p.Fail(unavailable())
			Retry(o)(&p).Complete(t.Context(), ask("q"))
			return p.Calls()
		}
		if zero, one := calls(RetryOptions{}), calls(RetryOptions{Retries: 1}); zero != 3 || one != 2 {
			t.Errorf("attempts: %d by default, %d with one retry; want 3 and 2", zero, one)
		}
		if none := calls(RetryOptions{Retries: -1}); none != 1 {
			t.Errorf("attempts with none: %d, want 1", none)
		}
	},
	"RetryOptions.BaseDelay": func(t *testing.T) {
		waits := func(o RetryOptions) time.Duration {
			var p schemafluxtest.Provider
			p.FailThen(1, unavailable(), "x")
			start := time.Now()
			Retry(o)(&p).Complete(t.Context(), ask("q"))
			return time.Since(start)
		}
		if def, set := waits(RetryOptions{}), waits(RetryOptions{BaseDelay: 7 * time.Second, MaxDelay: 7 * time.Second}); set != 7*time.Second || def == set {
			t.Errorf("the wait was %v by default and %v with a base delay of 7s", def, set)
		}
	},
	"RetryOptions.MaxDelay": func(t *testing.T) {
		total := func(o RetryOptions) time.Duration {
			var p schemafluxtest.Provider
			p.Fail(unavailable())
			start := time.Now()
			Retry(o)(&p).Complete(t.Context(), ask("q"))
			return time.Since(start)
		}
		capped := total(RetryOptions{BaseDelay: time.Second, MaxDelay: time.Second})
		free := total(RetryOptions{BaseDelay: time.Second})
		if capped != 2*time.Second || free <= capped {
			t.Errorf("two waits took %v capped at 1s and %v uncapped: the cap did nothing", capped, free)
		}
	},
	"RateLimitOptions.Burst": func(t *testing.T) {
		immediate := func(o RateLimitOptions) int {
			var p schemafluxtest.Provider
			limited := RateLimit(1, time.Hour, RateLimitOptions{Reject: true, Burst: o.Burst})(&p)
			n := 0
			for range 5 {
				if _, err := limited.Complete(t.Context(), ask("q")); err == nil {
					n++
				}
			}
			return n
		}
		if zero, three := immediate(RateLimitOptions{}), immediate(RateLimitOptions{Burst: 3}); zero != 1 || three != 3 {
			t.Errorf("requests that went at once: %d by default, %d with a burst of 3; want 1 and 3", zero, three)
		}
	},
	"RateLimitOptions.Reject": func(t *testing.T) {
		second := func(reject bool) (time.Duration, error) {
			var p schemafluxtest.Provider
			limited := RateLimit(1, time.Hour, RateLimitOptions{Reject: reject})(&p)
			limited.Complete(t.Context(), ask("q"))
			start := time.Now()
			_, err := limited.Complete(t.Context(), ask("q"))
			return time.Since(start), err
		}
		if waited, err := second(true); err == nil || waited != 0 {
			t.Errorf("with Reject the second request waited %v and returned %v, want an error at once", waited, err)
		}
		// The wait is served in sleeps the bucket bounds, and rounded up to a
		// nanosecond each, so it is the hour and at most a microsecond over.
		if waited, err := second(false); err != nil || waited < time.Hour || waited > time.Hour+time.Microsecond {
			t.Errorf("without Reject the second request waited %v and returned %v, want it to wait the hour", waited, err)
		}
	},
	"BulkheadOptions.Reject": func(t *testing.T) {
		refused := func(reject bool) bool {
			h := newHeld()
			wall := Bulkhead(1, BulkheadOptions{Reject: reject})(h)
			first := started(t.Context(), wall)
			synctest.Wait()
			second := make(chan error, 1)
			go func() { _, err := wall.Complete(t.Context(), ask("q")); second <- err }()
			synctest.Wait()
			select {
			case err := <-second:
				h.let()
				<-first
				return err != nil
			default:
				h.let()
				synctest.Wait()
				h.let()
				<-first
				<-second
				return false
			}
		}
		if !refused(true) || refused(false) {
			t.Error("Reject did not change what a request does at a full bulkhead")
		}
	},
	"BreakerOptions.Failures": func(t *testing.T) {
		asked := func(o BreakerOptions) int {
			s := newSwitched(unavailable())
			breaker := CircuitBreaker(o)(s)
			for range 8 {
				breaker.Complete(t.Context(), ask("q"))
			}
			return s.Calls()
		}
		if def, two := asked(BreakerOptions{}), asked(BreakerOptions{Failures: 2}); def != 5 || two != 2 {
			t.Errorf("requests that reached the provider: %d by default, %d with 2 failures; want 5 and 2", def, two)
		}
	},
	"BreakerOptions.Successes": func(t *testing.T) {
		sentAfterOneProbe := func(successes int) int {
			s := newSwitched(unavailable())
			breaker := CircuitBreaker(BreakerOptions{Failures: 2, Successes: successes, OpenFor: time.Minute})(s)
			fail(t, breaker, 2)
			time.Sleep(time.Minute)
			s.failing.Store(false)
			if _, err := breaker.Complete(t.Context(), ask("q")); err != nil {
				t.Fatalf("the probe: %v", err)
			}
			s.failing.Store(true)
			before := s.Calls()
			for range 4 {
				breaker.Complete(t.Context(), ask("q"))
			}
			return s.Calls() - before
		}
		// A circuit that closed after one probe sends the two failures that open it
		// again; one that needs two is still half open and opens at the first.
		if one, two := sentAfterOneProbe(0), sentAfterOneProbe(2); one != 2 || two != 1 {
			t.Errorf("requests sent after one probe: %d by default, %d with 2 successes needed; want 2 and 1", one, two)
		}
	},
	"BreakerOptions.HalfOpenCalls": func(t *testing.T) {
		probes := func(n int) int {
			var p schemafluxtest.Provider
			release := make(chan struct{})
			p.Respond(func(call int, _ schemaflux.Request) (schemaflux.Response, error) {
				if call == 0 {
					return schemaflux.Response{}, unavailable()
				}
				<-release
				return schemaflux.Response{}, nil
			})
			breaker := CircuitBreaker(BreakerOptions{Failures: 1, OpenFor: time.Minute, HalfOpenCalls: n})(&p)
			breaker.Complete(t.Context(), ask("q"))
			time.Sleep(time.Minute)
			var done []<-chan error
			for range 4 {
				done = append(done, started(t.Context(), breaker))
				synctest.Wait()
			}
			got := p.Calls() - 1
			close(release)
			for _, d := range done {
				<-d
			}
			return got
		}
		if one, three := probes(0), probes(3); one != 1 || three != 3 {
			t.Errorf("probes in flight: %d by default, %d with 3 allowed; want 1 and 3", one, three)
		}
	},
	"BreakerOptions.OpenFor": func(t *testing.T) {
		probeSent := func(openFor time.Duration, wait time.Duration) bool {
			s := newSwitched(unavailable())
			breaker := CircuitBreaker(BreakerOptions{Failures: 1, OpenFor: openFor})(s)
			breaker.Complete(t.Context(), ask("q"))
			time.Sleep(wait)
			before := s.Calls()
			breaker.Complete(t.Context(), ask("q"))
			return s.Calls() > before
		}
		if !probeSent(time.Minute, time.Minute) || probeSent(time.Hour, time.Minute) || probeSent(0, 10*time.Second) || !probeSent(0, 40*time.Second) {
			t.Error("OpenFor did not set how long the circuit stays open: the default is 30s, so 40s lets a probe through and 10s does not, and an hour holds a minute out")
		}
	},
	"BreakerOptions.PerModel": func(t *testing.T) {
		reached := func(perModel bool) int {
			s := newSwitched(unavailable())
			breaker := CircuitBreaker(BreakerOptions{Failures: 1, PerModel: perModel})(s)
			for _, model := range []string{"a", "a", "b"} {
				breaker.Complete(t.Context(), schemaflux.Request{Model: model, User: "q"})
			}
			return s.Calls()
		}
		if shared, split := reached(false), reached(true); shared != 1 || split != 2 {
			t.Errorf("requests that reached the provider: %d with one circuit, %d with one per model; want 1 and 2", shared, split)
		}
	},
	"BudgetOptions.Cost": func(t *testing.T) {
		answered := func(o BudgetOptions) int {
			p, _ := answering("x")
			budget := Budget(2.5, o)(p)
			n := 0
			for range 10 {
				if _, err := budget.Complete(t.Context(), ask("q")); err != nil {
					break
				}
				n++
			}
			return n
		}
		if with, without := answered(BudgetOptions{Cost: priced(1)}), answered(BudgetOptions{}); with != 2 || without != 0 {
			t.Errorf("requests answered: %d with a Cost of 1 a call, %d with the price table, which does not know the model; want 2 and 0", with, without)
		}
	},
	"BudgetOptions.AllowUnpriced": func(t *testing.T) {
		answered := func(o BudgetOptions) int {
			p, _ := answering("x")
			budget := Budget(2.5, o)(p)
			n := 0
			for range 10 {
				if _, err := budget.Complete(t.Context(), ask("q")); err != nil {
					break
				}
				n++
			}
			return n
		}
		if refused, allowed := answered(BudgetOptions{}), answered(BudgetOptions{AllowUnpriced: true}); refused != 0 || allowed != 10 {
			t.Errorf("requests answered for a model the price table does not know: %d by default, %d with AllowUnpriced; want 0 and 10", refused, allowed)
		}
	},
	"CacheOptions.TTL": func(t *testing.T) {
		calls := func(ttl time.Duration) int32 {
			p, n := answering("x")
			cached := Cache(CacheOptions{TTL: ttl})(p)
			cached.Complete(t.Context(), ask("q"))
			time.Sleep(time.Hour)
			cached.Complete(t.Context(), ask("q"))
			return n.Load()
		}
		if forever, short := calls(0), calls(time.Minute); forever != 1 || short != 2 {
			t.Errorf("provider calls over an hour: %d with no TTL, %d with 1m; want 1 and 2", forever, short)
		}
	},
	"CacheOptions.Store": func(t *testing.T) {
		store := &mapStore{}
		p, _ := answering("x")
		Cache(CacheOptions{Store: store})(p).Complete(t.Context(), ask("q"))
		if len(store.entries) != 1 {
			t.Errorf("the store holds %d answers, want 1", len(store.entries))
		}
	},
	"CacheOptions.OnEvent": func(t *testing.T) {
		var seen events
		p, _ := answering("x")
		Cache(CacheOptions{OnEvent: seen.add})(p).Complete(t.Context(), ask("q"))
		if len(seen.all()) != 1 {
			t.Errorf("%d events, want 1", len(seen.all()))
		}
	},
	"CachePartition.Tenant": func(t *testing.T) {
		p, n := answering("x")
		cached := Cache()(p)
		cached.Complete(WithCachePartition(t.Context(), CachePartition{Tenant: "a"}), ask("q"))
		cached.Complete(WithCachePartition(t.Context(), CachePartition{Tenant: "b"}), ask("q"))
		if n.Load() != 2 {
			t.Errorf("provider calls = %d, want 2", n.Load())
		}
	},
	"CachePartition.DataPolicy": func(t *testing.T) {
		p, n := answering("x")
		cached := Cache()(p)
		cached.Complete(WithCachePartition(t.Context(), CachePartition{DataPolicy: "a"}), ask("q"))
		cached.Complete(WithCachePartition(t.Context(), CachePartition{DataPolicy: "b"}), ask("q"))
		if n.Load() != 2 {
			t.Errorf("provider calls = %d, want 2", n.Load())
		}
	},
	"FallbackOptions.On": func(t *testing.T) {
		goes := func(on []schemaflux.Kind) bool {
			alt, altP := route("alt", "x")
			Fallback([]Route{alt}, FallbackOptions{On: on})(failingProvider("primary", unavailable())).Complete(t.Context(), ask("q"))
			return altP.Calls() == 1
		}
		if !goes(nil) || goes([]schemaflux.Kind{schemaflux.ErrTimeout}) {
			t.Error("On did not change which failures go to the alternate")
		}
	},
	"FallbackOptions.AllowSchemaDegradation": func(t *testing.T) {
		goes := func(allow bool) bool {
			alt, altP := route("alt", "x")
			req := schemaflux.Request{User: "q", Format: "json", Schema: map[string]any{"type": "object"}}
			Fallback([]Route{alt}, FallbackOptions{AllowSchemaDegradation: allow, Primary: Capabilities{NativeJSONSchema: true}})(failingProvider("primary", unavailable())).Complete(t.Context(), req)
			return altP.Calls() == 1
		}
		if goes(false) || !goes(true) {
			t.Error("AllowSchemaDegradation did not change whether a route without the capability is used")
		}
	},
	"FallbackOptions.Primary": func(t *testing.T) {
		goes := func(primary Capabilities) bool {
			alt, altP := route("alt", "x")
			req := schemaflux.Request{User: "q", Format: "json", Schema: map[string]any{"type": "object"}}
			Fallback([]Route{alt}, FallbackOptions{Primary: primary})(failingProvider("primary", unavailable())).Complete(t.Context(), req)
			return altP.Calls() == 1
		}
		if !goes(Capabilities{}) || goes(Capabilities{NativeJSONSchema: true}) {
			t.Error("Primary did not change whether an alternate that lacks a capability is eligible")
		}
	},
	"Capabilities.NativeJSONSchema": func(t *testing.T) {
		goes := func(c Capabilities) bool {
			alt, altP := route("alt", "x")
			alt.Capabilities = c
			req := schemaflux.Request{User: "q", Format: "json", Schema: map[string]any{"type": "object"}}
			Fallback([]Route{alt}, FallbackOptions{Primary: Capabilities{NativeJSONSchema: true}})(failingProvider("primary", unavailable())).Complete(t.Context(), req)
			return altP.Calls() == 1
		}
		if goes(Capabilities{}) || !goes(Capabilities{NativeJSONSchema: true}) {
			t.Error("NativeJSONSchema did not change whether the alternate is used")
		}
	},
	"Route.Provider": func(t *testing.T) {
		alt, altP := route("alt", "x")
		Fallback([]Route{alt})(failingProvider("primary", unavailable())).Complete(t.Context(), ask("q"))
		if altP.Calls() != 1 {
			t.Errorf("the alternate was asked %d times, want 1", altP.Calls())
		}
	},
	"Route.Capabilities": func(t *testing.T) {
		goes := func(c Capabilities) bool {
			alt, altP := route("alt", "x")
			alt.Capabilities = c
			req := schemaflux.Request{User: "q", Format: "json", Schema: map[string]any{"type": "object"}}
			Fallback([]Route{alt}, FallbackOptions{Primary: Capabilities{NativeJSONSchema: true}})(failingProvider("primary", unavailable())).Complete(t.Context(), req)
			return altP.Calls() == 1
		}
		if goes(Capabilities{}) == goes(Capabilities{NativeJSONSchema: true}) {
			t.Error("a route's Capabilities changed nothing")
		}
	},
	"Route.Model": func(t *testing.T) {
		alt, altP := route("alt", "x")
		alt.Model = "alternate-model"
		Fallback([]Route{alt})(failingProvider("primary", unavailable())).Complete(t.Context(), schemaflux.Request{Model: "primary-model", User: "q"})
		if altP.Last().Model != "alternate-model" {
			t.Errorf("the alternate was sent model %q", altP.Last().Model)
		}
	},
	"RedactOptions.NoBuiltins": func(t *testing.T) {
		sent := func(o RedactOptions) string {
			p, send := reached(t, o)
			send(ask(testEmail))
			return p.Last().User
		}
		if sent(RedactOptions{}) == sent(RedactOptions{NoBuiltins: true}) {
			t.Error("NoBuiltins changed nothing")
		}
	},
	"RedactOptions.Patterns": func(t *testing.T) {
		p, send := reached(t, RedactOptions{Patterns: phonePattern})
		send(ask(testPhone))
		if p.Last().User == testPhone {
			t.Error("a pattern of the caller's changed nothing")
		}
	},
	"RedactOptions.Marker": func(t *testing.T) {
		p, send := reached(t, RedactOptions{Marker: "<%s>"})
		send(ask(testEmail))
		if p.Last().User != "<email>" {
			t.Errorf("the provider was sent %q", p.Last().User)
		}
	},
	"RedactOptions.Func": func(t *testing.T) {
		p, send := reached(t, RedactOptions{Func: func(s string) string { return strings.ToUpper(s) }})
		send(ask("quiet"))
		if p.Last().User != "QUIET" {
			t.Errorf("the provider was sent %q", p.Last().User)
		}
	},
	"DecisionOptions.Floor": func(t *testing.T) {
		escalated := func(floor float64) bool {
			generator, decider := pair(schemafluxtest.Yes(0.5))
			resp, _ := Decisions(generator, decider, DecisionOptions{Floor: floor}).Complete(t.Context(), deciding(yesNo(`"x"`, "is it?")))
			return resp.Escalation != nil
		}
		if escalated(0) || !escalated(0.9) {
			t.Error("Floor did not change whether a low-confidence answer is passed over")
		}
	},
	"DecisionOptions.Model": func(t *testing.T) {
		generator, decider := pair(schemafluxtest.Yes(1))
		Decisions(generator, decider, DecisionOptions{Model: "the-decision-model"}).Complete(t.Context(), schemaflux.Request{Model: "other", Decision: yesNo(`"x"`, "is it?")})
		if decider.Last().Model != "the-decision-model" {
			t.Errorf("the decider was asked under %q", decider.Last().Model)
		}
	},
	"DecisionOptions.NoFailover": func(t *testing.T) {
		generator, decider := pair()
		decider.Fail(unavailable())
		_, withFailover := Decisions(generator, decider, DecisionOptions{}).Complete(t.Context(), deciding(yesNo(`"x"`, "is it?")))
		_, without := Decisions(generator, decider, DecisionOptions{NoFailover: true}).Complete(t.Context(), deciding(yesNo(`"x"`, "is it?")))
		if withFailover != nil || without == nil {
			t.Errorf("failover: %v, no failover: %v", withFailover, without)
		}
	},
	"CacheEvent.Hit": func(t *testing.T) {
		if e := cacheEvents(t); !slices.ContainsFunc(e, func(e CacheEvent) bool { return e.Hit }) {
			t.Error("no event had Hit")
		}
	},
	"CacheEvent.Coalesced": func(t *testing.T) {
		h := newHeld()
		var seen events
		cached := Cache(CacheOptions{OnEvent: seen.add})(h)
		a := started(t.Context(), cached)
		synctest.Wait()
		b := started(t.Context(), cached)
		synctest.Wait()
		h.let()
		<-a
		<-b
		if !slices.ContainsFunc(seen.all(), func(e CacheEvent) bool { return e.Coalesced }) {
			t.Error("no event had Coalesced")
		}
	},
	"CacheEvent.Key": func(t *testing.T) {
		if e := cacheEvents(t); !slices.ContainsFunc(e, func(e CacheEvent) bool { return e.Key != "" }) {
			t.Error("no event had a Key")
		}
	},
	"CacheEvent.Tenant": func(t *testing.T) {
		if e := cacheEvents(t); !slices.ContainsFunc(e, func(e CacheEvent) bool { return e.Tenant == "tenant" }) {
			t.Error("no event had the Tenant")
		}
	},
	"CacheEvent.DataPolicy": func(t *testing.T) {
		if e := cacheEvents(t); !slices.ContainsFunc(e, func(e CacheEvent) bool { return e.DataPolicy == "policy" }) {
			t.Error("no event had the DataPolicy")
		}
	},
	"CacheEvent.Bypassed": func(t *testing.T) {
		var seen events
		p, _ := answering("x")
		ctx := withPolicy(t.Context(), types.DataPolicy{AllowedProviders: []string{"test"}})
		Cache(CacheOptions{OnEvent: seen.add})(p).Complete(ctx, ask("q"))
		if !slices.ContainsFunc(seen.all(), func(e CacheEvent) bool { return e.Bypassed }) {
			t.Error("no event had Bypassed")
		}
	},
}

// cacheEvents runs a miss and a hit in a partition and returns the events.
func cacheEvents(t *testing.T) []CacheEvent {
	t.Helper()
	var seen events
	p, _ := answering("x")
	cached := Cache(CacheOptions{OnEvent: seen.add})(p)
	ctx := WithCachePartition(t.Context(), CachePartition{Tenant: "tenant", DataPolicy: "policy"})
	cached.Complete(ctx, ask("q"))
	cached.Complete(ctx, ask("q"))
	return seen.all()
}

func TestEveryFieldOfThePackagesOptionsChangesWhatTheMiddlewareDoes(t *testing.T) {
	t.Parallel()

	var fields []string
	for name, typ := range optionTypes {
		for i := range typ.NumField() {
			if typ.Field(i).IsExported() {
				fields = append(fields, name+"."+typ.Field(i).Name)
			}
		}
	}
	sort.Strings(fields)
	if len(fields) < 35 {
		t.Fatalf("only %d option fields were found: %v", len(fields), fields)
	}
	for _, field := range fields {
		check, ok := effects[field]
		if !ok {
			t.Errorf("%s has no check that setting it changes what the middleware does: a field that changes nothing is a defect", field)
			continue
		}
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, check)
		})
	}
	for field := range effects {
		if !slices.Contains(fields, field) {
			t.Errorf("%s has a check and is no field of an options type", field)
		}
	}
}

// the declared names of the package, which are the names of table A4 for mw,
// the two of ruling R25, and Wrap, which a middleware of a program's own is
// written with since Func cannot keep what a provider underneath can do.
var declared = []string{
	"Bulkhead", "BulkheadOptions", "Budget", "BudgetOptions", "BreakerOptions", "Cache", "CacheEvent", "CacheOptions", "CachePartition",
	"CacheStore", "Capabilities", "Chain", "CircuitBreaker", "DecisionOptions", "Decisions", "Fallback", "FallbackOptions", "Func",
	"Hedge", "Middleware", "RateLimit", "RateLimitOptions", "Redact", "RedactOptions", "Retry", "RetryOptions", "Route",
	"WithCachePartition", "Wrap",
}

func TestThePackageDeclaresExactlyTheNamesOfTheSpecification(t *testing.T) {
	t.Parallel()

	files := parseSource(t, ".")
	var names []string
	for _, file := range files {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.IsExported() {
					names = append(names, d.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							names = append(names, s.Name.Name)
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								names = append(names, n.Name)
							}
						}
					}
				}
			}
		}
	}
	sort.Strings(names)
	want := slices.Clone(declared)
	sort.Strings(want)
	if !slices.Equal(names, want) {
		t.Errorf("the package declares %v, want %v", names, want)
	}
	if len(want) != 29 {
		t.Errorf("the list holds %d names, want the 26 of table A4, the 2 of R25 and Wrap", len(want))
	}
}

// merges holds, for every field of the options types a middleware takes any
// number of, a check of how option values merge: a later value that leaves the
// field at its zero does not take away what an earlier one set, a later value that
// sets it is read as the field's kind says (the last of two numbers, the union of
// two maps, a boolean that is on if any value turns it on, two functions that both
// run), and what loosens a guard is refused in any value but the first. A later
// zero or a later map that replaced an earlier setting lost the rules a program
// asked for without a word.
var merges = map[string]func(t *testing.T){
	"RetryOptions.Retries": func(t *testing.T) {
		wantRetry(t, mergeRetryOptions([]RetryOptions{{Retries: 4}, {}}), RetryOptions{Retries: 4}, "a later zero")
		wantRetry(t, mergeRetryOptions([]RetryOptions{{}, {Retries: 4}}), RetryOptions{Retries: 4}, "an earlier zero")
		wantRetry(t, mergeRetryOptions([]RetryOptions{{Retries: 4}, {Retries: -1}}), RetryOptions{Retries: -1}, "a later value")
		wantRetry(t, mergeRetryOptions([]RetryOptions{{Retries: -1}, {Retries: 2}}), RetryOptions{Retries: 2}, "a later value over none")
	},
	"RetryOptions.BaseDelay": func(t *testing.T) {
		wantRetry(t, mergeRetryOptions([]RetryOptions{{BaseDelay: time.Second}, {}}), RetryOptions{BaseDelay: time.Second}, "a later zero")
		wantRetry(t, mergeRetryOptions([]RetryOptions{{BaseDelay: time.Second}, {BaseDelay: time.Minute}}), RetryOptions{BaseDelay: time.Minute}, "a later value")
	},
	"RetryOptions.MaxDelay": func(t *testing.T) {
		wantRetry(t, mergeRetryOptions([]RetryOptions{{MaxDelay: time.Second}, {}}), RetryOptions{MaxDelay: time.Second}, "a later zero")
		wantRetry(t, mergeRetryOptions([]RetryOptions{{MaxDelay: time.Second}, {MaxDelay: time.Minute}}), RetryOptions{MaxDelay: time.Minute}, "a later value")
	},
	"RateLimitOptions.Burst": func(t *testing.T) {
		if got := mergeRateLimitOptions([]RateLimitOptions{{Burst: 5}, {}}); got.Burst != 5 {
			t.Errorf("a later zero Burst: %+v", got)
		}
		if got := mergeRateLimitOptions([]RateLimitOptions{{Burst: 5}, {Burst: 9}}); got.Burst != 9 {
			t.Errorf("a later Burst: %+v", got)
		}
	},
	"RateLimitOptions.Reject": func(t *testing.T) {
		if got := mergeRateLimitOptions([]RateLimitOptions{{Reject: true}, {Reject: false}}); !got.Reject {
			t.Errorf("a later false took Reject off: %+v", got)
		}
		if got := mergeRateLimitOptions([]RateLimitOptions{{}, {Reject: true}}); !got.Reject {
			t.Errorf("a later true: %+v", got)
		}
	},
	"BulkheadOptions.Reject": func(t *testing.T) {
		if got := mergeBulkheadOptions([]BulkheadOptions{{Reject: true}, {}}); !got.Reject {
			t.Errorf("a later false took Reject off: %+v", got)
		}
		if got := mergeBulkheadOptions([]BulkheadOptions{{}, {Reject: true}}); !got.Reject {
			t.Errorf("a later true: %+v", got)
		}
	},
	"BreakerOptions.Failures": func(t *testing.T) {
		if got := mergeBreakerOptions([]BreakerOptions{{Failures: 3}, {}}); got.Failures != 3 {
			t.Errorf("a later zero: %+v", got)
		}
		if got := mergeBreakerOptions([]BreakerOptions{{Failures: 3}, {Failures: 7}}); got.Failures != 7 {
			t.Errorf("a later value: %+v", got)
		}
	},
	"BreakerOptions.Successes": func(t *testing.T) {
		if got := mergeBreakerOptions([]BreakerOptions{{Successes: 3}, {}}); got.Successes != 3 {
			t.Errorf("a later zero: %+v", got)
		}
		if got := mergeBreakerOptions([]BreakerOptions{{Successes: 3}, {Successes: 7}}); got.Successes != 7 {
			t.Errorf("a later value: %+v", got)
		}
	},
	"BreakerOptions.HalfOpenCalls": func(t *testing.T) {
		if got := mergeBreakerOptions([]BreakerOptions{{HalfOpenCalls: 3}, {}}); got.HalfOpenCalls != 3 {
			t.Errorf("a later zero: %+v", got)
		}
		if got := mergeBreakerOptions([]BreakerOptions{{HalfOpenCalls: 3}, {HalfOpenCalls: 7}}); got.HalfOpenCalls != 7 {
			t.Errorf("a later value: %+v", got)
		}
	},
	"BreakerOptions.OpenFor": func(t *testing.T) {
		if got := mergeBreakerOptions([]BreakerOptions{{OpenFor: time.Second}, {}}); got.OpenFor != time.Second {
			t.Errorf("a later zero: %+v", got)
		}
		if got := mergeBreakerOptions([]BreakerOptions{{OpenFor: time.Second}, {OpenFor: time.Hour}}); got.OpenFor != time.Hour {
			t.Errorf("a later value: %+v", got)
		}
	},
	"BreakerOptions.PerModel": func(t *testing.T) {
		if got := mergeBreakerOptions([]BreakerOptions{{PerModel: true}, {PerModel: false}}); !got.PerModel {
			t.Errorf("a later false took PerModel off: %+v", got)
		}
		if got := mergeBreakerOptions([]BreakerOptions{{}, {PerModel: true}}); !got.PerModel {
			t.Errorf("a later true: %+v", got)
		}
	},
	"BudgetOptions.Cost": func(t *testing.T) {
		one, two := priced(1), priced(2)
		got, problem := mergeBudgetOptions([]BudgetOptions{{Cost: one}, {}})
		if problem != "" || got.Cost == nil || got.Cost(schemaflux.Usage{}, "", "").Total != 1 {
			t.Errorf("a later nil Cost: problem %q, %+v", problem, got)
		}
		got, _ = mergeBudgetOptions([]BudgetOptions{{Cost: one}, {Cost: two}})
		if got.Cost(schemaflux.Usage{}, "", "").Total != 2 {
			t.Errorf("a later Cost did not win")
		}
	},
	"BudgetOptions.AllowUnpriced": func(t *testing.T) {
		if got, problem := mergeBudgetOptions([]BudgetOptions{{AllowUnpriced: true}, {}}); problem != "" || !got.AllowUnpriced {
			t.Errorf("a later false took AllowUnpriced off: problem %q, %+v", problem, got)
		}
		if _, problem := mergeBudgetOptions([]BudgetOptions{{}, {AllowUnpriced: true}}); problem == "" {
			t.Error("AllowUnpriced in a later value, which loosens the budget, was not refused")
		}
		if _, problem := mergeBudgetOptions([]BudgetOptions{{AllowUnpriced: true}, {}, {AllowUnpriced: true}}); problem == "" {
			t.Error("AllowUnpriced in a third value was not refused")
		}
	},
	"CacheOptions.TTL": func(t *testing.T) {
		if got := mergeCacheOptions([]CacheOptions{{TTL: time.Hour}, {}}); got.TTL != time.Hour {
			t.Errorf("a later zero: %+v", got)
		}
		if got := mergeCacheOptions([]CacheOptions{{TTL: time.Hour}, {TTL: time.Minute}}); got.TTL != time.Minute {
			t.Errorf("a later value: %+v", got)
		}
	},
	"CacheOptions.Store": func(t *testing.T) {
		first, second := &mapStore{}, &mapStore{}
		if got := mergeCacheOptions([]CacheOptions{{Store: first}, {}}); got.Store != CacheStore(first) {
			t.Errorf("a later nil store took the store away: %+v", got)
		}
		if got := mergeCacheOptions([]CacheOptions{{Store: first}, {Store: second}}); got.Store != CacheStore(second) {
			t.Errorf("a later store did not win: %+v", got)
		}
	},
	"CacheOptions.OnEvent": func(t *testing.T) {
		var order []string
		got := mergeCacheOptions([]CacheOptions{
			{OnEvent: func(CacheEvent) { order = append(order, "first") }},
			{},
			{OnEvent: func(CacheEvent) { order = append(order, "second") }},
		})
		got.OnEvent(CacheEvent{})
		if !slices.Equal(order, []string{"first", "second"}) {
			t.Errorf("the callbacks ran as %v, want both, the earlier first", order)
		}
		if mergeCacheOptions([]CacheOptions{{}, {}}).OnEvent != nil {
			t.Error("no callback was given and one was made")
		}
	},
	"FallbackOptions.On": func(t *testing.T) {
		got, _ := mergeFallbackOptions([]FallbackOptions{{On: []schemaflux.Kind{schemaflux.ErrTimeout}}, {}})
		if !slices.Equal(got.On, []schemaflux.Kind{schemaflux.ErrTimeout}) {
			t.Errorf("a later empty list took the kinds away: %v", got.On)
		}
		got, _ = mergeFallbackOptions([]FallbackOptions{{On: []schemaflux.Kind{schemaflux.ErrTimeout}}, {On: []schemaflux.Kind{schemaflux.ErrBusy, schemaflux.ErrTimeout}}})
		if !slices.Equal(got.On, []schemaflux.Kind{schemaflux.ErrTimeout, schemaflux.ErrBusy}) {
			t.Errorf("the kinds were %v, want the union with none twice", got.On)
		}
	},
	"FallbackOptions.AllowSchemaDegradation": func(t *testing.T) {
		if got, problem := mergeFallbackOptions([]FallbackOptions{{AllowSchemaDegradation: true}, {}}); problem != "" || !got.AllowSchemaDegradation {
			t.Errorf("a later false took it off: problem %q, %+v", problem, got)
		}
		if _, problem := mergeFallbackOptions([]FallbackOptions{{}, {AllowSchemaDegradation: true}}); problem == "" {
			t.Error("AllowSchemaDegradation in a later value, which loosens a guard, was not refused")
		}
	},
	"FallbackOptions.Primary": func(t *testing.T) {
		got, _ := mergeFallbackOptions([]FallbackOptions{{Primary: Capabilities{NativeJSONSchema: true}}, {}})
		if !got.Primary.NativeJSONSchema {
			t.Errorf("a later empty declaration took the capability away: %+v", got.Primary)
		}
		got, _ = mergeFallbackOptions([]FallbackOptions{{}, {Primary: Capabilities{NativeJSONSchema: true}}})
		if !got.Primary.NativeJSONSchema {
			t.Errorf("a later declaration: %+v", got.Primary)
		}
	},
	"RedactOptions.NoBuiltins": func(t *testing.T) {
		if got, problem := mergeRedactOptions([]RedactOptions{{NoBuiltins: true}, {}}); problem != "" || !got.NoBuiltins {
			t.Errorf("a later false turned the built-ins back on: problem %q, %+v", problem, got)
		}
		if got, problem := mergeRedactOptions([]RedactOptions{{}, {NoBuiltins: true}}); problem == "" || got.NoBuiltins {
			t.Errorf("NoBuiltins in a later value, which turns protection off, was not refused: problem %q, %+v", problem, got)
		}
	},
	"RedactOptions.Patterns": func(t *testing.T) {
		ssn, acct, other := regexp.MustCompile("ssn"), regexp.MustCompile("acct"), regexp.MustCompile("other")
		got, _ := mergeRedactOptions([]RedactOptions{{Patterns: map[string]*regexp.Regexp{"ssn": ssn}}, {Patterns: map[string]*regexp.Regexp{"acct": acct}}})
		if got.Patterns["ssn"] != ssn || got.Patterns["acct"] != acct || len(got.Patterns) != 2 {
			t.Errorf("the rules were %v, want both", got.Patterns)
		}
		got, _ = mergeRedactOptions([]RedactOptions{{Patterns: map[string]*regexp.Regexp{"ssn": ssn}}, {}})
		if got.Patterns["ssn"] != ssn {
			t.Errorf("a later value with no rules took the rules away: %v", got.Patterns)
		}
		got, _ = mergeRedactOptions([]RedactOptions{{Patterns: map[string]*regexp.Regexp{"ssn": ssn}}, {Patterns: map[string]*regexp.Regexp{"ssn": other}}})
		if got.Patterns["ssn"] != other || len(got.Patterns) != 1 {
			t.Errorf("a later rule of the same name did not win: %v", got.Patterns)
		}
		first := map[string]*regexp.Regexp{"ssn": ssn}
		mergeRedactOptions([]RedactOptions{{Patterns: first}, {Patterns: map[string]*regexp.Regexp{"acct": acct}}})
		if len(first) != 1 {
			t.Errorf("the caller's map was written to: %v", first)
		}
	},
	"RedactOptions.Marker": func(t *testing.T) {
		if got, _ := mergeRedactOptions([]RedactOptions{{Marker: "<a>"}, {}}); got.Marker != "<a>" {
			t.Errorf("a later empty marker: %+v", got)
		}
		if got, _ := mergeRedactOptions([]RedactOptions{{Marker: "<a>"}, {Marker: "<b>"}}); got.Marker != "<b>" {
			t.Errorf("a later marker: %+v", got)
		}
	},
	"RedactOptions.Func": func(t *testing.T) {
		upper := func(s string) string { return strings.ToUpper(s) }
		exclaim := func(s string) string { return s + "!" }
		got, _ := mergeRedactOptions([]RedactOptions{{Func: upper}, {}})
		if got.Func == nil || got.Func("a") != "A" {
			t.Error("a later nil function took the function away")
		}
		got, _ = mergeRedactOptions([]RedactOptions{{Func: upper}, {Func: exclaim}})
		if got.Func("a") != "A!" {
			t.Errorf("the functions gave %q, want both, the earlier first", got.Func("a"))
		}
		got, _ = mergeRedactOptions([]RedactOptions{{}, {}})
		if got.Func != nil {
			t.Error("no function was given and one was made")
		}
	},
}

// wantRetry fails the test unless got is want.
func wantRetry(t *testing.T, got, want RetryOptions, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: %+v, want %+v", what, got, want)
	}
}

// Every field of every options type a middleware takes any number of is checked
// for how it merges, so that a field added later cannot be the one a later value
// silently takes the setting of an earlier one from.
func TestEveryFieldOfTheOptionsMergesWithoutLosingWhatAnEarlierValueAskedFor(t *testing.T) {
	t.Parallel()

	variadic := []string{"RetryOptions", "RateLimitOptions", "BulkheadOptions", "BreakerOptions", "BudgetOptions", "CacheOptions", "FallbackOptions", "RedactOptions"}
	var fields []string
	for _, name := range variadic {
		typ := optionTypes[name]
		for i := range typ.NumField() {
			if typ.Field(i).IsExported() {
				fields = append(fields, name+"."+typ.Field(i).Name)
			}
		}
	}
	for _, field := range fields {
		check, ok := merges[field]
		if !ok {
			t.Errorf("%s has no check of how option values merge: a later zero or a later map may replace what an earlier value set", field)
			continue
		}
		t.Run(field, check)
	}
	for field := range merges {
		if !slices.Contains(fields, field) {
			t.Errorf("%s has a merge check and is no field of an options type a middleware takes any number of", field)
		}
	}
}
