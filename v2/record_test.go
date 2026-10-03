package schemaflux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// metaNotCarried are the fields of types.Meta that a Record does not have, each
// with the reason. A field of types.Meta that is neither read by
// recordFromOutcome nor listed here fails TestEveryFieldOfMetaIsCarriedOrListed.
var metaNotCarried = map[string]string{
	"CacheHitRatio":                "Tokens.Cached over Tokens.Input: a derived number is the caller's to compute",
	"RequestedTier":                "the tier is an option, and Record has RequestedModel",
	"ModelDrifted":                 "RequestedModel != Model",
	"ModelDriftUnknown":            "either of them being empty",
	"ModelConfidence":              "a claim of the model, in no Record (R13)",
	"ModelClaims":                  "model text in an untyped bag",
	"Cost.PricePerPromptToken":     "the price table's to state",
	"Cost.PricePerCompletionToken": "the price table's to state",
	"Cost.BillingPeriod":           "not a call's",
	"Cost.OrganizationID":          "not a call's",
	"Cost.ProjectID":               "not a call's",
}

// leafValues are the values some leaves need to be seen to matter: Estimated is
// read from one word of the quality.
var leafValues = map[string]any{"Cost.Quality": types.PricingEstimated}

// leafPaths lists the paths of the fields of typ that are not themselves
// structs, descending into structs other than time.Time.
func leafPaths(typ reflect.Type, prefix string) []string {
	var paths []string
	for i := range typ.NumField() {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		path := prefix + field.Name
		if field.Type.Kind() == reflect.Struct && field.Type != reflect.TypeOf(time.Time{}) {
			paths = append(paths, leafPaths(field.Type, path+".")...)
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

// withLeaf returns a pointer to a zero value of typ with the leaf at path set.
func withLeaf(t *testing.T, typ reflect.Type, path string) reflect.Value {
	t.Helper()
	root := reflect.New(typ)
	leaf := root.Elem()
	for _, name := range strings.Split(path, ".") {
		leaf = leaf.FieldByName(name)
	}
	if override, ok := leafValues[path]; ok {
		leaf.Set(reflect.ValueOf(override))
		return root
	}
	counter := 0
	fillValue(t, leaf, path, &counter)
	return root
}

func TestEveryFieldOfMetaIsCarriedOrListed(t *testing.T) {
	t.Parallel()
	metaType := reflect.TypeOf(types.Meta{})
	baseline := recordFromOutcome(&ops.Outcome{}, true, true, 4096)
	paths := leafPaths(metaType, "")
	known := map[string]bool{}
	for _, path := range paths {
		known[path] = true
	}
	for path := range metaNotCarried {
		if !known[path] {
			t.Errorf("%s is on the list of fields not carried and types.Meta has no such field: remove it from the list", path)
		}
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			meta := withLeaf(t, metaType, path).Elem().Interface().(types.Meta)
			got := recordFromOutcome(&ops.Outcome{Meta: meta}, true, true, 4096)
			changed := !reflect.DeepEqual(got, baseline)
			if reason, listed := metaNotCarried[path]; listed {
				if changed {
					t.Errorf("types.Meta.%s is listed as not carried (%s) and recordFromOutcome reads it", path, reason)
				}
				return
			}
			if !changed {
				t.Errorf("types.Meta.%s is neither read by recordFromOutcome nor on the list of fields not carried: map it to a field of Record, or list it with the reason", path)
			}
		})
	}
}

func TestEveryFieldOfTheOutcomeIsCarried(t *testing.T) {
	t.Parallel()
	baseline := recordFromOutcome(&ops.Outcome{}, true, true, 4096)
	outcomeType := reflect.TypeOf(ops.Outcome{})
	for _, name := range fieldNames(outcomeType) {
		if name == "Meta" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			var outcome ops.Outcome
			switch name {
			case "Err":
				outcome.Err = errors.New("failed")
			default:
				outcome = onlyField(t, outcomeType, name).Interface().(ops.Outcome)
			}
			if reflect.DeepEqual(recordFromOutcome(&outcome, true, true, 4096), baseline) {
				t.Errorf("ops.Outcome.%s is not carried by recordFromOutcome", name)
			}
		})
	}
}

func TestARecordHoldsTheDecisionFactsOfItsOutcome(t *testing.T) {
	t.Parallel()
	half, zero := 0.5, 0.0
	cases := []struct {
		name    string
		outcome ops.Outcome
		check   func(Record) string
	}{
		{"who answered", ops.Outcome{AnsweredBy: "gpt-6-luna"}, func(r Record) string {
			if r.AnsweredBy != "gpt-6-luna" {
				return fmt.Sprintf("AnsweredBy = %q", r.AnsweredBy)
			}
			return ""
		}},
		{"typed", ops.Outcome{Typed: true}, func(r Record) string {
			if !r.Typed {
				return "Typed is false"
			}
			return ""
		}},
		{"who was asked first", ops.Outcome{AskedFirst: "jev"}, func(r Record) string {
			if r.AskedFirst != "jev" {
				return fmt.Sprintf("AskedFirst = %q", r.AskedFirst)
			}
			return ""
		}},
		{"why it was passed over", ops.Outcome{Escalated: "below the floor"}, func(r Record) string {
			if r.Escalated != "below the floor" {
				return fmt.Sprintf("Escalated = %q", r.Escalated)
			}
			return ""
		}},
		{"a reported probability", ops.Outcome{AskedFirstModelConfidence: &half}, func(r Record) string {
			if !r.AskedFirstReported || r.AskedFirstConfidence != 0.5 {
				return fmt.Sprintf("AskedFirstConfidence = %v reported %v", r.AskedFirstConfidence, r.AskedFirstReported)
			}
			return ""
		}},
		{"a reported probability of zero is reported", ops.Outcome{AskedFirstModelConfidence: &zero}, func(r Record) string {
			if !r.AskedFirstReported || r.AskedFirstConfidence != 0 {
				return fmt.Sprintf("AskedFirstConfidence = %v reported %v", r.AskedFirstConfidence, r.AskedFirstReported)
			}
			return ""
		}},
		{"no probability is not a probability of zero", ops.Outcome{AskedFirst: "jev"}, func(r Record) string {
			if r.AskedFirstReported || r.AskedFirstConfidence != 0 {
				return fmt.Sprintf("AskedFirstConfidence = %v reported %v", r.AskedFirstConfidence, r.AskedFirstReported)
			}
			return ""
		}},
		{"a call that is not a decision has none", ops.Outcome{}, func(r Record) string {
			if r.AnsweredBy != "" || r.Typed || r.AskedFirst != "" || r.Escalated != "" || r.AskedFirstReported || r.AskedFirstConfidence != 0 {
				return fmt.Sprintf("facts on a call that is not a decision: %+v", r)
			}
			return ""
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := recordFromOutcome(&c.outcome, false, false, 4096)
			if msg := c.check(rec); msg != "" {
				t.Error(msg)
			}
		})
	}
	t.Run("the pointer is copied", func(t *testing.T) {
		confidence := 0.25
		outcome := ops.Outcome{AskedFirst: "jev", AskedFirstModelConfidence: &confidence}
		rec := recordFromOutcome(&outcome, false, false, 4096)
		confidence = 0.75
		if rec.AskedFirstConfidence != 0.25 {
			t.Errorf("AskedFirstConfidence = %v after the outcome's value changed, want 0.25", rec.AskedFirstConfidence)
		}
	})
	t.Run("the facts reach the record through NoteDecision", func(t *testing.T) {
		var outcome ops.Outcome
		confidence := 0.3
		outcome.NoteDecision(ops.DecisionRecord{AnsweredBy: "luna", Typed: false, AskedFirst: "jev", Escalated: "below the floor", AskedFirstModelConfidence: &confidence}, false)
		rec := recordFromOutcome(&outcome, false, false, 4096)
		if rec.AnsweredBy != "luna" || rec.Typed || rec.AskedFirst != "jev" || rec.Escalated != "below the floor" || !rec.AskedFirstReported || rec.AskedFirstConfidence != 0.3 {
			t.Errorf("record = %+v", rec)
		}
	})
}

