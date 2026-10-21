package schemaflux_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// diffedOwner is a nested struct of the caller's own.
type diffedOwner struct {
	Team   string `json:"team"`
	Region string `json:"region"`
}

// diffedAccount is a value of the caller's own to compare: a plain field, a
// nested struct, a slice, a field JSON leaves out and a field it cannot see.
type diffedAccount struct {
	Name    string      `json:"name"`
	Seats   int         `json:"seats"`
	Owner   diffedOwner `json:"owner"`
	Tags    []string    `json:"tags"`
	Secret  string      `json:"-"`
	private string
}

// diffedAccountV2 has the fields of diffedAccount's first two and one more, and
// is what turns a field up as added.
type diffedAccountV2 struct {
	Name   string `json:"name"`
	Seats  int    `json:"seats"`
	Region string `json:"region"`
}

// diffedAccountWords has Seats as text where diffedAccount has a number.
type diffedAccountWords struct {
	Name  string `json:"name"`
	Seats string `json:"seats"`
}

const diffSummary = "The name changed and the plan grew."

var (
	diffBefore = diffedAccount{Name: "acme", Seats: 5}
	diffAfter  = diffedAccount{Name: "acme corp", Seats: 9}
)

// diffClient is a client on a model that writes the summary given.
func diffClient(summary string, settings ...func(*schemaflux.Client)) (context.Context, *schemafluxtest.Provider) {
	model := answeringWith(summary)
	return operationClient(model, settings...), model
}

