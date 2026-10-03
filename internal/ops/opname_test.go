package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/telemetry"
)

// A caller's name for a call applies to the internal operation it was given for
// and to no other, so a sub-operation of the call keeps its own name, and a
// context that was never given one names every operation as it always did.
func TestACallerNameIsTheNameOfTheOperationItWasGivenFor(t *testing.T) {
	given := WithCallerName(context.Background(), "isdetail", "is")
	cases := []struct {
		name string
		ctx  context.Context
		in   string
		want string
	}{
		{"the operation it was given for", given, "is", "isdetail"},
		{"another operation", given, "which", "which"},
		{"a longer name that starts alike", given, "island", "island"},
		{"the caller's own name", given, "isdetail", "isdetail"},
		{"an empty name", given, "", ""},
		{"a context with no caller name", context.Background(), "is", "is"},
		{"a nil context", nil, "is", "is"},
		{"a caller that is the internal name", WithCallerName(context.Background(), "is", "is"), "is", "is"},
		{"an empty caller", WithCallerName(context.Background(), "", "is"), "is", "is"},
		{"an empty internal name", WithCallerName(context.Background(), "isdetail", ""), "is", "is"},
		{"the later of two names for one operation", WithCallerName(given, "isother", "is"), "is", "isother"},
		{"two names for two operations", WithCallerName(given, "whichdetail", "which"), "is", "is"},
		{"the second of two names for two operations", WithCallerName(given, "whichdetail", "which"), "which", "whichdetail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nameForCaller(tc.ctx, tc.in); got != tc.want {
				t.Errorf("nameForCaller(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The name reaches both places a name is reported from: the operation scope and
// the context that names the requests under it. A second call of the operation
// under the scope is the same unit as the first, as it is under its own name.
func TestACallerNameIsTheNameOfTheScopeAndOfTheRequestsUnderIt(t *testing.T) {
	collector := installCollector(t)
	ctx := WithCallerName(context.Background(), "isdetail", "is")

	named := WithOperationName(ctx, "is")
	if got := operationNameFromContext(named); got != "isdetail" {
		t.Errorf("the name the requests are given is %q, want isdetail", got)
	}
	if got := operationNameFromContext(WithOperationName(ctx, "which")); got != "which" {
		t.Errorf("a sub-operation's requests are given %q, want which", got)
	}

	scoped, observed := observeOperation(named, "is")
	var err error
	func() {
		defer observed.end(&err)
		again, inner := observeOperation(scoped, "is")
		if inner != nil {
			t.Error("the same operation, inside its own scope, opened a second scope")
		}
		_, nested := observeOperation(again, "which")
		if nested == nil {
			t.Error("a sub-operation opened no scope of its own")
		}
		nested.end(&err)
	}()

	started, finished := collector.snapshot()
	var names []string
	for _, event := range started {
		if event.Scope == telemetry.ScopeOperation {
			names = append(names, event.Operation)
		}
	}
	if len(names) != 2 || names[0] != "isdetail" || names[1] != "which" {
		t.Errorf("the operation scopes that started are %v, want [isdetail which]", names)
	}
	for _, result := range finished {
		if result.Scope == telemetry.ScopeOperation && result.ErrorKind != "" {
			t.Errorf("scope %q ended with the failure %q", result.Operation, result.ErrorKind)
		}
	}
}