func TestTheTotalOfDecisionRecordsClearsWhatTheyDisagreeOn(t *testing.T) {
	t.Parallel()
	one := ops.Outcome{AnsweredBy: "luna", Typed: true, AskedFirst: "jev", Escalated: "below the floor"}
	same := ops.Outcome{AnsweredBy: "luna", Typed: true, AskedFirst: "jev", Escalated: "below the floor"}
	otherAnswerer := ops.Outcome{AnsweredBy: "jev", Typed: true, AskedFirst: "jev", Escalated: "below the floor"}
	otherFirst := ops.Outcome{AnsweredBy: "luna", Typed: true, AskedFirst: "quill", Escalated: "below the floor"}
	otherWhy := ops.Outcome{AnsweredBy: "luna", Typed: true, AskedFirst: "jev", Escalated: "unavailable"}
	untyped := ops.Outcome{AnsweredBy: "luna", Typed: false, AskedFirst: "jev", Escalated: "below the floor"}
	cases := []struct {
		name string
		from []ops.Outcome
		want Record
	}{
		{"records that agree keep what they agree on", []ops.Outcome{one, same}, Record{AnsweredBy: "luna", Typed: true, AskedFirst: "jev", Escalated: "below the floor"}},
		{"another answerer clears it, the rest stays", []ops.Outcome{one, otherAnswerer}, Record{Typed: true, AskedFirst: "jev", Escalated: "below the floor"}},
		{"another first-asked clears it, the rest stays", []ops.Outcome{one, otherFirst}, Record{AnsweredBy: "luna", Typed: true, Escalated: "below the floor"}},
		{"another reason clears it, the rest stays", []ops.Outcome{one, otherWhy}, Record{AnsweredBy: "luna", Typed: true, AskedFirst: "jev"}},
		{"one that is not typed makes the total not typed", []ops.Outcome{one, untyped}, Record{AnsweredBy: "luna", AskedFirst: "jev", Escalated: "below the floor"}},
		{"a record with no facts clears every string", []ops.Outcome{one, {}}, Record{}},
		{"the probability of the one asked first is not carried", []ops.Outcome{{AskedFirst: "jev", AskedFirstModelConfidence: new(0.4)}}, Record{AskedFirst: "jev"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rec Recorder
			for i := range c.from {
				(*sink)(&rec).Add(t.Context(), &c.from[i])
			}
			got := rec.Total()
			facts := Record{AnsweredBy: got.AnsweredBy, Typed: got.Typed, AskedFirst: got.AskedFirst, Escalated: got.Escalated, AskedFirstConfidence: got.AskedFirstConfidence, AskedFirstReported: got.AskedFirstReported}
			if !reflect.DeepEqual(facts, c.want) {
				t.Errorf("facts of the total = %+v, want %+v", facts, c.want)
			}
		})
	}
}

func TestEveryFieldOfAQuestionAndACheckIsCarried(t *testing.T) {
	t.Parallel()
	t.Run("a question", func(t *testing.T) {
		typ := reflect.TypeOf(ops.AnsweredQuestion{})
		for _, name := range fieldNames(typ) {
			question := onlyField(t, typ, name).Interface().(ops.AnsweredQuestion)
			got := askedFromInternal([]ops.AnsweredQuestion{question})
			if len(got) != 1 || reflect.DeepEqual(got[0], Asked{}) {
				t.Errorf("ops.AnsweredQuestion.%s is not carried by askedFromInternal", name)
			}
		}
		if reflect.TypeOf(Asked{}).NumField() != typ.NumField() {
			t.Errorf("Asked has %d fields and AnsweredQuestion has %d", reflect.TypeOf(Asked{}).NumField(), typ.NumField())
		}
	})
	t.Run("a check", func(t *testing.T) {
		typ := reflect.TypeOf(types.Check{})
		for _, name := range fieldNames(typ) {
			check := onlyField(t, typ, name).Interface().(types.Check)
			got := checksFromInternal([]types.Check{check})
			if len(got) != 1 || got[0] == (Check{}) {
				t.Errorf("types.Check.%s is not carried by checksFromInternal", name)
			}
		}
		if reflect.TypeOf(Check{}).NumField() != typ.NumField() {
			t.Errorf("Check has %d fields and types.Check has %d", reflect.TypeOf(Check{}).NumField(), typ.NumField())
		}
	})
	t.Run("a timing", func(t *testing.T) {
		typ := reflect.TypeOf(types.Timing{})
		for _, name := range fieldNames(typ) {
			timing := onlyField(t, typ, name).Interface().(types.Timing)
			if got := timingFromInternal(timing); got == (Timing{}) {
				t.Errorf("types.Timing.%s is not carried by timingFromInternal", name)
			} else if back := timingToInternal(got); back != timing {
				t.Errorf("types.Timing.%s does not come back", name)
			}
		}
		if reflect.TypeOf(Timing{}).NumField() != typ.NumField() {
			t.Errorf("Timing has %d fields and types.Timing has %d", reflect.TypeOf(Timing{}).NumField(), typ.NumField())
		}
	})
}

// fullOutcome returns an outcome with every field of the Record that comes from
// it set, and the Record it must make.
func fullOutcome() (ops.Outcome, Record) {
	start := time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC)
	failure := &Error{Kind: ErrInvalidAnswer, Op: "extract", Message: "the answer does not fit the type"}
	outcome := ops.Outcome{
		Start: start,
		Err:   failure,
		Asked: []ops.AnsweredQuestion{
			{Question: "is it urgent?", Answer: true},
			{Question: "which?", Options: []string{"a", "b"}, Answer: "b"},
			{Question: "how bad?", Low: 1, High: 5, Answer: 4},
		},
		Body:       "the refused answer",
		AnsweredBy: "gpt-6-luna", Typed: true, AskedFirst: "jev", Escalated: "below the floor", AskedFirstModelConfidence: new(0.42),
		Meta: types.Meta{
			RequestID:         "req_1",
			CorrelationID:     "corr_1",
			Operation:         "extract",
			Provider:          "openai",
			Model:             "gpt-6-luna-2026",
			SchemaID:          "invoice@1:abc",
			Usage:             types.TokenUsage{PromptTokens: 100, CompletionTokens: 40, TotalTokens: 140, CachedTokens: 30, ReasoningTokens: 10, CacheWriteTokens: 5},
			Cost:              types.CostInfo{TotalCost: 0.5, PromptCost: 0.3, CompletionCost: 0.2, CachedCost: 0.05, ReasoningCost: 0.04, Currency: "USD", Priced: true, PricingSource: "gpt-6-luna", Quality: types.PricingEstimated},
			Attempts:          5,
			Repairs:           1,
			Retries:           2,
			CacheHits:         1,
			Hedged:            true,
			ServiceTier:       "flex",
			Strategy:          "scoring",
			Elapsed:           3 * time.Second,
			Timing:            types.Timing{Calls: 4, Reused: true, Queued: time.Millisecond, Lookup: 2 * time.Millisecond, Connect: 3 * time.Millisecond, Handshake: 4 * time.Millisecond, Send: 5 * time.Millisecond, Wait: 6 * time.Millisecond, Read: 7 * time.Millisecond, Network: 8 * time.Millisecond},
			RequestedContract: types.ContractEvidenceChecked,
			DeliveredContract: types.ContractSchemaConstrained,
			Checks:            []types.Check{{Name: "schema", Passed: true}, {Name: "subset", Passed: false, Detail: "2 items are not in the input"}},
			RequestedModel:    "gpt-6-luna",
			Provenance: types.Provenance{
				ResultID: "res_1", ParentResultIDs: []string{"res_0"}, InputDigest: "sha:1", SchemaDigest: "sha:2",
				OperationVersion: "extract@3", PromptVersion: "3", ResolvedModel: "gpt-6-luna-2026",
				NormalizersApplied: []string{"trim"}, ItemRecoveryPath: "first attempt", CacheUsed: true,
				LibraryVersion: "v2", AdapterVersion: "openai",
			},
		},
	}
	want := Record{
		Op: "extract", Start: start, Elapsed: 3 * time.Second, Err: failure,
		Provider: "openai", Model: "gpt-6-luna-2026", RequestedModel: "gpt-6-luna", ServiceTier: "flex",
		RequestID: "req_1", CorrelationID: "corr_1", SchemaID: "invoice@1:abc",
		Requests: 4, Retries: 2, Repairs: 1, CacheHits: 1, Hedged: true,
		Tokens:    Usage{Input: 100, Output: 40, Total: 140, Cached: 30, CacheWrite: 5, Reasoning: 10},
		Cost:      Cost{Total: 0.5, Input: 0.3, Output: 0.2, Cached: 0.05, Reasoning: 0.04, Currency: "USD", Priced: true, Estimated: true, Source: "gpt-6-luna"},
		Timing:    Timing{Calls: 4, Reused: true, Queued: time.Millisecond, Lookup: 2 * time.Millisecond, Connect: 3 * time.Millisecond, Handshake: 4 * time.Millisecond, Send: 5 * time.Millisecond, Wait: 6 * time.Millisecond, Read: 7 * time.Millisecond, Network: 8 * time.Millisecond},
		Requested: ContractEvidenceChecked, Delivered: ContractSchemaConstrained,
		Checks:   []Check{{Name: "schema", Passed: true}, {Name: "subset", Passed: false, Detail: "2 items are not in the input"}},
		Strategy: "scoring",
		Provenance: &Provenance{
			ResultID: "res_1", ParentResultIDs: []string{"res_0"}, InputDigest: "sha:1", SchemaDigest: "sha:2",
			OperationVersion: "extract@3", PromptVersion: "3", ResolvedModel: "gpt-6-luna-2026",
			NormalizersApplied: []string{"trim"}, ItemRecoveryPath: "first attempt", CacheUsed: true,
			LibraryVersion: "v2", AdapterVersion: "openai",
		},
		Asked: []Asked{
			{Question: "is it urgent?", Answer: true},
			{Question: "which?", Options: []string{"a", "b"}, Answer: "b"},
			{Question: "how bad?", Low: 1, High: 5, Answer: 4},
		},
		Body:       "the refused answer",
		AnsweredBy: "gpt-6-luna", Typed: true, AskedFirst: "jev", Escalated: "below the floor",
		AskedFirstConfidence: 0.42, AskedFirstReported: true,
	}
	return outcome, want
}