// diffRequest makes one call that asks for a summary and returns the request it
// sent.
func diffRequest[T any](t *testing.T, before, after T, opts ...schemaflux.DiffOptions) schemaflux.Request {
	t.Helper()
	ctx, model := diffClient(diffSummary)
	if _, err := schemaflux.Diff(ctx, before, after, opts...); err != nil {
		t.Fatalf("the call failed: %v", err)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	return model.Last()
}

func TestDiffReturnsTheChangesFoundInGoAndTheSummaryTheModelWrote(t *testing.T) {
	t.Parallel()
	ctx, model := diffClient("  " + diffSummary + " \n")
	got, err := schemaflux.Diff(ctx, diffBefore, diffAfter)
	if err != nil {
		t.Fatal(err)
	}
	want := schemaflux.DiffResult{
		Modified: []schemaflux.DiffChange{
			{Field: "Name", OldValue: "acme", NewValue: "acme corp", ChangeType: "modified"},
			{Field: "Seats", OldValue: 5, NewValue: 9, ChangeType: "modified"},
		},
		Summary: diffSummary,
	}
	if got.SummaryError != nil || got.Summary != want.Summary || !reflect.DeepEqual(got.Modified, want.Modified) || len(got.Added) != 0 || len(got.Removed) != 0 {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	req := model.Last()
	wantText(t, req, "OLD DATA:")
	wantText(t, req, `"name": "acme corp"`)
	wantText(t, req, `"field": "Seats"`)
	wantNoText(t, req, "CONTEXT:")
}

func TestDiffWithNoOptionsAsksTheFastTierForTheSummaryAtTheDefaultSettings(t *testing.T) {
	t.Parallel()
	req := diffRequest(t, diffBefore, diffAfter)
	if req.Model != "test-model" || req.WebSearch || req.Format == "json" {
		t.Errorf("model %q web %v format %q", req.Model, req.WebSearch, req.Format)
	}
}

func TestDiffEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	base := diffRequest(t, diffBefore, diffAfter)
	nestedBefore := diffedAccount{Name: "acme", Seats: 5, Owner: diffedOwner{"core", "eu"}}
	nestedAfter := diffedAccount{Name: "acme", Seats: 5, Owner: diffedOwner{"core", "us"}}
	cases := []struct {
		name  string
		opts  schemaflux.DiffOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Background is sent as a quoted value", schemaflux.DiffOptions{Background: `a "billing" system`}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `CONTEXT: "a \"billing\" system"`)
		}},
		{"IgnoreFields leaves the field out of what is sent", schemaflux.DiffOptions{IgnoreFields: []string{"Seats"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `"field": "Name"`)
			wantNoText(t, req, `"field": "Seats"`)
		}},
		{"Steering", schemaflux.DiffOptions{Options: schemaflux.Options{Steering: "write for an auditor"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "write for an auditor")
		}},
		{"Model", schemaflux.DiffOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"Tier", schemaflux.DiffOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens == base.MaxTokens {
				t.Errorf("the output ceiling is %d at both tiers", req.MaxTokens)
			}
		}},
		{"Mode", schemaflux.DiffOptions{Options: schemaflux.Options{Mode: schemaflux.ModeCreative}}, func(t *testing.T, req schemaflux.Request) {
			if req.Temperature == base.Temperature {
				t.Errorf("the temperature is %v in both modes", req.Temperature)
			}
		}},
		{"Effort", schemaflux.DiffOptions{Options: schemaflux.Options{Effort: schemaflux.EffortHigh}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "high" {
				t.Errorf("effort = %q", req.Effort)
			}
		}},
		{"MaxOutputTokens", schemaflux.DiffOptions{Options: schemaflux.Options{MaxOutputTokens: 77}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 77 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.DiffOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.check(t, diffRequest(t, diffBefore, diffAfter, tc.opts))
		})
	}
	t.Run("a changed nested struct is structure_changed", func(t *testing.T) {
		t.Parallel()
		req := diffRequest(t, nestedBefore, nestedAfter)
		wantText(t, req, `"change_type": "structure_changed"`)
		wantNoText(t, req, `"change_type": "modified"`)
	})
	t.Run("NoDeepCompare reports it as one modified value", func(t *testing.T) {
		t.Parallel()
		req := diffRequest(t, nestedBefore, nestedAfter, schemaflux.DiffOptions{NoDeepCompare: true})
		wantText(t, req, `"change_type": "modified"`)
		wantNoText(t, req, `"change_type": "structure_changed"`)
	})
}

func TestDiffNoDeepCompareChangesOnlyTheLabelOfAChangedNestedStruct(t *testing.T) {
	t.Parallel()
	before := diffedAccount{Owner: diffedOwner{"core", "eu"}}
	after := diffedAccount{Owner: diffedOwner{"core", "us"}}
	for _, tc := range []struct {
		name string
		opts []schemaflux.DiffOptions
		want string
	}{
		{"by default", nil, "structure_changed"},
		{"with NoDeepCompare", []schemaflux.DiffOptions{{NoDeepCompare: true}}, "modified"},
		{"with NoDeepCompare false", []schemaflux.DiffOptions{{NoDeepCompare: false}}, "structure_changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, _ := diffClient(diffSummary)
			got, err := schemaflux.Diff(ctx, before, after, tc.opts...)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Modified) != 1 || got.Modified[0].Field != "Owner" || got.Modified[0].ChangeType != tc.want {
				t.Errorf("modified = %+v, want Owner as %s", got.Modified, tc.want)
			}
			if got.Modified[0].OldValue != before.Owner || got.Modified[0].NewValue != after.Owner {
				t.Errorf("the values of a nested struct come back whole: %+v", got.Modified[0])
			}
		})
	}
}

func TestDiffIgnoreFieldsLeavesAFieldOutOfTheComparisonItself(t *testing.T) {
	t.Parallel()
	t.Run("the other change is still found", func(t *testing.T) {
		t.Parallel()
		ctx, _ := diffClient(diffSummary)
		got, err := schemaflux.Diff(ctx, diffBefore, diffAfter, schemaflux.DiffOptions{IgnoreFields: []string{"Seats"}})
		if err != nil || len(got.Modified) != 1 || got.Modified[0].Field != "Name" {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("when the only change is ignored nothing is asked of the model", func(t *testing.T) {
		t.Parallel()
		ctx, model := diffClient(diffSummary)
		got, err := schemaflux.Diff(ctx, diffBefore, diffAfter, schemaflux.DiffOptions{IgnoreFields: []string{"Name", "Seats"}})
		if err != nil || model.Calls() != 0 || len(got.Modified) != 0 || got.Summary == "" {
			t.Errorf("got %+v, %v with %d requests", got, err, model.Calls())
		}
	})
	t.Run("a name that is no field of the struct is ignored without complaint", func(t *testing.T) {
		t.Parallel()
		ctx, _ := diffClient(diffSummary)
		got, err := schemaflux.Diff(ctx, diffBefore, diffAfter, schemaflux.DiffOptions{IgnoreFields: []string{"Nope"}})
		if err != nil || len(got.Modified) != 2 {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("the name is the Go name, not the JSON one", func(t *testing.T) {
		t.Parallel()
		ctx, _ := diffClient(diffSummary)
		got, err := schemaflux.Diff(ctx, diffBefore, diffAfter, schemaflux.DiffOptions{IgnoreFields: []string{"seats"}})
		if err != nil || len(got.Modified) != 2 {
			t.Errorf("got %+v, %v", got, err)
		}
	})
}

func TestDiffAsksNoModelWhenThereIsNothingToSay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		before, after diffedAccount
		sentence      string
	}{
		{"the values are identical", diffBefore, diffBefore, "identical"},
		{"they differ only in a field the type cannot show", diffedAccount{Name: "a", private: "x"}, diffedAccount{Name: "a", private: "y"}, "cannot read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, model := diffClient(diffSummary)
			got, err := schemaflux.Diff(ctx, tc.before, tc.after)
			if err != nil || model.Calls() != 0 || len(got.Modified) != 0 || len(got.Added) != 0 || len(got.Removed) != 0 {
				t.Fatalf("got %+v, %v with %d requests", got, err, model.Calls())
			}
			if !strings.Contains(got.Summary, "No changes detected") || !strings.Contains(got.Summary, tc.sentence) || got.SummaryError != nil {
				t.Errorf("a call that asked no model leaves a sentence of the library's own in Summary and no error: %q, %v", got.Summary, got.SummaryError)
			}
		})
	}
}

func TestDiffReportsAFieldJSONLeavesOutButNeverSendsIt(t *testing.T) {
	t.Parallel()
	const canary = "diff-hidden-canary-3b71"
	t.Run("beside a change that is sent", func(t *testing.T) {
		t.Parallel()
		ctx, model := diffClient(diffSummary)
		got, err := schemaflux.Diff(ctx,
			diffedAccount{Name: "a", Secret: canary + "-old"}, diffedAccount{Name: "b", Secret: canary + "-new"})
		if err != nil || len(got.Modified) != 2 || got.Summary != diffSummary {
			t.Fatalf("got %+v, %v", got, err)
		}
		wantNoText(t, model.Last(), canary)
		wantText(t, model.Last(), `"field": "Name"`)
	})
	t.Run("when it is the only change", func(t *testing.T) {
		t.Parallel()
		ctx, model := diffClient(diffSummary)
		got, err := schemaflux.Diff(ctx, diffedAccount{Secret: canary + "-old"}, diffedAccount{Secret: canary + "-new"})
		if err != nil || model.Calls() != 0 || len(got.Modified) != 1 || got.Modified[0].Field != "Secret" {
			t.Fatalf("got %+v, %v with %d requests", got, err, model.Calls())
		}
		if got.Summary == "" || got.SummaryError != nil {
			t.Errorf("summary %q error %v: the sentence that says nothing was sent is the library's", got.Summary, got.SummaryError)
		}
	})
}

func TestDiffAsksAModelAboutAnAddedOrRemovedFieldOnlyAboveTheFastTier(t *testing.T) {
	t.Parallel()
	var before, after any = diffedAccountV2{Name: "a", Seats: 1, Region: "eu"}, diffedAccount{Name: "a", Seats: 1}
	t.Run("at the default tier nothing is asked and the summary is empty", func(t *testing.T) {
		t.Parallel()
		ctx, model := diffClient(diffSummary)
		got, err := schemaflux.Diff(ctx, before, after)
		if err != nil || model.Calls() != 0 || got.Summary != "" || got.SummaryError != nil {
			t.Fatalf("got %+v, %v with %d requests", got, err, model.Calls())
		}
		if !reflect.DeepEqual(got.Removed, []string{"Region"}) || !reflect.DeepEqual(got.Added, []string{"Owner", "Tags", "Secret"}) || len(got.Modified) != 0 {
			t.Errorf("added %v removed %v modified %v", got.Added, got.Removed, got.Modified)
		}
	})
	t.Run("at the smart tier the model is asked", func(t *testing.T) {
		t.Parallel()
		ctx, model := diffClient(diffSummary)
		got, err := schemaflux.Diff(ctx, before, after, schemaflux.DiffOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}})
		if err != nil || model.Calls() != 1 || got.Summary != diffSummary {
			t.Fatalf("got %+v, %v with %d requests", got, err, model.Calls())
		}
	})
	t.Run("a field that is modified asks at any tier", func(t *testing.T) {
		t.Parallel()
		ctx, model := diffClient(diffSummary)
		got, err := schemaflux.Diff[any](ctx, diffedAccountV2{Name: "a", Seats: 1}, diffedAccountV2{Name: "b", Seats: 1})
		if err != nil || model.Calls() != 1 || got.Summary != diffSummary {
			t.Fatalf("got %+v, %v with %d requests", got, err, model.Calls())
		}
	})
}

func TestDiffTellsAChangeOfKindFromAChangeOfValue(t *testing.T) {
	t.Parallel()
	ctx, _ := diffClient(diffSummary)
	got, err := schemaflux.Diff[any](ctx, diffedAccount{Name: "a", Seats: 5}, diffedAccountWords{Name: "a", Seats: "five"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Modified) != 1 || got.Modified[0] != (schemaflux.DiffChange{Field: "Seats", OldValue: 5, NewValue: "five", ChangeType: "type_changed"}) {
		t.Errorf("modified = %+v", got.Modified)
	}
}

func TestDiffComparesPointersToStructsAndStructsThroughAnInterface(t *testing.T) {
	t.Parallel()
	t.Run("two pointers", func(t *testing.T) {
		t.Parallel()
		ctx, _ := diffClient(diffSummary)
		got, err := schemaflux.Diff(ctx, &diffBefore, &diffAfter)
		if err != nil || len(got.Modified) != 2 {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("two nil pointers have nothing to differ in", func(t *testing.T) {
		t.Parallel()
		ctx, model := diffClient(diffSummary)
		got, err := schemaflux.Diff[*diffedAccount](ctx, nil, nil)
		if err != nil || model.Calls() != 0 || len(got.Modified) != 0 {
			t.Errorf("got %+v, %v with %d requests", got, err, model.Calls())
		}
	})
	t.Run("a pointer and a struct through an interface", func(t *testing.T) {
		t.Parallel()
		ctx, _ := diffClient(diffSummary)
		got, err := schemaflux.Diff[any](ctx, &diffBefore, diffAfter)
		if err != nil || len(got.Modified) != 2 {
			t.Errorf("got %+v, %v", got, err)
		}
	})
}

func TestDiffRefusesValuesThatCannotBeComparedAsAnInvalidRequestBeforeSendingAnything(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		call func(ctx context.Context) (schemaflux.DiffResult, error)
	}{
		{"numbers", func(ctx context.Context) (schemaflux.DiffResult, error) { return schemaflux.Diff(ctx, 1, 2) }},
		{"strings", func(ctx context.Context) (schemaflux.DiffResult, error) { return schemaflux.Diff(ctx, "a", "b") }},
		{"maps", func(ctx context.Context) (schemaflux.DiffResult, error) {
			return schemaflux.Diff(ctx, map[string]int{"a": 1}, map[string]int{"a": 2})
		}},
		{"slices of structs", func(ctx context.Context) (schemaflux.DiffResult, error) {
			return schemaflux.Diff(ctx, []diffedOwner{{}}, []diffedOwner{{Team: "t"}})
		}},
		{"pointers to pointers", func(ctx context.Context) (schemaflux.DiffResult, error) {
			a, b := &diffBefore, &diffAfter
			return schemaflux.Diff(ctx, &a, &b)
		}},
		{"nothing at all", func(ctx context.Context) (schemaflux.DiffResult, error) { return schemaflux.Diff[any](ctx, nil, nil) }},
		{"a struct and a number through an interface", func(ctx context.Context) (schemaflux.DiffResult, error) {
			return schemaflux.Diff[any](ctx, diffBefore, 7)
		}},
		{"a number and a struct through an interface", func(ctx context.Context) (schemaflux.DiffResult, error) {
			return schemaflux.Diff[any](ctx, 7, diffBefore)
		}},
		{"a nil pointer and a struct", func(ctx context.Context) (schemaflux.DiffResult, error) {
			return schemaflux.Diff[*diffedAccount](ctx, nil, &diffAfter)
		}},
		{"a struct and a nil pointer", func(ctx context.Context) (schemaflux.DiffResult, error) {
			return schemaflux.Diff[*diffedAccount](ctx, &diffBefore, nil)
		}},
		{"a nil pointer and a struct through an interface", func(ctx context.Context) (schemaflux.DiffResult, error) {
			var none *diffedAccount
			return schemaflux.Diff[any](ctx, none, diffAfter)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			ctx, model := diffClient(diffSummary, recordingTo(&recorder))
			got, err := tc.call(ctx)
			wantError(t, err, schemaflux.ErrInvalidRequest, "diff")
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
			if !reflect.DeepEqual(got, schemaflux.DiffResult{}) {
				t.Errorf("a refusal left %+v beside the error", got)
			}
			if r := onlyRecord(t, &recorder); r.Op != "diff" || r.Err != err || r.Requests != 0 {
				t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
			}
		})
	}
}

func TestDiffRefusesANilContextAndASettingNoCallCanUse(t *testing.T) {
	t.Parallel()
	_, err := schemaflux.Diff[diffedAccount](nil, diffBefore, diffAfter)
	wantError(t, err, schemaflux.ErrInvalidRequest, "diff")

	for name, opts := range map[string]schemaflux.DiffOptions{
		"a negative output ceiling": {Options: schemaflux.Options{MaxOutputTokens: -1}},
		"a tier that is not one":    {Options: schemaflux.Options{Tier: schemaflux.Tier(99)}},
		"a mode that is not one":    {Options: schemaflux.Options{Mode: schemaflux.Mode(99)}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, model := diffClient(diffSummary)
			got, err := schemaflux.Diff(ctx, diffBefore, diffAfter, opts)
			wantError(t, err, schemaflux.ErrConfiguration, "diff")
			if model.Calls() != 0 || !reflect.DeepEqual(got, schemaflux.DiffResult{}) {
				t.Errorf("%d requests, and a refusal left %+v beside the error", model.Calls(), got)
			}
		})
	}
	t.Run("values that cannot be compared are reported before a setting that cannot be used", func(t *testing.T) {
		t.Parallel()
		ctx, _ := diffClient(diffSummary)
		_, err := schemaflux.Diff(ctx, 1, 2, schemaflux.DiffOptions{Options: schemaflux.Options{MaxOutputTokens: -1}})
		wantError(t, err, schemaflux.ErrInvalidRequest, "diff")
	})
}

func TestDiffMergesSeveralOptionsValuesLeftToRight(t *testing.T) {
	t.Parallel()
	nestedBefore := diffedAccount{Owner: diffedOwner{"core", "eu"}}
	nestedAfter := diffedAccount{Owner: diffedOwner{"core", "us"}, Name: "n"}
	t.Run("a later value sets what it names and leaves the rest", func(t *testing.T) {
		t.Parallel()
		req := diffRequest(t, diffBefore, diffAfter,
			schemaflux.DiffOptions{Background: "first", IgnoreFields: []string{"Seats"}, Options: schemaflux.Options{Steering: "kept"}},
			schemaflux.DiffOptions{Options: schemaflux.Options{Model: "later-model"}},
			schemaflux.DiffOptions{})
		wantText(t, req, `CONTEXT: "first"`)
		wantNoText(t, req, `"field": "Seats"`)
		wantText(t, req, "kept")
		if req.Model != "later-model" {
			t.Errorf("model = %q", req.Model)
		}
	})
	t.Run("the later of two values for one field wins", func(t *testing.T) {
		t.Parallel()
		wantText(t, diffRequest(t, diffBefore, diffAfter, schemaflux.DiffOptions{Background: "first"}, schemaflux.DiffOptions{Background: "second"}), `CONTEXT: "second"`)
	})
	t.Run("a true bool is not cleared by a later false", func(t *testing.T) {
		t.Parallel()
		req := diffRequest(t, nestedBefore, nestedAfter, schemaflux.DiffOptions{NoDeepCompare: true}, schemaflux.DiffOptions{})
		wantNoText(t, req, "structure_changed")
	})
}

func TestDiffReportsAFailedSummaryInTheResultAndReturnsTheComparison(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		model func(p *schemafluxtest.Provider)
		kind  schemaflux.Kind
	}{
		{"a kind the provider gave", func(p *schemafluxtest.Provider) {
			p.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited, Err: errors.New("slow down")})
		}, schemaflux.ErrRateLimited},
		{"a plain error", func(p *schemafluxtest.Provider) { p.Fail(errors.New("the connection broke")) }, schemaflux.ErrUnavailable},
		{"an empty answer", func(p *schemafluxtest.Provider) { p.Reply("   ") }, schemaflux.ErrInvalidAnswer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			tc.model(&model)
			var recorder schemaflux.Recorder
			ctx := operationClient(&model, recordingTo(&recorder))
			got, err := schemaflux.Diff(ctx, diffBefore, diffAfter)
			if err != nil {
				t.Fatalf("a failed summary is not the call's error: %v", err)
			}
			wantError(t, got.SummaryError, tc.kind, "diff")
			if got.Summary != "" || len(got.Modified) != 2 {
				t.Errorf("summary %q with %d changes, want none and the comparison", got.Summary, len(got.Modified))
			}
			if r := onlyRecord(t, &recorder); r.Op != "diff" || r.Err != nil {
				t.Errorf("record = %q err %v", r.Op, r.Err)
			}
		})
	}
}

// A context that has ended refuses the call: the comparison is Go and needs no
// request, but a call under an ended context is not carried out, as for every
// other function.
func TestDiffUnderAContextThatHasEndedReturnsTheContextsErrorAndComparesNothing(t *testing.T) {
	t.Parallel()
	causes := []struct {
		name string
		end  func(ctx context.Context) context.Context
		kind schemaflux.Kind
	}{
		{"cancelled", func(ctx context.Context) context.Context {
			ctx, stop := context.WithCancel(ctx)
			stop()
			return ctx
		}, schemaflux.ErrCanceled},
		{"deadline passed", func(ctx context.Context) context.Context {
			ctx, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
			t.Cleanup(stop)
			return ctx
		}, schemaflux.ErrTimeout},
		{"cancelled with a cause", func(ctx context.Context) context.Context {
			ctx, stop := context.WithCancelCause(ctx)
			stop(errors.New("shutting down"))
			return ctx
		}, schemaflux.ErrCanceled},
	}
	for _, tc := range causes {
		for _, withRecorder := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/recorder=%v", tc.name, withRecorder), func(t *testing.T) {
				t.Parallel()
				model := answeringWith(diffSummary)
				var recorder schemaflux.Recorder
				var settings []func(*schemaflux.Client)
				if withRecorder {
					settings = append(settings, recordingTo(&recorder))
				}
				got, err := schemaflux.Diff(tc.end(operationClient(model, settings...)), diffBefore, diffAfter)
				wantError(t, err, tc.kind, "diff")
				if !reflect.DeepEqual(got, schemaflux.DiffResult{}) {
					t.Errorf("a call refused for its context left %+v beside the error", got)
				}
				if model.Calls() != 0 {
					t.Errorf("%d requests were sent", model.Calls())
				}
				if withRecorder {
					if r := onlyRecord(t, &recorder); r.Op != "diff" || r.Requests != 0 || r.Err == nil {
						t.Errorf("record = op %q, %d requests, error %v", r.Op, r.Requests, r.Err)
					}
				}
			})
		}
	}
}

func TestDiffResultIsWrittenAsJSONWithoutItsError(t *testing.T) {
	t.Parallel()
	got := schemaflux.DiffResult{
		Added: []string{"A"}, Removed: []string{"R"}, Summary: "s",
		Modified:     []schemaflux.DiffChange{{Field: "F", OldValue: 1, NewValue: 2, ChangeType: "modified"}},
		SummaryError: errors.New("not written"),
	}
	out, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"added":["A"],"removed":["R"],"modified":[{"field":"F","old_value":1,"new_value":2,"change_type":"modified"}],"summary":"s"}`
	if string(out) != want {
		t.Errorf("got %s, want %s", out, want)
	}
}

// diffCalled is the call the Record and payload tests make.
func diffCalled(ctx context.Context) error {
	_, err := schemaflux.Diff(ctx, diffBefore, diffAfter)
	return err
}

func TestDiffAddsOneRecordNamedDiffForEveryCall(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		call     func(ctx context.Context) error
		requests int
	}{
		{"a call that asks for a summary", diffCalled, 1},
		{"a call with no change", func(ctx context.Context) error {
			_, err := schemaflux.Diff(ctx, diffBefore, diffBefore)
			return err
		}, 0},
		{"a call whose only change is not sent", func(ctx context.Context) error {
			_, err := schemaflux.Diff(ctx, diffedAccount{Secret: "a"}, diffedAccount{Secret: "b"})
			return err
		}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			if err := tc.call(operationClient(answeringWith(diffSummary), recordingTo(&recorder))); err != nil {
				t.Fatal(err)
			}
			r := onlyRecord(t, &recorder)
			if r.Op != "diff" || r.Err != nil || r.Requests != tc.requests {
				t.Errorf("record = %q err %v requests %d, want %d", r.Op, r.Err, r.Requests, tc.requests)
			}
			if r.Requested != schemaflux.ContractPromptOnly || r.Delivered != schemaflux.ContractPromptOnly {
				t.Errorf("levels = %v and %v, want prompt only", r.Requested, r.Delivered)
			}
		})
	}
	t.Run("a call refused for its options", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		_, err := schemaflux.Diff(operationClient(answeringWith(diffSummary), recordingTo(&recorder)), diffBefore, diffAfter,
			schemaflux.DiffOptions{Options: schemaflux.Options{MaxOutputTokens: -5}})
		wantError(t, err, schemaflux.ErrConfiguration, "diff")
		if r := onlyRecord(t, &recorder); r.Op != "diff" || r.Err != err || r.Requests != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
}

func TestDiffIsRefusedUnderAnyFloorAboveTheLowestLevelAndDeliveredAtIt(t *testing.T) {
	t.Parallel()
	for _, recorded := range []bool{false, true} {
		for _, floor := range []schemaflux.ContractLevel{schemaflux.ContractPromptOnly, schemaflux.ContractJSONWellFormed, schemaflux.ContractSchemaAndInvariantChecked} {
			name := "no recorder"
			if recorded {
				name = "a recorder"
			}
			t.Run(name+", a floor of "+floor.String(), func(t *testing.T) {
				t.Parallel()
				model := namedModel(diffSummary)
				var recorder schemaflux.Recorder
				settings := []func(*schemaflux.Client){underFloor(floor)}
				if recorded {
					settings = append(settings, recordingTo(&recorder))
				}
				got, err := schemaflux.Diff(operationClient(model, settings...), diffBefore, diffAfter)
				refused := floor > schemaflux.ContractPromptOnly
				if refused {
					wantError(t, err, schemaflux.ErrPolicy, "diff")
					if model.Calls() != 0 || !reflect.DeepEqual(got, schemaflux.DiffResult{}) {
						t.Errorf("a refusal sent %d requests and returned %+v", model.Calls(), got)
					}
				} else if err != nil || model.Calls() != 1 || got.Summary != diffSummary {
					t.Fatalf("at its own level: %+v, %v, %d requests", got, err, model.Calls())
				}
				if !recorded {
					return
				}
				r := onlyRecord(t, &recorder)
				if r.Op != "diff" || (r.Err != nil) != refused {
					t.Errorf("record = %q err %v", r.Op, r.Err)
				}
				if refused && r.Requests != 0 {
					t.Errorf("a refusal's Record counts %d requests", r.Requests)
				}
			})
		}
	}
}

func TestDiffRunsAsOneOperationScopeForOneCallWhetherOrNotItAsksAModel(t *testing.T) {
	var scopes []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			scopes = append(scopes, r.Operation)
		}
	}})
	defer restore()
	for name, call := range map[string]func(ctx context.Context) error{
		"a call that asks for a summary": diffCalled,
		"a call with no change": func(ctx context.Context) error {
			_, err := schemaflux.Diff(ctx, diffBefore, diffBefore)
			return err
		},
	} {
		scopes = nil
		if err := call(operationClient(answeringWith(diffSummary))); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(scopes) != 1 {
			t.Errorf("%s: %d operation scopes finished (%v), want 1", name, len(scopes), scopes)
		}
	}
}

func TestDiffLeavesTheCallersTextOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "diff-private-canary-7e1a"
	type holder struct {
		Note   string         `json:"note"`
		Extra  map[string]any `json:"extra"`
		Hidden string         `json:"-"`
	}
	carrying := func(ctx context.Context) error {
		d, err := schemaflux.Diff(ctx,
			holder{Note: "before " + canary, Extra: map[string]any{canary + "-key": canary}, Hidden: "hidden " + canary},
			holder{Note: "after " + canary, Extra: map[string]any{canary + "-key": "other"}, Hidden: "hidden again " + canary},
			schemaflux.DiffOptions{Background: "background " + canary, IgnoreFields: []string{canary}, Options: schemaflux.Options{Steering: "steer " + canary}})
		if err != nil {
			return err
		}
		return d.SummaryError
	}
	t.Run("a provider that fails with a plain error", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(errors.New("vendor said " + canary)) }, false, carrying)
	})
	t.Run("a provider that fails with an Error holding a vendor message", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Err: errors.New("vendor said " + canary)})
		}, false, carrying)
	})
	t.Run("an answer that is empty", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("  ") }, true, carrying)
	})
	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("a summary") }, true, carrying)
	})
	t.Run("a value that cannot be written as JSON", func(t *testing.T) {
		t.Parallel()
		type unwritable struct {
			Note string   `json:"note"`
			Chan chan int `json:"chan"`
		}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("a summary") }, true, func(ctx context.Context) error {
			d, err := schemaflux.Diff(ctx, unwritable{Note: "a " + canary}, unwritable{Note: "b " + canary})
			if err != nil {
				return err
			}
			if d.SummaryError == nil {
				t.Error("a value JSON cannot write was summarised")
			}
			return d.SummaryError
		})
	})
	t.Run("values that cannot be compared", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("a summary") }, true, func(ctx context.Context) error {
			_, err := schemaflux.Diff(ctx, canary, canary+"2")
			return err
		})
	})
	t.Run("a call refused for an option", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("a summary") }, true, func(ctx context.Context) error {
			_, err := schemaflux.Diff(ctx, diffBefore, diffAfter, schemaflux.DiffOptions{Background: canary, Options: schemaflux.Options{MaxOutputTokens: -1, Steering: canary}})
			return err
		})
	})
}
