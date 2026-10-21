package ops

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The plain form of an operation, called as the old root calls it, is one call
// of an operation to an observer: an operation scope of its own, with the
// requests it makes as its children, ending with the kind of the error the call
// returned. Version 2 has this on its recorded path through the call helper; the
// plain forms had only the requests.

// scopeKey is the context key under which a scope's id travels to its children.
type scopeKey struct{}

// scopeTree is an observer that keeps every scope it was told about with the id
// of the scope it was opened inside.
type scopeTree struct {
	mu     sync.Mutex
	next   int
	opened []scopeOpened
	closed []telemetry.OperationResult
}

type scopeOpened struct {
	id     int
	parent int
	event  telemetry.OperationEvent
}

func (s *scopeTree) OperationStarted(ctx context.Context, event telemetry.OperationEvent) context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	parent, _ := ctx.Value(scopeKey{}).(int)
	s.opened = append(s.opened, scopeOpened{id: s.next, parent: parent, event: event})
	return context.WithValue(ctx, scopeKey{}, s.next)
}

func (s *scopeTree) OperationFinished(_ context.Context, result telemetry.OperationResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = append(s.closed, result)
}

func (s *scopeTree) scopes(scope telemetry.Scope) []scopeOpened {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []scopeOpened
	for _, o := range s.opened {
		if o.event.Scope == scope {
			found = append(found, o)
		}
	}
	return found
}

func (s *scopeTree) finished(scope telemetry.Scope, operation string) []telemetry.OperationResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []telemetry.OperationResult
	for _, r := range s.closed {
		if r.Scope == scope && (operation == "" || r.Operation == operation) {
			found = append(found, r)
		}
	}
	return found
}

// answeringProvider answers every request with content, or fails it with err.
type answeringProvider struct {
	content string
	err     error
}