func TestAFullOutcomeMakesTheRecordFieldByField(t *testing.T) {
	t.Parallel()
	outcome, want := fullOutcome()
	got := recordFromOutcome(&outcome, true, true, 4096)
	if !reflect.DeepEqual(got, want) {
		wantValue, gotValue := reflect.ValueOf(want), reflect.ValueOf(got)
		for i := range wantValue.NumField() {
			if !reflect.DeepEqual(wantValue.Field(i).Interface(), gotValue.Field(i).Interface()) {
				t.Errorf("%s = %#v, want %#v", wantValue.Type().Field(i).Name, gotValue.Field(i).Interface(), wantValue.Field(i).Interface())
			}
		}
	}
	t.Run("every field of the Record is set by a full outcome", func(t *testing.T) {
		v := reflect.ValueOf(got)
		for i := range v.NumField() {
			if v.Field(i).IsZero() {
				t.Errorf("Record.%s is zero for a full outcome", v.Type().Field(i).Name)
			}
		}
	})
}

func TestRequestsAreTheAttemptsLessTheAnswersACacheServed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		attempts, cacheHits, want int
	}{
		{0, 0, 0},
		{1, 0, 1},
		{2, 0, 2},
		{5, 0, 5},
		{1, 1, 0},
		{3, 1, 2},
		{3, 3, 0},
		{2, 5, 0},
		{0, 1, 0},
		{10, 4, 6},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d attempts and %d from a cache", c.attempts, c.cacheHits), func(t *testing.T) {
			rec := recordFromOutcome(&ops.Outcome{Meta: types.Meta{Attempts: c.attempts, CacheHits: c.cacheHits}}, false, false, 0)
			if rec.Requests != c.want || rec.CacheHits != c.cacheHits {
				t.Errorf("Requests = %d, CacheHits = %d, want %d and %d", rec.Requests, rec.CacheHits, c.want, c.cacheHits)
			}
		})
	}
}

func TestEstimatedIsReadFromTheQualityOfThePrice(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		quality types.PricingQuality
		want    bool
	}{
		{"", false}, {types.PricingUnknown, false}, {types.PricingExact, false}, {types.PricingFree, false}, {types.PricingEstimated, true},
	} {
		t.Run("a price of quality "+string(c.quality), func(t *testing.T) {
			if got := costFromInternal(types.CostInfo{Quality: c.quality}).Estimated; got != c.want {
				t.Errorf("Estimated = %v", got)
			}
		})
	}
}

func TestTheErrorOfARecordIsAnError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		kind Kind
	}{
		{"an *Error stays as it is", &Error{Kind: ErrTimeout, Op: "sort"}, ErrTimeout},
		{"an internal error is converted", types.Newf(types.KindBudgetExceeded, "x", "spent"), ErrBudgetExceeded},
		{"a plain error is internal", errors.New("boom"), ErrInternal},
		{"a context error is a cancellation", context.Canceled, ErrCanceled},
		{"a wrapped kind", fmt.Errorf("w: %w", ErrBusy), ErrBusy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := recordFromOutcome(&ops.Outcome{Err: c.err, Meta: types.Meta{Operation: "sort"}}, false, false, 0)
			var e *Error
			if !errors.As(rec.Err, &e) || e.Kind != c.kind || e.Op != "sort" {
				t.Errorf("Err = %#v", rec.Err)
			}
		})
	}
	t.Run("no error is no error", func(t *testing.T) {
		if rec := recordFromOutcome(&ops.Outcome{}, false, false, 0); rec.Err != nil {
			t.Errorf("Err = %v", rec.Err)
		}
	})
}

// holdsCanary reports whether the canary is in any string reachable from v,
// including unexported fields, map keys and the dynamic values of interfaces.
func holdsCanary(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return strings.Contains(v.String(), canary)
	case reflect.Interface, reflect.Pointer:
		return !v.IsNil() && holdsCanary(v.Elem())
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if holdsCanary(v.Index(i)) {
				return true
			}
		}
	case reflect.Map:
		for iter := v.MapRange(); iter.Next(); {
			if holdsCanary(iter.Key()) || holdsCanary(iter.Value()) {
				return true
			}
		}
	case reflect.Struct:
		for i := range v.NumField() {
			// The chain of an error holds the provider's own error by design, and its
			// text is reached only through Detail; the tests of the error cover it.
			if v.Type().Field(i).Name == "Err" {
				continue
			}
			if holdsCanary(v.Field(i)) {
				return true
			}
		}
	}
	return false
}

func TestARecordWithDecisionsAndBodiesOffCarriesNoMarker(t *testing.T) {
	t.Parallel()
	outcome := ops.Outcome{
		Asked: []ops.AnsweredQuestion{{Question: canary + " question", Options: []string{canary + " a", "b"}, Answer: canary + " answer"}, {Question: "q2", Answer: 3}},
		Body:  `{"total": "` + canary + `"}`,
		Err:   &llm.APIError{StatusCode: 400, Message: canary, Body: canary},
		Meta:  types.Meta{Operation: "is", Attempts: 1},
	}
	t.Run("both off", func(t *testing.T) {
		rec := recordFromOutcome(&outcome, false, false, 4096)
		if holdsCanary(reflect.ValueOf(rec)) {
			t.Errorf("the Record holds the marker: %+v", rec)
		}
		if rec.Asked != nil || rec.Body != "" {
			t.Errorf("Asked = %v, Body = %q", rec.Asked, rec.Body)
		}
		encoded, err := json.Marshal(rec)
		if err != nil || strings.Contains(string(encoded), canary) {
			t.Errorf("json: %s, %v", encoded, err)
		}
		if text := fmt.Sprintf("%+v %v %#v", rec, rec, rec.Err); strings.Contains(text, canary) {
			t.Errorf("fmt prints the marker: %s", text)
		}
	})
	t.Run("decisions on, bodies off", func(t *testing.T) {
		rec := recordFromOutcome(&outcome, true, false, 4096)
		if len(rec.Asked) != 2 || !strings.Contains(rec.Asked[0].Question, canary) {
			t.Errorf("Asked = %+v", rec.Asked)
		}
		if rec.Body != "" {
			t.Errorf("Body = %q", rec.Body)
		}
	})
	t.Run("bodies on, decisions off", func(t *testing.T) {
		rec := recordFromOutcome(&outcome, false, true, 4096)
		if rec.Asked != nil || !strings.Contains(rec.Body, canary) {
			t.Errorf("Asked = %v, Body = %q", rec.Asked, rec.Body)
		}
	})
	t.Run("the error of a record holds the provider's text only in its chain", func(t *testing.T) {
		rec := recordFromOutcome(&outcome, false, false, 4096)
		var e *Error
		if !errors.As(rec.Err, &e) || strings.Contains(e.Error(), canary) {
			t.Errorf("got %v", rec.Err)
		}
	})
}

