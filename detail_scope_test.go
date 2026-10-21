package schemaflux_test

import (
	"context"
	"sync"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// scopeLog keeps the finish of every operation and request scope that was
// reported while it is registered, in the order they finished.
type scopeLog struct {
	mu         sync.Mutex
	operations []string
	requests   []string
}

func (l *scopeLog) watch(t *testing.T) {
	t.Helper()
	t.Cleanup(telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		l.mu.Lock()
		defer l.mu.Unlock()
		switch r.Scope {
		case telemetry.ScopeOperation:
			l.operations = append(l.operations, r.Operation)
		case telemetry.ScopeRequest:
			l.requests = append(l.requests, r.Operation)
		}
	}}))
}

// A Detail function names its operation scope, and the requests under it, as it
// names its Record: "isdetail" and not the "is" of the internal twin it runs.
// Otherwise a trace and the Record of one call name two different operations,
// and the one a reader looks up by name is not the other.
func TestTheOperationScopeOfADetailFormIsNamedAsItsRecord(t *testing.T) {
	// Not parallel: an observer is process state.
	labels := []string{"billing", "technical", "shipping"}
	forms := []struct {
		name string
		want string
		call func(ctx context.Context) error
		body string
	}{
		{"IsDetail", "isdetail", func(ctx context.Context) error {
			_, err := schemaflux.IsDetail(ctx, isRefundMsg, isRefundAsk)
			return err
		}, isYesSure},
		{"WhichDetail", "whichdetail", func(ctx context.Context) error {
			_, err := schemaflux.WhichDetail(ctx, isRefundMsg, "Which team should handle this?", labels)
			return err
		}, `{"q1":1,"confidence":0.9}`},
		{"WhichOrNoneDetail", "whichornonedetail", func(ctx context.Context) error {
			_, _, err := schemaflux.WhichOrNoneDetail(ctx, isRefundMsg, "Which team should handle this?", labels)
			return err
		}, `{"q1":3,"confidence":0.7}`},
		{"RateDetail", "ratedetail", func(ctx context.Context) error {
			_, err := schemaflux.RateDetail(ctx, isRefundMsg, "How urgent is this?", 1, 5)
			return err
		}, `{"q1":2,"confidence":0.6}`},
		{"AssessDetail", "assessdetail", func(ctx context.Context) error {
			_, err := schemaflux.AssessDetail[assessTriage](ctx, assessMsg)
			return err
		}, assessAnswerSur},
		{"SummarizeDetail", "summarizedetail", func(ctx context.Context) error {
			_, err := schemaflux.SummarizeDetail(ctx, summaryText)
			return err
		}, summaryDetail},
		{"RewriteDetail", "rewritedetail", func(ctx context.Context) error {
			_, err := schemaflux.RewriteDetail(ctx, rewriteInput)
			return err
		}, rewriteDetail},
		{"TranslateDetail", "translatedetail", func(ctx context.Context) error {
			_, err := schemaflux.TranslateDetail(ctx, translateInput, "Spanish")
			return err
		}, translateDetail},
		{"ExpandDetail", "expanddetail", func(ctx context.Context) error {
			_, err := schemaflux.ExpandDetail(ctx, expandInput)
			return err
		}, expandDetail},
	}
	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			var log scopeLog
			log.watch(t)
			var recorder schemaflux.Recorder
			if err := form.call(operationClient(answeringWith(form.body), recordingTo(&recorder))); err != nil {
				t.Fatalf("%s: %v", form.name, err)
			}
			if op := onlyRecord(t, &recorder).Op; op != form.want {
				t.Fatalf("the Record names the call %q, want %q", op, form.want)
			}
			if len(log.operations) != 1 || log.operations[0] != form.want {
				t.Errorf("the operation scopes are %v, want exactly [%s]", log.operations, form.want)
			}
			if len(log.requests) == 0 {
				t.Error("the observer saw no request")
			}
			for _, name := range log.requests {
				if name != form.want {
					t.Errorf("a request of the call is named %q, want %q", name, form.want)
				}
			}
		})
	}
}

// Calling the plain form of the same operations names the scope for the plain
// form: only a Detail form takes a name of its own.
func TestThePlainFormsOfTheDetailOperationsKeepTheirOwnNames(t *testing.T) {
	forms := []struct {
		name string
		want string
		call func(ctx context.Context) error
		body string
	}{
		{"Is", "is", func(ctx context.Context) error {
			_, err := schemaflux.Is(ctx, isRefundMsg, isRefundAsk)
			return err
		}, isYesSure},
		{"Summarize", "summarize", func(ctx context.Context) error {
			_, err := schemaflux.Summarize(ctx, summaryText)
			return err
		}, summaryShort},
	}
	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			var log scopeLog
			log.watch(t)
			var recorder schemaflux.Recorder
			if err := form.call(operationClient(answeringWith(form.body), recordingTo(&recorder))); err != nil {
				t.Fatalf("%s: %v", form.name, err)
			}
			if op := onlyRecord(t, &recorder).Op; op != form.want {
				t.Fatalf("the Record names the call %q, want %q", op, form.want)
			}
			if len(log.operations) != 1 || log.operations[0] != form.want {
				t.Errorf("the operation scopes are %v, want exactly [%s]", log.operations, form.want)
			}
		})
	}
}