func (p *answeringProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	if p.err != nil {
		return llm.CompletionResponse{}, p.err
	}
	return llm.CompletionResponse{Content: p.content, Model: req.Model, Provider: "local", FinishReason: "stop",
		Usage: types.TokenUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}}, nil
}
func (*answeringProvider) Name() string                               { return "local" }
func (*answeringProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (*answeringProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

// plainForm is one plain operation of the old root, called on ctx.
type plainForm struct {
	name string
	// answer is what a provider answers with for the call to succeed, or "" for
	// a form whose success needs an answer of a shape these tests do not build.
	answer string
	call   func(ctx context.Context) error
}

func plainForms() []plainForm {
	withContext := func(ctx context.Context) CommonOptions { return CommonOptions{Context: ctx} }
	return []plainForm{
		{"summarize", "A summary.", func(ctx context.Context) error {
			o := NewSummarizeOptions()
			o.CommonOptions = withContext(ctx)
			_, err := Summarize("A long text about quarterly revenue.", o)
			return err
		}},
		{"rewrite", "A rewrite.", func(ctx context.Context) error {
			o := NewRewriteOptions()
			o.CommonOptions = withContext(ctx)
			_, err := Rewrite("hey team", o)
			return err
		}},
		{"translate", "Buenos dias.", func(ctx context.Context) error {
			o := NewTranslateOptions()
			o.TargetLanguage = "Spanish"
			o.CommonOptions = withContext(ctx)
			_, err := Translate("Good morning.", o)
			return err
		}},
		{"expand", "A longer text, with more.", func(ctx context.Context) error {
			o := NewExpandOptions()
			o.CommonOptions = withContext(ctx)
			_, err := Expand("Short.", o)
			return err
		}},
		{"classify", "", func(ctx context.Context) error {
			o := NewClassifyOptions().WithCategories([]string{"billing", "technical"})
			o.CommonOptions = withContext(ctx)
			_, err := Classify[string, string]("My card was charged twice.", o)
			return err
		}},
		{"score", "", func(ctx context.Context) error {
			o := NewScoreOptions().WithCriteria([]string{"clarity"})
			o.CommonOptions = withContext(ctx)
			_, err := Score("A short note.", o)
			return err
		}},
		{"filter", "", func(ctx context.Context) error {
			o := NewFilterOptions()
			o.Criteria = "mentions money"
			o.CommonOptions = withContext(ctx)
			_, err := Filter([]string{"a refund", "a greeting"}, o)
			return err
		}},
		{"sort", "", func(ctx context.Context) error {
			o := NewSortOptions()
			o.Criteria = "by urgency"
			o.CommonOptions = withContext(ctx)
			_, err := Sort([]string{"later", "now"}, o)
			return err
		}},
		{"is", `{"q1":true}`, func(ctx context.Context) error {
			_, err := Is(ctx, "Please refund me.", "Is this about a refund?")
			return err
		}},
		{"which", `{"q1":2}`, func(ctx context.Context) error {
			_, err := Which(ctx, "Please refund me.", "Which team?", []string{"sales", "billing"})
			return err
		}},
		{"rate", `{"q1":4}`, func(ctx context.Context) error {
			_, err := Rate(ctx, "Please refund me.", "How urgent?", 1, 5)
			return err
		}},
		{"merge", "", func(ctx context.Context) error {
			_, err := Merge([]string{"a", "b"}, "keep the longer", types.OpOptions{Context: ctx})
			return err
		}},
	}
}

// One call of a plain form is one operation scope named for the operation, and
// every request the call made is a child of it.
func TestAPlainFormIsOneOperationScopeWithItsRequestsBeneathIt(t *testing.T) {
	for _, form := range plainForms() {
		t.Run(form.name, func(t *testing.T) {
			tree := &scopeTree{}
			t.Cleanup(telemetry.AddObserver(tree))
			failure := types.Newf(types.KindAuthentication, "llm.complete", "the key was refused")
			ctx := WithProvider(context.Background(), &answeringProvider{err: failure})

			err := form.call(ctx)
			if err == nil {
				t.Fatal("the call succeeded against a provider that fails every request")
			}
			operations := tree.scopes(telemetry.ScopeOperation)
			if len(operations) != 1 || operations[0].event.Operation != form.name {
				t.Fatalf("the operation scopes are %v, want exactly one named %q", names(operations), form.name)
			}
			requests := tree.scopes(telemetry.ScopeRequest)
			if len(requests) == 0 {
				t.Fatal("no request was seen")
			}
			for _, request := range requests {
				if request.parent != operations[0].id {
					t.Errorf("a request was opened in scope %d, want the operation's scope %d", request.parent, operations[0].id)
				}
				if request.event.Operation != form.name {
					t.Errorf("a request names the operation %q, want %q as its scope does", request.event.Operation, form.name)
				}
			}
			finished := tree.finished(telemetry.ScopeOperation, form.name)
			if len(finished) != 1 {
				t.Fatalf("the operation scope finished %d times, want 1", len(finished))
			}
			if want := types.KindOf(err).String(); finished[0].ErrorKind != want {
				t.Errorf("the scope ended with %q, the error the call returned is %q", finished[0].ErrorKind, want)
			}
			if finished[0].Requests != len(requests) {
				t.Errorf("the scope counted %d requests, the observer saw %d", finished[0].Requests, len(requests))
			}
		})
	}
}

// A plain form that succeeds ends its scope with no failure.
func TestAPlainFormThatSucceedsEndsItsScopeWithNoFailure(t *testing.T) {
	for _, form := range plainForms() {
		if form.answer == "" {
			continue
		}
		t.Run(form.name, func(t *testing.T) {
			tree := &scopeTree{}
			t.Cleanup(telemetry.AddObserver(tree))
			ctx := WithProvider(context.Background(), &answeringProvider{content: form.answer})

			if err := form.call(ctx); err != nil {
				t.Fatalf("%s: %v", form.name, err)
			}
			finished := tree.finished(telemetry.ScopeOperation, form.name)
			if len(finished) != 1 || finished[0].ErrorKind != "" || finished[0].Requests != 1 {
				t.Errorf("the scope ended %+v, want one finish with no failure and one request", finished)
			}
			if got := len(tree.scopes(telemetry.ScopeOperation)); got != 1 {
				t.Errorf("%d operation scopes, want 1", got)
			}
		})
	}
}

// A plain form that is run inside the scope of the same operation (the Result
// form of it, the call of the version-2 API) is the same unit and opens no
// second scope.
func TestAPlainFormInsideItsOwnScopeOpensNoSecondScope(t *testing.T) {
	tree := &scopeTree{}
	t.Cleanup(telemetry.AddObserver(tree))
	ctx := WithProvider(context.Background(), &answeringProvider{content: `{"q1":true}`})

	scoped, observed := observeOperation(ctx, "is")
	var err error
	func() {
		defer observed.end(&err)
		_, err = Is(scoped, "Please refund me.", "Is this about a refund?")
	}()
	if err != nil {
		t.Fatal(err)
	}
	if got := tree.scopes(telemetry.ScopeOperation); len(got) != 1 {
		t.Errorf("%d operation scopes, want 1", len(got))
	}
}

// With no observer registered a plain form makes the allocations it always did:
// opening and ending the scope costs nothing when nobody is watching.
func TestOpeningAScopeCostsNothingWhenNoObserverIsRegistered(t *testing.T) {
	ctx := context.Background()
	var err error
	allocations := testing.AllocsPerRun(1000, func() {
		scoped, observed := observeOperation(ctx, "summarize")
		func() {
			defer observed.end(&err)
			_ = scoped
		}()
	})
	if allocations != 0 {
		t.Errorf("opening and ending a scope allocated %v times with no observer registered, want 0", allocations)
	}

	options := NewSummarizeOptions()
	options.CommonOptions.Context = ctx
	common, embedded := options.CommonOptions, options.OpOptions
	allocations = testing.AllocsPerRun(1000, func() {
		defer observedPlain(&common, &embedded, "summarize").end(&err)
	})
	if allocations != 0 {
		t.Errorf("observedPlain allocated %v times with no observer registered, want 0", allocations)
	}
}

func names(opened []scopeOpened) []string {
	var found []string
	for _, o := range opened {
		found = append(found, fmt.Sprintf("%s#%d", o.event.Operation, o.id))
	}
	return found
}