func TestTheRecordSharesNothingWithTheOutcome(t *testing.T) {
	t.Parallel()
	outcome, _ := fullOutcome()
	rec := recordFromOutcome(&outcome, true, true, 4096)
	outcome.Asked[1].Options[0] = "changed"
	outcome.Meta.Checks[0].Name = "changed"
	outcome.Meta.Provenance.ParentResultIDs[0] = "changed"
	outcome.Meta.Provenance.NormalizersApplied[0] = "changed"
	if rec.Asked[1].Options[0] != "a" || rec.Checks[0].Name != "schema" || rec.Provenance.ParentResultIDs[0] != "res_0" || rec.Provenance.NormalizersApplied[0] != "trim" {
		t.Errorf("the record changed with the outcome: %+v", rec)
	}
}

func TestProvenanceIsNilForAnOperationThatBuildsNone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		p    types.Provenance
		nil_ bool
	}{
		{"none", types.Provenance{}, true},
		{"an empty slice of parents", types.Provenance{ParentResultIDs: []string{}}, true},
		{"a result id", types.Provenance{ResultID: "r"}, false},
		{"a parent", types.Provenance{ParentResultIDs: []string{"p"}}, false},
		{"an input digest", types.Provenance{InputDigest: "d"}, false},
		{"a schema digest", types.Provenance{SchemaDigest: "d"}, false},
		{"an operation version", types.Provenance{OperationVersion: "v"}, false},
		{"a prompt version", types.Provenance{PromptVersion: "v"}, false},
		{"a resolved model", types.Provenance{ResolvedModel: "m"}, false},
		{"a normalizer", types.Provenance{NormalizersApplied: []string{"n"}}, false},
		{"a recovery path", types.Provenance{ItemRecoveryPath: "p"}, false},
		{"cache used", types.Provenance{CacheUsed: true}, false},
		{"a library version", types.Provenance{LibraryVersion: "v"}, false},
		{"an adapter version", types.Provenance{AdapterVersion: "a"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := provenanceFromInternal(c.p); (got == nil) != c.nil_ {
				t.Errorf("got %+v", got)
			}
		})
	}
	t.Run("every field of the internal provenance is carried", func(t *testing.T) {
		typ := reflect.TypeOf(types.Provenance{})
		for _, name := range fieldNames(typ) {
			p := onlyField(t, typ, name).Interface().(types.Provenance)
			if got := provenanceFromInternal(p); got == nil {
				t.Errorf("types.Provenance.%s is not carried by provenanceFromInternal", name)
			}
		}
		if reflect.TypeOf(Provenance{}).NumField() != typ.NumField() {
			t.Errorf("Provenance has %d fields and the internal one has %d", reflect.TypeOf(Provenance{}).NumField(), typ.NumField())
		}
	})
}

func TestCutBody(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		max  int
		want string
	}{
		{"an empty body", "", 10, ""},
		{"a short body", "abc", 10, "abc"},
		{"a body of exactly the limit", "abcde", 5, "abcde"},
		{"a body one past the limit", "abcdef", 5, "abcde"},
		{"a limit of one", "abc", 1, "a"},
		{"a limit of zero keeps nothing", "abc", 0, ""},
		{"a negative limit keeps nothing", "abc", -1, ""},
		{"a cut inside a two-byte character backs up", "aé", 2, "a"},
		{"a cut after a two-byte character keeps it", "aéb", 3, "aé"},
		{"a cut inside a three-byte character backs up", "a€", 3, "a"},
		{"a cut inside a four-byte character backs up", "ab😀", 5, "ab"},
		{"a limit smaller than the first character", "😀", 2, ""},
		{"the default limit", strings.Repeat("x", 5000), defaultMaxBody, strings.Repeat("x", defaultMaxBody)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bind.CutBody(c.body, c.max); got != c.want {
				t.Errorf("CutBody(%q, %d) = %q, want %q", c.body, c.max, got, c.want)
			}
		})
	}
}

func TestTheBodyIsCutToTheRecordersLimit(t *testing.T) {
	t.Parallel()
	outcome := ops.Outcome{Body: strings.Repeat("é", 100)}
	for _, c := range []struct{ limit, want int }{{10, 10}, {11, 10}, {1, 0}, {200, 200}, {500, 200}} {
		t.Run(fmt.Sprintf("a limit of %d bytes keeps %d", c.limit, c.want), func(t *testing.T) {
			rec := recordFromOutcome(&outcome, false, true, c.limit)
			if len(rec.Body) != c.want {
				t.Errorf("kept %d bytes", len(rec.Body))
			}
		})
	}
}

func TestContractLevels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		level    ContractLevel
		internal types.ContractLevel
		name     string
	}{
		{ContractPromptOnly, types.ContractPromptOnly, "prompt only"},
		{ContractJSONWellFormed, types.ContractJSONWellFormed, "json well-formed"},
		{ContractSchemaConstrained, types.ContractSchemaConstrained, "schema constrained"},
		{ContractSchemaAndInvariantChecked, types.ContractSchemaAndInvariantChecked, "schema and invariants checked"},
		{ContractEvidenceChecked, types.ContractEvidenceChecked, "evidence checked"},
		{ContractFullyGoverned, types.ContractFullyGoverned, "fully governed"},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if int(c.level) != i {
				t.Errorf("the level is numbered %d, want %d", c.level, i)
			}
			if got := c.level.String(); got != c.name || got != c.internal.String() {
				t.Errorf("String() = %q, internal %q, want %q", got, c.internal.String(), c.name)
			}
			if got := contractFromInternal(c.internal); got != c.level {
				t.Errorf("contractFromInternal = %v", got)
			}
		})
	}
	t.Run("a level out of range is unknown and keeps its number", func(t *testing.T) {
		for _, n := range []uint8{6, 7, 100, 255} {
			if got := ContractLevel(n).String(); got != "unknown" {
				t.Errorf("String() = %q", got)
			}
			if got := contractFromInternal(types.ContractLevel(n)); uint8(got) != n {
				t.Errorf("contractFromInternal(%d) = %d", n, got)
			}
		}
	})
	t.Run("the weakest is the lowest", func(t *testing.T) {
		if !(ContractPromptOnly < ContractJSONWellFormed && ContractJSONWellFormed < ContractFullyGoverned) {
			t.Error("the levels are not ordered")
		}
	})
}

func TestTimingMethods(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		timing   Timing
		measured bool
		setup    time.Duration
	}{
		{"nothing timed", Timing{}, false, 0},
		{"a call with no network time", Timing{Calls: 1}, false, 0},
		{"network time", Timing{Network: time.Second}, true, 0},
		{"time in the queue only", Timing{Queued: time.Millisecond}, true, 0},
		{"a lookup is not a measurement by itself", Timing{Lookup: time.Millisecond}, false, time.Millisecond},
		{"setup is lookup, connect and handshake", Timing{Lookup: time.Millisecond, Connect: 2 * time.Millisecond, Handshake: 4 * time.Millisecond, Network: time.Second}, true, 7 * time.Millisecond},
		{"send, wait and read are not setup", Timing{Send: time.Second, Wait: time.Second, Read: time.Second}, false, 0},
		{"a reused connection has no setup", Timing{Reused: true, Network: time.Second}, true, 0},
		{"a summed timing", Timing{Calls: 3, Lookup: 3 * time.Millisecond, Connect: 3 * time.Millisecond, Handshake: 3 * time.Millisecond, Network: 3 * time.Second}, true, 9 * time.Millisecond},
		{"the same as the internal timing", timingFromInternal(types.Timing{Network: 1, Lookup: 2}), true, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.timing.Measured(); got != c.measured {
				t.Errorf("Measured() = %v", got)
			}
			if got := c.timing.Setup(); got != c.setup {
				t.Errorf("Setup() = %v", got)
			}
			internal := timingToInternal(c.timing)
			if internal.Measured() != c.timing.Measured() || internal.Setup() != c.timing.Setup() {
				t.Errorf("the methods disagree with the internal ones")
			}
		})
	}
}

// at returns a time n seconds after a fixed instant.
func at(n int) time.Time {
	return time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Second)
}

func TestTotal(t *testing.T) {
	t.Parallel()
	failure := &Error{Kind: ErrTimeout, Op: "sort"}
	other := &Error{Kind: ErrBusy, Op: "sort"}
	cases := []struct {
		name    string
		records []Record
		want    Record
	}{
		{"none is the zero record", nil, Record{}},
		{"one record is that record, less what is not carried", []Record{{
			Op: "is", Start: at(0), Elapsed: time.Second, Provider: "p", Model: "m", RequestedModel: "rm", ServiceTier: "flex", RequestID: "r", CorrelationID: "c", SchemaID: "s",
			Requests: 2, Retries: 1, Repairs: 1, CacheHits: 1, Hedged: true, Strategy: "scoring",
			Tokens: Usage{Input: 1, Output: 2, Total: 3, Cached: 4, CacheWrite: 5, Reasoning: 6},
			Cost:   Cost{Total: 1, Input: 2, Output: 3, Cached: 4, Reasoning: 5, Currency: "USD", Priced: true, Estimated: true, Source: "t"},
			Timing: Timing{Calls: 1, Reused: true, Network: time.Second}, Requested: ContractEvidenceChecked, Delivered: ContractSchemaConstrained,
			Err: failure, Checks: []Check{{Name: "c"}}, Asked: []Asked{{Question: "q"}}, Body: "b", Provenance: &Provenance{ResultID: "x"},
			AnsweredBy: "p", Typed: true, AskedFirst: "j", Escalated: "unavailable", AskedFirstConfidence: 0.5, AskedFirstReported: true,
		}}, Record{
			Op: "is", Start: at(0), Elapsed: time.Second, Provider: "p", Model: "m", RequestedModel: "rm", ServiceTier: "flex", RequestID: "r", CorrelationID: "c", SchemaID: "s",
			Requests: 2, Retries: 1, Repairs: 1, CacheHits: 1, Hedged: true, Strategy: "scoring",
			Tokens: Usage{Input: 1, Output: 2, Total: 3, Cached: 4, CacheWrite: 5, Reasoning: 6},
			Cost:   Cost{Total: 1, Input: 2, Output: 3, Cached: 4, Reasoning: 5, Currency: "USD", Priced: true, Estimated: true, Source: "t"},
			Timing: Timing{Calls: 1, Reused: true, Network: time.Second}, Requested: ContractEvidenceChecked, Delivered: ContractSchemaConstrained,
			Err: failure, AnsweredBy: "p", Typed: true, AskedFirst: "j", Escalated: "unavailable",
		}},
		{"counts are sums", []Record{
			{Requests: 1, Retries: 2, Repairs: 3, CacheHits: 4}, {Requests: 10, Retries: 20, Repairs: 30, CacheHits: 40}, {Requests: 100, Retries: 200, Repairs: 300, CacheHits: 400},
		}, Record{Requests: 111, Retries: 222, Repairs: 333, CacheHits: 444, Cost: Cost{}, Timing: Timing{}}},
		{"tokens are sums field by field", []Record{
			{Tokens: Usage{Input: 1, Output: 2, Total: 3, Cached: 4, CacheWrite: 5, Reasoning: 6}},
			{Tokens: Usage{Input: 10, Output: 20, Total: 30, Cached: 40, CacheWrite: 50, Reasoning: 60}},
		}, Record{Tokens: Usage{Input: 11, Output: 22, Total: 33, Cached: 44, CacheWrite: 55, Reasoning: 66}}},
		{"costs are sums and priced is true only if every record is priced", []Record{
			{Requests: 1, Cost: Cost{Total: 1, Input: 2, Output: 3, Cached: 4, Reasoning: 5, Priced: true}},
			{Requests: 1, Cost: Cost{Total: 10, Input: 20, Output: 30, Cached: 40, Reasoning: 50, Priced: true}},
		}, Record{Requests: 2, Cost: Cost{Total: 11, Input: 22, Output: 33, Cached: 44, Reasoning: 55, Priced: true}}},
		{"one unpriced record makes the total unpriced", []Record{{Requests: 1, Cost: Cost{Total: 1, Priced: true}}, {Requests: 1, Cost: Cost{Total: 2}}}, Record{Requests: 2, Cost: Cost{Total: 3}}},
		{"a call refused before anything was sent does not make the total unpriced", []Record{
			{Requests: 1, Cost: Cost{Total: 2, Priced: true, Currency: "USD"}},
			{Err: &Error{Kind: ErrConfiguration, Op: "extract"}},
		}, Record{Requests: 1, Err: &Error{Kind: ErrConfiguration, Op: "extract"}, Cost: Cost{Total: 2, Priced: true}}},
		{"an answer a cache served does not make the total unpriced", []Record{
			{Requests: 1, Cost: Cost{Total: 2, Priced: true}},
			{CacheHits: 1},
		}, Record{Requests: 1, CacheHits: 1, Cost: Cost{Total: 2, Priced: true}}},
		{"only calls that sent nothing is not priced", []Record{{}, {}}, Record{}},
		{"estimated is true if any record is", []Record{{Requests: 1, Cost: Cost{Priced: true}}, {Requests: 1, Cost: Cost{Priced: true, Estimated: true}}}, Record{Requests: 2, Cost: Cost{Priced: true, Estimated: true}}},
		{"currency and source are kept when all agree", []Record{{Cost: Cost{Currency: "USD", Source: "t"}}, {Cost: Cost{Currency: "USD", Source: "t"}}}, Record{Cost: Cost{Currency: "USD", Source: "t"}}},
		{"currency and source are dropped when they differ", []Record{{Cost: Cost{Currency: "USD", Source: "t"}}, {Cost: Cost{Currency: "EUR", Source: "u"}}}, Record{}},
		{"timing durations and calls are sums, reused only if all reused", []Record{
			{Timing: Timing{Calls: 1, Reused: true, Queued: 1, Lookup: 2, Connect: 3, Handshake: 4, Send: 5, Wait: 6, Read: 7, Network: 8}},
			{Timing: Timing{Calls: 2, Reused: true, Queued: 10, Lookup: 20, Connect: 30, Handshake: 40, Send: 50, Wait: 60, Read: 70, Network: 80}},
		}, Record{Timing: Timing{Calls: 3, Reused: true, Queued: 11, Lookup: 22, Connect: 33, Handshake: 44, Send: 55, Wait: 66, Read: 77, Network: 88}, Cost: Cost{}}},
		{"one record that did not reuse a connection makes reused false", []Record{{Timing: Timing{Reused: true}}, {Timing: Timing{}}}, Record{}},
		{"hedged is true if any record hedged", []Record{{}, {Hedged: true}, {}}, Record{Hedged: true}},
		{"the first error in the order of the records", []Record{{}, {Err: failure}, {Err: other}}, Record{Err: failure}},
		{"no error when none failed", []Record{{}, {}}, Record{}},
		{"requested and delivered are the weakest", []Record{
			{Requested: ContractEvidenceChecked, Delivered: ContractSchemaConstrained},
			{Requested: ContractSchemaConstrained, Delivered: ContractJSONWellFormed},
			{Requested: ContractFullyGoverned, Delivered: ContractFullyGoverned},
		}, Record{Requested: ContractSchemaConstrained, Delivered: ContractJSONWellFormed}},
		{"strings are kept when every record agrees", []Record{
			{Op: "is", Provider: "p", Model: "m", RequestedModel: "rm", ServiceTier: "t", RequestID: "r", CorrelationID: "c", SchemaID: "s", Strategy: "x", AnsweredBy: "a", AskedFirst: "f", Escalated: "e"},
			{Op: "is", Provider: "p", Model: "m", RequestedModel: "rm", ServiceTier: "t", RequestID: "r", CorrelationID: "c", SchemaID: "s", Strategy: "x", AnsweredBy: "a", AskedFirst: "f", Escalated: "e"},
		}, Record{Op: "is", Provider: "p", Model: "m", RequestedModel: "rm", ServiceTier: "t", RequestID: "r", CorrelationID: "c", SchemaID: "s", Strategy: "x", AnsweredBy: "a", AskedFirst: "f", Escalated: "e"}},
		{"strings are dropped when any record differs", []Record{
			{Op: "is", Provider: "p", Model: "m", RequestedModel: "rm", ServiceTier: "t", RequestID: "r", CorrelationID: "c", SchemaID: "s", Strategy: "x", AnsweredBy: "a", AskedFirst: "f", Escalated: "e"},
			{Op: "which", Provider: "q", Model: "n", RequestedModel: "rn", ServiceTier: "u", RequestID: "s", CorrelationID: "d", SchemaID: "t", Strategy: "y", AnsweredBy: "b", AskedFirst: "g", Escalated: "h"},
		}, Record{}},
		{"a string one record lacks is dropped", []Record{{Model: "m"}, {}}, Record{}},
		{"typed is true only if every record is typed", []Record{{Typed: true}, {Typed: true}}, Record{Typed: true}},
		{"one record that is not typed makes typed false", []Record{{Typed: true}, {}}, Record{}},
		{"checks, asked, body and provenance are not carried", []Record{
			{Checks: []Check{{Name: "a"}}, Asked: []Asked{{Question: "q"}}, Body: "b", Provenance: &Provenance{ResultID: "x"}},
			{Checks: []Check{{Name: "b"}}, Asked: []Asked{{Question: "r"}}, Body: "c", Provenance: &Provenance{ResultID: "y"}},
		}, Record{}},
		{"the confidence of the decision provider asked first is not carried", []Record{{AskedFirstConfidence: 0.2, AskedFirstReported: true}, {AskedFirstConfidence: 0.4, AskedFirstReported: true}}, Record{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sumRecords(c.records)
			want := c.want
			// Typed and the two "all" fields start true for a sum of records; a case
			// that does not mention them expects them false only if a record said so.
			if len(c.records) > 0 {
				allTyped, allPriced, allReused := true, true, true
				sent, timed := false, false
				for _, r := range c.records {
					allTyped = allTyped && r.Typed
					if r.Requests > 0 {
						sent = true
						allPriced = allPriced && r.Cost.Priced
					}
					if r.Timing.Calls > 0 {
						timed = true
						allReused = allReused && r.Timing.Reused
					}
				}
				allPriced, allReused = allPriced && sent, allReused && timed
				if allTyped != want.Typed || allPriced != want.Cost.Priced || allReused != want.Timing.Reused {
					t.Fatalf("the case expects Typed=%v Priced=%v Reused=%v, which the records do not give (%v %v %v)", want.Typed, want.Cost.Priced, want.Timing.Reused, allTyped, allPriced, allReused)
				}
			}
			// The start and the elapsed time of several records are checked in
			// TestTotalStartAndElapsed.
			if len(c.records) > 1 {
				want.Start, want.Elapsed = got.Start, got.Elapsed
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got  %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestTotalStartAndElapsed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		records     []Record
		wantStart   time.Time
		wantElapsed time.Duration
	}{
		{"one record", []Record{{Start: at(5), Elapsed: 2 * time.Second}}, at(5), 2 * time.Second},
		{"two records one after the other: the span, gap included", []Record{{Start: at(0), Elapsed: time.Second}, {Start: at(3), Elapsed: time.Second}}, at(0), 4 * time.Second},
		{"overlapping records add up to the time that passed and not more", []Record{{Start: at(0), Elapsed: 4 * time.Second}, {Start: at(1), Elapsed: 4 * time.Second}}, at(0), 5 * time.Second},
		{"a record inside another", []Record{{Start: at(0), Elapsed: 10 * time.Second}, {Start: at(2), Elapsed: time.Second}}, at(0), 10 * time.Second},
		{"the order of the records does not matter", []Record{{Start: at(3), Elapsed: time.Second}, {Start: at(0), Elapsed: time.Second}}, at(0), 4 * time.Second},
		{"identical records", []Record{{Start: at(0), Elapsed: time.Second}, {Start: at(0), Elapsed: time.Second}, {Start: at(0), Elapsed: time.Second}}, at(0), time.Second},
		{"fifty records that ran at once", func() []Record {
			records := make([]Record, 50)
			for i := range records {
				records[i] = Record{Start: at(0), Elapsed: time.Duration(i+1) * time.Millisecond}
			}
			return records
		}(), at(0), 50 * time.Millisecond},
		{"records with no start time keep the longest elapsed", []Record{{Elapsed: time.Second}, {Elapsed: 3 * time.Second}}, time.Time{}, 3 * time.Second},
		{"a record with no start beside one with a start", []Record{{Start: at(0), Elapsed: time.Second}, {Elapsed: 5 * time.Second}}, at(0), 5 * time.Second},
		{"a record that took no time", []Record{{Start: at(2)}}, at(2), 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sumRecords(c.records)
			if !got.Start.Equal(c.wantStart) || got.Elapsed != c.wantElapsed {
				t.Errorf("Start = %v, Elapsed = %v, want %v and %v", got.Start, got.Elapsed, c.wantStart, c.wantElapsed)
			}
		})
	}
}

func TestAgreed(t *testing.T) {
	t.Parallel()
	field := func(r Record) string { return r.Model }
	cases := []struct {
		name   string
		models []string
		want   string
	}{
		{"one record", []string{"a"}, "a"},
		{"two that agree", []string{"a", "a"}, "a"},
		{"two that differ", []string{"a", "b"}, ""},
		{"three with the odd one last", []string{"a", "a", "b"}, ""},
		{"three with the odd one first", []string{"b", "a", "a"}, ""},
		{"all empty", []string{"", ""}, ""},
		{"an empty one among others", []string{"a", "", "a"}, ""},
		{"ten that agree", strings.Split(strings.Repeat("m,", 10)[:19], ","), "m"},
		{"differing in case", []string{"A", "a"}, ""},
		{"differing in a trailing space", []string{"a", "a "}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			records := make([]Record, len(c.models))
			for i, m := range c.models {
				records[i].Model = m
			}
			if got := agreed(records, field); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// deliver gives outcome to every sink of a call made under ctx on cfg, as the
// call path's End does.
func deliver(ctx context.Context, cfg *ops.ExecConfig, outcome *ops.Outcome) {
	ops.EachSink(ctx, cfg, func(s ops.Sink) { s.Add(ctx, outcome) })
}

// oneOutcome is an outcome of an operation called op that sent n requests.
func oneOutcome(op string, n int) *ops.Outcome {
	return &ops.Outcome{Start: at(0), Meta: types.Meta{Operation: op, Attempts: n, Elapsed: time.Second, Usage: types.TokenUsage{PromptTokens: n, TotalTokens: n}}}
}

func TestARecorderCollectsTheRecordsOfTheCallsUnderItsContext(t *testing.T) {
	t.Parallel()
	t.Run("the zero recorder is ready and empty", func(t *testing.T) {
		var rec Recorder
		if len(rec.Records()) != 0 || rec.Total().Requests != 0 {
			t.Error("a new recorder is not empty")
		}
		rec.Reset()
	})
	t.Run("a record is added for each call, in order", func(t *testing.T) {
		var rec Recorder
		ctx := WithRecorder(context.Background(), &rec)
		for i, op := range []string{"is", "sort", "filter"} {
			deliver(ctx, nil, oneOutcome(op, i+1))
		}
		records := rec.Records()
		if len(records) != 3 || records[0].Op != "is" || records[1].Op != "sort" || records[2].Op != "filter" || records[2].Requests != 3 {
			t.Errorf("got %+v", records)
		}
	})
	t.Run("a context derived from the recorder's reaches it", func(t *testing.T) {
		var rec Recorder
		ctx, cancel := context.WithCancel(WithRecorder(context.Background(), &rec))
		defer cancel()
		deliver(context.WithValue(ctx, struct{}{}, 1), nil, oneOutcome("is", 1))
		if len(rec.Records()) != 1 {
			t.Error("not recorded")
		}
	})
	t.Run("a call under another context is not recorded", func(t *testing.T) {
		var rec Recorder
		WithRecorder(context.Background(), &rec)
		deliver(context.Background(), nil, oneOutcome("is", 1))
		if len(rec.Records()) != 0 {
			t.Error("recorded")
		}
	})
	t.Run("the records are the caller's", func(t *testing.T) {
		var rec Recorder
		deliver(WithRecorder(context.Background(), &rec), nil, oneOutcome("is", 1))
		records := rec.Records()
		records[0].Op = "changed"
		if rec.Records()[0].Op != "is" {
			t.Error("the slice is shared")
		}
	})
	t.Run("Reset forgets every record", func(t *testing.T) {
		var rec Recorder
		ctx := WithRecorder(context.Background(), &rec)
		deliver(ctx, nil, oneOutcome("is", 1))
		deliver(ctx, nil, oneOutcome("is", 1))
		rec.Reset()
		if len(rec.Records()) != 0 || !reflect.DeepEqual(rec.Total(), Record{}) {
			t.Error("not empty after Reset")
		}
		deliver(ctx, nil, oneOutcome("sort", 1))
		if len(rec.Records()) != 1 {
			t.Error("a recorder does not work after Reset")
		}
	})
	t.Run("Total adds the records", func(t *testing.T) {
		var rec Recorder
		ctx := WithRecorder(context.Background(), &rec)
		deliver(ctx, nil, oneOutcome("is", 1))
		deliver(ctx, nil, oneOutcome("is", 2))
		deliver(ctx, nil, oneOutcome("is", 3))
		total := rec.Total()
		if total.Requests != 6 || total.Tokens.Input != 6 || total.Op != "is" {
			t.Errorf("got %+v", total)
		}
	})
	t.Run("a nil recorder answers empty", func(t *testing.T) {
		var rec *Recorder
		if rec.Records() != nil || !reflect.DeepEqual(rec.Total(), Record{}) {
			t.Error("not empty")
		}
		rec.Reset()
	})
	t.Run("a nil outcome adds nothing", func(t *testing.T) {
		var rec Recorder
		(*sink)(&rec).Add(context.Background(), nil)
		if len(rec.Records()) != 0 {
			t.Error("recorded")
		}
	})
	t.Run("a Recorder shows a caller three methods", func(t *testing.T) {
		if n := reflect.TypeOf(&Recorder{}).NumMethod(); n != 3 {
			t.Errorf("a *Recorder has %d exported methods, want Records, Total and Reset", n)
		}
	})
}

func TestRecordersCompose(t *testing.T) {
	t.Parallel()
	t.Run("every recorder on the context chain receives the record once", func(t *testing.T) {
		var outer, middle, inner Recorder
		ctx := WithRecorder(context.Background(), &outer)
		ctx = WithRecorder(ctx, &middle)
		ctx = WithRecorder(ctx, &inner)
		deliver(ctx, nil, oneOutcome("is", 1))
		for name, r := range map[string]*Recorder{"outer": &outer, "middle": &middle, "inner": &inner} {
			if n := len(r.Records()); n != 1 {
				t.Errorf("the %s recorder has %d records", name, n)
			}
		}
	})
	t.Run("the nearest recorder is told first", func(t *testing.T) {
		var order []string
		var mu sync.Mutex
		note := func(name string) *Recorder {
			return &Recorder{Handle: func(context.Context, Record) { mu.Lock(); order = append(order, name); mu.Unlock() }}
		}
		ctx := WithRecorder(context.Background(), note("outer"))
		ctx = WithRecorder(ctx, note("inner"))
		deliver(ctx, nil, oneOutcome("is", 1))
		if strings.Join(order, ",") != "inner,outer" {
			t.Errorf("order = %v", order)
		}
	})
	t.Run("the client's recorder is told after those of the context", func(t *testing.T) {
		var order []string
		note := func(name string) *Recorder {
			return &Recorder{Handle: func(context.Context, Record) { order = append(order, name) }}
		}
		clients := note("client")
		ctx := WithRecorder(context.Background(), note("context"))
		deliver(ctx, &ops.ExecConfig{Sink: (*sink)(clients)}, oneOutcome("is", 1))
		if strings.Join(order, ",") != "context,client" {
			t.Errorf("order = %v", order)
		}
	})
	t.Run("a recorder placed twice receives the record once", func(t *testing.T) {
		var rec Recorder
		ctx := WithRecorder(WithRecorder(context.Background(), &rec), &rec)
		deliver(ctx, &ops.ExecConfig{Sink: (*sink)(&rec)}, oneOutcome("is", 1))
		if n := len(rec.Records()); n != 1 {
			t.Errorf("the recorder has %d records", n)
		}
	})
	t.Run("each recorder is given only what it asked for", func(t *testing.T) {
		var plain, decisions, bodies, both Recorder
		decisions.Decisions = true
		bodies.Bodies = true
		bodies.MaxBody = 4
		both.Decisions, both.Bodies = true, true
		ctx := context.Background()
		for _, r := range []*Recorder{&plain, &decisions, &bodies, &both} {
			ctx = WithRecorder(ctx, r)
		}
		outcome := oneOutcome("is", 1)
		outcome.Asked = []ops.AnsweredQuestion{{Question: "q", Answer: true}}
		outcome.Body = "abcdefgh"
		deliver(ctx, nil, outcome)
		get := func(r *Recorder) Record { return r.Records()[0] }
		if r := get(&plain); r.Asked != nil || r.Body != "" {
			t.Errorf("plain: %+v", r)
		}
		if r := get(&decisions); len(r.Asked) != 1 || r.Body != "" {
			t.Errorf("decisions: %+v", r)
		}
		if r := get(&bodies); r.Asked != nil || r.Body != "abcd" {
			t.Errorf("bodies: %+v", r)
		}
		if r := get(&both); len(r.Asked) != 1 || r.Body != "abcdefgh" {
			t.Errorf("both: %+v", r)
		}
	})
	t.Run("a record given to one recorder is not shared with another", func(t *testing.T) {
		var first, second Recorder
		ctx := WithRecorder(WithRecorder(context.Background(), &first), &second)
		outcome := oneOutcome("is", 1)
		outcome.Meta.Checks = []types.Check{{Name: "schema"}}
		deliver(ctx, nil, outcome)
		first.records[0].Checks[0].Name = "changed"
		if second.records[0].Checks[0].Name != "schema" {
			t.Error("the two recorders share their checks")
		}
	})
}

func TestARecorderWithHandleKeepsNothing(t *testing.T) {
	t.Parallel()
	t.Run("Handle is called once per record and the recorder keeps none", func(t *testing.T) {
		var calls []string
		rec := Recorder{Handle: func(_ context.Context, r Record) { calls = append(calls, r.Op) }}
		ctx := WithRecorder(context.Background(), &rec)
		for _, op := range []string{"is", "sort", "filter", "rank"} {
			deliver(ctx, nil, oneOutcome(op, 1))
		}
		if strings.Join(calls, ",") != "is,sort,filter,rank" {
			t.Errorf("Handle was called with %v", calls)
		}
		if len(rec.Records()) != 0 || !reflect.DeepEqual(rec.Total(), Record{}) {
			t.Error("a recorder with Handle kept a record")
		}
	})
	t.Run("Handle is given the call's context", func(t *testing.T) {
		type key struct{}
		var got any
		rec := Recorder{Handle: func(ctx context.Context, _ Record) { got = ctx.Value(key{}) }}
		ctx := context.WithValue(WithRecorder(context.Background(), &rec), key{}, "v")
		deliver(ctx, nil, oneOutcome("is", 1))
		if got != "v" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("a panic in Handle is not recovered", func(t *testing.T) {
		rec := Recorder{Handle: func(context.Context, Record) { panic("the caller's own bug") }}
		defer func() {
			if recover() == nil {
				t.Error("the panic was swallowed")
			}
		}()
		deliver(WithRecorder(context.Background(), &rec), nil, oneOutcome("is", 1))
	})
	t.Run("Handle runs with no lock of the recorder held", func(t *testing.T) {
		var rec Recorder
		rec.Handle = func(context.Context, Record) {
			rec.Reset()
			rec.Records()
			rec.Total()
		}
		done := make(chan struct{})
		go func() {
			deliver(WithRecorder(context.Background(), &rec), nil, oneOutcome("is", 1))
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Handle deadlocked on the recorder's own lock")
		}
	})
}

func TestARecorderReadsItsSettingsOnce(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		recorder     *Recorder
		decisions    bool
		bodies       bool
		maxBody      int
		checkMessage string
	}{
		{"the zero recorder wants neither", &Recorder{}, false, false, 4096, ""},
		{"decisions", &Recorder{Decisions: true}, true, false, 4096, ""},
		{"bodies at the default size", &Recorder{Bodies: true}, false, true, 4096, ""},
		{"bodies at a size of its own", &Recorder{Bodies: true, MaxBody: 100}, false, true, 100, ""},
		{"a size without bodies", &Recorder{MaxBody: 100}, false, false, 100, ""},
		{"both", &Recorder{Decisions: true, Bodies: true, MaxBody: 1}, true, true, 1, ""},
		{"a negative size keeps no body", &Recorder{Bodies: true, MaxBody: -1}, false, false, -1, "Recorder.MaxBody must not be negative"},
		{"a negative size without bodies is still refused", &Recorder{MaxBody: -5}, false, false, -5, "Recorder.MaxBody must not be negative"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := c.recorder
			decisions, bodies, maxBody := (*sink)(rec).Wants()
			if decisions != c.decisions || bodies != c.bodies {
				t.Errorf("Wants() = %v, %v, %d", decisions, bodies, maxBody)
			}
			if c.checkMessage == "" && maxBody != c.maxBody {
				t.Errorf("maxBody = %d, want %d", maxBody, c.maxBody)
			}
			err := rec.check()
			if c.checkMessage == "" && err != nil {
				t.Errorf("check() = %v", err)
			}
			if c.checkMessage != "" {
				var e *Error
				if !errors.As(err, &e) || e.Kind != ErrConfiguration || e.Message != c.checkMessage {
					t.Errorf("check() = %v", err)
				}
			}
		})
	}
	t.Run("a change after first use has no effect", func(t *testing.T) {
		rec := Recorder{Decisions: true}
		(*sink)(&rec).Wants()
		rec.Decisions, rec.Bodies, rec.MaxBody = false, true, 7
		decisions, bodies, maxBody := (*sink)(&rec).Wants()
		if !decisions || bodies || maxBody != 4096 {
			t.Errorf("Wants() = %v, %v, %d after a change", decisions, bodies, maxBody)
		}
	})
	t.Run("a nil recorder has nothing to check", func(t *testing.T) {
		var rec *Recorder
		if rec.check() != nil {
			t.Error("a nil recorder is refused")
		}
	})
}

func TestFiftyGoroutinesAddRecordsAtOnce(t *testing.T) {
	t.Parallel()
	t.Run("kept", func(t *testing.T) {
		var rec Recorder
		ctx := WithRecorder(context.Background(), &rec)
		var wg sync.WaitGroup
		for i := range 50 {
			wg.Go(func() {
				deliver(ctx, nil, oneOutcome("is", i+1))
				_ = rec.Records()
				_ = rec.Total()
			})
		}
		wg.Wait()
		if n := len(rec.Records()); n != 50 {
			t.Fatalf("%d records", n)
		}
		if total := rec.Total(); total.Requests != 50*51/2 {
			t.Errorf("Requests = %d", total.Requests)
		}
	})
	t.Run("handled", func(t *testing.T) {
		var calls, requests atomic.Int64
		rec := Recorder{Handle: func(_ context.Context, r Record) { calls.Add(1); requests.Add(int64(r.Requests)) }}
		ctx := WithRecorder(context.Background(), &rec)
		var wg sync.WaitGroup
		for i := range 50 {
			wg.Go(func() { deliver(ctx, nil, oneOutcome("is", i+1)) })
		}
		wg.Wait()
		if calls.Load() != 50 || requests.Load() != 50*51/2 {
			t.Errorf("%d calls, %d requests", calls.Load(), requests.Load())
		}
	})
	t.Run("several recorders, reset while records arrive", func(t *testing.T) {
		var a, b Recorder
		ctx := WithRecorder(WithRecorder(context.Background(), &a), &b)
		var wg sync.WaitGroup
		for range 50 {
			wg.Go(func() { deliver(ctx, nil, oneOutcome("is", 1)) })
		}
		wg.Go(func() { a.Reset() })
		wg.Wait()
		if len(b.Records()) != 50 {
			t.Errorf("the second recorder has %d records", len(b.Records()))
		}
	})
}

func TestWithRecorder(t *testing.T) {
	t.Run("a nil recorder returns the context", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), struct{}{}, 1)
		if got := WithRecorder(ctx, nil); got != ctx {
			t.Error("the context was changed")
		}
		if ops.HasSink(WithRecorder(ctx, nil), nil) {
			t.Error("a nil recorder is a sink")
		}
	})
	t.Run("a nil recorder costs nothing", func(t *testing.T) {
		ctx := context.Background()
		if allocs := testing.AllocsPerRun(100, func() { _ = WithRecorder(ctx, nil) }); allocs != 0 {
			t.Errorf("%v allocations", allocs)
		}
		if allocs := testing.AllocsPerRun(100, func() { _ = ops.HasSink(ctx, nil) }); allocs != 0 {
			t.Errorf("HasSink: %v allocations", allocs)
		}
	})
	t.Run("a recorder makes the context a sink", func(t *testing.T) {
		var rec Recorder
		if !ops.HasSink(WithRecorder(context.Background(), &rec), nil) {
			t.Error("not a sink")
		}
	})
	t.Run("a nil context panics as context.WithValue does", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("no panic")
			}
		}()
		var rec Recorder
		WithRecorder(nil, &rec) //nolint:staticcheck // the nil context is the case
	})
	t.Run("a nil recorder with a nil context returns it as it is", func(t *testing.T) {
		if WithRecorder(nil, nil) != nil { //nolint:staticcheck // the nil context is the case
			t.Error("not nil")
		}
	})
	t.Run("the recorder is one sink however many contexts derive from it", func(t *testing.T) {
		var rec Recorder
		base := WithRecorder(context.Background(), &rec)
		deliver(context.WithValue(base, struct{ a int }{1}, 1), nil, oneOutcome("a", 1))
		deliver(context.WithValue(base, struct{ a int }{2}, 2), nil, oneOutcome("b", 1))
		if len(rec.Records()) != 2 {
			t.Errorf("%d records", len(rec.Records()))
		}
	})
}

func TestARecordAndItsErrorMarshalWithoutThePayload(t *testing.T) {
	t.Parallel()
	outcome, _ := fullOutcome()
	outcome.Err = &llm.APIError{Provider: "openai", StatusCode: 400, Message: canary, Body: canary}
	rec := recordFromOutcome(&outcome, false, false, 4096)
	encoded, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), canary) {
		t.Errorf("the JSON of a record carries the marker: %s", encoded)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	errorField, ok := decoded["error"].(map[string]any)
	if !ok || errorField["kind"] != "invalid request" || errorField["status"] != 400.0 {
		t.Errorf("error = %v", decoded["error"])
	}
}
