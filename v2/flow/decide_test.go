package flow_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"
	"github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// The tests of Decide and Match share a few fixtures. A model here is a
// schemafluxtest.Provider, which names no model of its own per tier, so
// tierModel puts a different name on each tier and a test can tell which tier a
// request was made for.

// tierModel is a fake model that gives each tier a model of its own.
type tierModel struct{ *schemafluxtest.Provider }

func (tierModel) ModelFor(tier schemaflux.Tier) string {
	switch tier {
	case schemaflux.TierSmart:
		return "smart-model"
	case schemaflux.TierFast:
		return "fast-model"
	case schemaflux.TierQuick:
		return "quick-model"
	}
	return "no-tier-model"
}

// modelThatSays is a fake that answers every request with the bodies given, in
// order, the last repeating, and tells the tier a request was made for by its
// model.
func modelThatSays(bodies ...string) tierModel {
	var p schemafluxtest.Provider
	p.Reply(bodies...)
	return tierModel{&p}
}

// modelThatFails is a fake whose every request fails with err.
func modelThatFails(err error) tierModel {
	var p schemafluxtest.Provider
	p.Fail(err)
	return tierModel{&p}
}

// choice is the body of a model that selects decision number selected, with the
// alternatives given.
func choice(selected int, alternatives ...int) string {
	listed, _ := json.Marshal(append([]int{}, alternatives...))
	return fmt.Sprintf(`{"selected":%d,"explanation":"the model's own reason","confidence":0.8,"alternatives":%s}`, selected, listed)
}

// clientOn is a context carrying a client on p, with the settings applied.
func clientOn(p schemaflux.Provider, settings ...func(*schemaflux.Client)) context.Context {
	c := schemaflux.Client{Provider: p}
	for _, apply := range settings {
		apply(&c)
	}
	return schemaflux.WithClient(context.Background(), c)
}

// recordedBy is a client setting that gives the client a recorder.
func recordedBy(r *schemaflux.Recorder) func(*schemaflux.Client) {
	return func(c *schemaflux.Client) { c.Recorder = r }
}

// floorRouteOnce declares the route of the client that is held to a floor.
var floorRouteOnce sync.Once

// heldToFloor is a client setting that holds the client to a floor. The
// provider has to be named "operation-floor" (floorModel makes one), the one
// provider the policy allows.
func heldToFloor(level schemaflux.ContractLevel) func(*schemaflux.Client) {
	return func(c *schemaflux.Client) {
		floorRouteOnce.Do(func() { provider.DeclareRoute("operation-floor", "test-model", []string{"eu"}, 0) })
		c.Policy = &schemaflux.DataPolicy{Providers: []string{"operation-floor"}, Regions: []string{"eu"}, MinContract: level}
	}
}

// floorModel is a fake named for the policy of heldToFloor, answering with bodies.
func floorModel(bodies ...string) *schemafluxtest.Provider {
	var p schemafluxtest.Provider
	p.Reply(bodies...)
	p.SetName("operation-floor")
	return &p
}

// triage is what the tests of Decide decide among.
func triage() []flow.Decision[string] {
	return []flow.Decision[string]{
		{Value: "billing", Description: "a question about an invoice or a payment"},
		{Value: "technical", Description: "something is broken"},
		{Value: "general", Description: "anything else", Fallback: true},
	}
}

// zeroResult reports that a result is the zero value: nothing is returned beside
// an error.
func zeroResult[T any](r flow.DecideResult[T]) bool {
	return reflect.DeepEqual(r, flow.DecideResult[T]{})
}

// near reports that two sampling temperatures are the same, which a request
// holds as a float32.
func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// allKinds is every kind of error, with whether Decide's fallback stands in for
// it: the model could not be reached or its answer could not be used.
var allKinds = []struct {
	kind     schemaflux.Kind
	fallback bool
}{
	{schemaflux.ErrConfiguration, false},
	{schemaflux.ErrInvalidRequest, false},
	{schemaflux.ErrUnsupported, false},
	{schemaflux.ErrUnauthorized, false},
	{schemaflux.ErrPolicy, false},
	{schemaflux.ErrBudgetExceeded, false},
	{schemaflux.ErrBusy, false},
	{schemaflux.ErrRateLimited, true},
	{schemaflux.ErrUnavailable, true},
	{schemaflux.ErrTimeout, true},
	{schemaflux.ErrCanceled, false},
	{schemaflux.ErrClosed, false},
	{schemaflux.ErrTooLarge, false},
	{schemaflux.ErrTruncated, false},
	{schemaflux.ErrRefused, false},
	{schemaflux.ErrInvalidAnswer, true},
	{schemaflux.ErrReviewRequired, false},
	{schemaflux.ErrInternal, false},
}

func TestDecideTakesTheDecisionTheModelSelects(t *testing.T) {
	t.Parallel()
	for i, want := range []string{"billing", "technical", "general"} {
		t.Run(want, func(t *testing.T) {
			t.Parallel()
			model := modelThatSays(choice(i, (i+1)%3))
			got, err := flow.Decide(clientOn(model), "I was charged twice", triage())
			if err != nil {
				t.Fatalf("Decide = %v", err)
			}
			wantResult := flow.DecideResult[string]{
				Value: want, Index: i, Explanation: "the model's own reason", ModelConfidence: 0.8, Alternatives: []int{(i + 1) % 3},
			}
			if !reflect.DeepEqual(got, wantResult) {
				t.Errorf("Decide = %+v, want %+v", got, wantResult)
			}
			if model.Calls() != 1 {
				t.Errorf("the model was asked %d times, want once", model.Calls())
			}
		})
	}
}

func TestDecideSendsTheSituationAndTheDescriptionsAndNothingElseOfTheCaller(t *testing.T) {
	t.Parallel()
	model := modelThatSays(choice(0))
	decisions := triage()
	decisions[0].Value = "VALUE-NOT-SENT"
	decisions[0].Condition = func(any) bool { return false }
	_, err := flow.Decide(clientOn(model), map[string]string{"subject": "SITUATION-TEXT"}, decisions)
	if err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	text := req.System + "\n" + req.User
	for _, want := range []string{"SITUATION-TEXT", "a question about an invoice or a payment", "something is broken", "anything else"} {
		if !strings.Contains(text, want) {
			t.Errorf("the request does not say %q", want)
		}
	}
	if strings.Contains(text, "VALUE-NOT-SENT") {
		t.Error("a decision's Value was sent to the model: it is the caller's own, and the model chooses by number")
	}
	if req.Schema == nil || req.SchemaName == "" {
		t.Error("the answer was not declared as a schema")
	}
}

func TestDecideTakesTheFirstDecisionWhoseConditionHoldsAndAsksNoModel(t *testing.T) {
	t.Parallel()
	var seen []any
	hold := func(held bool) func(any) bool {
		return func(situation any) bool { seen = append(seen, situation); return held }
	}
	decisions := []flow.Decision[string]{
		{Value: "a", Description: "first", Condition: hold(false)},
		{Value: "b", Description: "second"},
		{Value: "c", Description: "third", Condition: hold(true)},
		{Value: "d", Description: "fourth", Condition: hold(true)},
	}
	model := modelThatSays(choice(0))
	got, err := flow.Decide(clientOn(model), "the situation", decisions)
	if err != nil {
		t.Fatal(err)
	}
	want := flow.DecideResult[string]{Value: "c", Index: 2, Explanation: "Condition met for: third", ByCondition: true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Decide = %+v, want %+v", got, want)
	}
	if model.Calls() != 0 {
		t.Errorf("a model was asked %d times though a condition held", model.Calls())
	}
	if len(seen) != 2 || seen[0] != "the situation" || seen[1] != "the situation" {
		t.Errorf("the conditions were given %v: each is given the situation, and the fourth is never reached", seen)
	}
}

func TestDecideAsksTheModelWhenNoConditionHolds(t *testing.T) {
	t.Parallel()
	decisions := []flow.Decision[string]{
		{Value: "a", Description: "first", Condition: func(any) bool { return false }},
		{Value: "b", Description: "second"},
	}
	model := modelThatSays(choice(1))
	got, err := flow.Decide(clientOn(model), "s", decisions)
	if err != nil || got.Value != "b" || got.Index != 1 || got.ByCondition {
		t.Fatalf("Decide = %+v, %v", got, err)
	}
	if model.Calls() != 1 {
		t.Errorf("the model was asked %d times", model.Calls())
	}
}

func TestDecideNeverReadsAnAnswerThatSelectsNothingAsAChoice(t *testing.T) {
	t.Parallel()
	answers := []struct{ name, body string }{
		{"no selection", `{"explanation":"x","confidence":0.5,"alternatives":[]}`},
		{"an empty object", `{}`},
		{"an empty answer", ``},
		{"not JSON", `I choose the first one`},
		{"null", `null`},
		{"a negative number", choice(-1)},
		{"a number past the list", choice(3)},
		{"a number far past the list", choice(1 << 40)},
		{"a fraction", `{"selected":1.5,"explanation":"x","confidence":0.5,"alternatives":[]}`},
		{"a string", `{"selected":"billing","explanation":"x","confidence":0.5,"alternatives":[]}`},
		{"null selection", `{"selected":null,"explanation":"x","confidence":0.5,"alternatives":[]}`},
		{"its own alternative", choice(1, 1)},
		{"an alternative past the list", choice(1, 7)},
		{"a repeated alternative", choice(0, 1, 1)},
		{"a confidence off the scale", `{"selected":1,"explanation":"x","confidence":7,"alternatives":[]}`},
	}
	for _, tc := range answers {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			decisions := triage()
			// Without a fallback the answer is an error and the result is empty.
			decisions[2].Fallback = false
			got, err := flow.Decide(clientOn(modelThatSays(tc.body)), "s", decisions)
			e := wantKind(t, err, schemaflux.ErrInvalidAnswer)
			if e.Op != "flow.decide" {
				t.Errorf("Op = %q", e.Op)
			}
			if !zeroResult(got) {
				t.Errorf("Decide returned %+v beside an error: an answer that selects nothing is never the first decision", got)
			}
			// With one the marked decision is taken, and it says so.
			decisions[2].Fallback = true
			got, err = flow.Decide(clientOn(modelThatSays(tc.body)), "s", decisions)
			if err != nil {
				t.Fatalf("Decide with a fallback = %v", err)
			}
			if !got.Fallback || got.Value != "general" || got.Index != 2 || got.ByCondition {
				t.Errorf("Decide with a fallback = %+v", got)
			}
		})
	}
}

func TestDecideTakesTheFallbackOnlyForAFailureTheModelCouldNotRecoverFrom(t *testing.T) {
	t.Parallel()
	for _, tc := range allKinds {
		t.Run(tc.kind.Error(), func(t *testing.T) {
			t.Parallel()
			model := modelThatFails(&schemaflux.Error{Kind: tc.kind, Op: "provider", Message: "the provider said no"})
			got, err := flow.Decide(clientOn(model), "s", triage())
			if tc.fallback {
				if err != nil {
					t.Fatalf("Decide = %v", err)
				}
				want := flow.DecideResult[string]{Value: "general", Index: 2, Explanation: got.Explanation, Fallback: true}
				if !reflect.DeepEqual(got, want) || got.Explanation == "" {
					t.Errorf("Decide = %+v, want the fallback %+v with an explanation", got, want)
				}
				return
			}
			wantKind(t, err, tc.kind)
			if !zeroResult(got) {
				t.Errorf("Decide returned %+v beside an error", got)
			}
		})
	}
}

func TestDecideWithNoFallbackReturnsEveryFailure(t *testing.T) {
	t.Parallel()
	decisions := triage()
	decisions[2].Fallback = false
	for _, tc := range allKinds {
		t.Run(tc.kind.Error(), func(t *testing.T) {
			t.Parallel()
			got, err := flow.Decide(clientOn(modelThatFails(&schemaflux.Error{Kind: tc.kind})), "s", decisions)
			wantKind(t, err, tc.kind)
			if !zeroResult(got) {
				t.Errorf("Decide returned %+v beside an error", got)
			}
		})
	}
}

func TestDecideReadsAFailureThatSaysNothingOfItsKindAsAnUnreachableProvider(t *testing.T) {
	t.Parallel()
	got, err := flow.Decide(clientOn(modelThatFails(errors.New("connection reset"))), "s", triage())
	if err != nil || !got.Fallback || got.Value != "general" {
		t.Errorf("Decide = %+v, %v: a plain error from a provider is read as the provider being unreachable", got, err)
	}
	got, err = flow.Decide(clientOn(modelThatFails(nil)), "s", triage())
	if err != nil || !got.Fallback {
		t.Errorf("Decide = %+v, %v", got, err)
	}
}

func TestDecideNeverFallsBackOnADefectOfTheLibraryOrTheProgram(t *testing.T) {
	t.Parallel()
	var p schemafluxtest.Provider
	p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
		panic("the provider panicked: DEFECT-CANARY")
	})
	got, err := flow.Decide(clientOn(&p), "s", triage())
	wantKind(t, err, schemaflux.ErrInternal)
	if !zeroResult(got) {
		t.Errorf("Decide returned %+v beside a defect", got)
	}
	if strings.Contains(err.Error(), "DEFECT-CANARY") {
		t.Errorf("the error carries what the panic threw: %v", err)
	}
}

func TestDecideNeverFallsBackWhenTheCallersContextHasEnded(t *testing.T) {
	t.Parallel()
	for _, done := range doneContexts() {
		t.Run("already "+done.name, func(t *testing.T) {
			t.Parallel()
			model := modelThatSays(choice(0))
			// A decision whose condition holds would not need the context, and is
			// still not taken: a call that is over starts nothing.
			decisions := triage()
			decisions[0].Condition = func(any) bool { return true }
			got, err := flow.Decide(schemaflux.WithClient(done.ctx, schemaflux.Client{Provider: model}), "s", decisions)
			e := wantKind(t, err, done.kind)
			if !errors.Is(e, done.is) {
				t.Errorf("errors.Is(%v, %v) is false", err, done.is)
			}
			if !zeroResult(got) || model.Calls() != 0 {
				t.Errorf("Decide = %+v after %d requests", got, model.Calls())
			}
		})
	}
	for _, failure := range []error{
		&schemaflux.Error{Kind: schemaflux.ErrTimeout},
		&schemaflux.Error{Kind: schemaflux.ErrUnavailable},
		&schemaflux.Error{Kind: schemaflux.ErrRateLimited},
		&schemaflux.Error{Kind: schemaflux.ErrInvalidAnswer},
		errors.New("plain"),
		nil,
	} {
		t.Run(fmt.Sprintf("cancelled during the call, the provider says %v", failure), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var p schemafluxtest.Provider
			p.RespondContext(func(ctx context.Context, _ int, _ schemaflux.Request) (schemaflux.Response, error) {
				cancel()
				if failure == nil {
					return schemaflux.Response{}, ctx.Err()
				}
				return schemaflux.Response{}, failure
			})
			got, err := flow.Decide(schemaflux.WithClient(ctx, schemaflux.Client{Provider: &p}), "s", triage())
			if err == nil {
				t.Fatalf("Decide = %+v with no error after the caller cancelled: the fallback hid the cancellation", got)
			}
			if !zeroResult(got) {
				t.Errorf("Decide returned %+v beside an error", got)
			}
		})
	}
}

func TestDecideNeverFallsBackWhenTheCallersDeadlinePassesDuringTheCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Reply(choice(0))
		p.Delay(time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		got, err := flow.Decide(schemaflux.WithClient(ctx, schemaflux.Client{Provider: &p}), "s", triage())
		wantKind(t, err, schemaflux.ErrTimeout)
		if !zeroResult(got) {
			t.Errorf("Decide returned %+v beside an error", got)
		}
	})
}

func TestDecideTakesTheFallbackWhenOneRequestTimesOutAndTheCallersContextIsLive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Reply(choice(0))
		p.Delay(time.Minute)
		ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &p, Timeout: time.Second})
		got, err := flow.Decide(ctx, "s", triage())
		if err != nil || !got.Fallback || got.Value != "general" {
			t.Fatalf("Decide = %+v, %v: the request's own timeout is the provider being unavailable", got, err)
		}
	})
}

func TestDecideDoesNotMistakeTheModelChoosingTheMarkedDecisionForTheFallback(t *testing.T) {
	t.Parallel()
	got, err := flow.Decide(clientOn(modelThatSays(choice(2))), "s", triage())
	if err != nil || got.Value != "general" || got.Index != 2 || got.Fallback {
		t.Errorf("Decide = %+v, %v: the model chose it, which is not the fallback", got, err)
	}
	if got.ModelConfidence != 0.8 || got.Explanation != "the model's own reason" {
		t.Errorf("Decide = %+v: the model's words and claim are kept", got)
	}
}

func TestDecideFallbackSaysNothingOfTheModelAndNothingOfTheFailure(t *testing.T) {
	t.Parallel()
	const vendor = "VENDOR-CANARY-3f9a"
	model := modelThatFails(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: vendor, Err: errors.New(vendor)})
	got, err := flow.Decide(clientOn(model), "s", triage())
	if err != nil {
		t.Fatal(err)
	}
	if got.ModelConfidence != 0 || got.Alternatives != nil || got.ByCondition {
		t.Errorf("Decide = %+v: no model's answer was used", got)
	}
	if strings.Contains(fmt.Sprintf("%+v", got), vendor) {
		t.Errorf("the result repeats the provider's text: %+v", got)
	}
}

func TestDecideTakesAnyValueTypeAndReturnsTheValueOfTheDecisionTaken(t *testing.T) {
	t.Parallel()
	type route struct {
		Queue string
		Slots []int
	}
	shared := &route{Queue: "shared"}
	decisions := []flow.Decision[*route]{
		{Value: &route{Queue: "first", Slots: []int{1}}, Description: "first"},
		{Value: shared, Description: "second"},
		{Value: nil, Description: "none", Fallback: true},
	}
	got, err := flow.Decide(clientOn(modelThatSays(choice(1))), "s", decisions)
	if err != nil || got.Value != shared {
		t.Fatalf("Decide = %+v, %v: the very value of the decision is returned", got, err)
	}
	got, err = flow.Decide(clientOn(modelThatFails(nil)), "s", decisions)
	if err != nil || !got.Fallback || got.Value != nil || got.Index != 2 {
		t.Fatalf("Decide = %+v, %v: a fallback whose value is nil is still the fallback", got, err)
	}
	ints, err := flow.Decide(clientOn(modelThatSays(choice(0))), "s", []flow.Decision[int]{{Value: 0, Description: "zero"}, {Value: 7, Description: "seven"}})
	if err != nil || ints.Value != 0 || ints.Index != 0 || ints.Fallback {
		t.Fatalf("Decide = %+v, %v: a decision whose value is the zero value is a decision", ints, err)
	}
}

func TestDecideRefusesWhatItCannotAskBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	one := func(f func(*flow.Decision[string])) []flow.Decision[string] {
		d := triage()
		f(&d[0])
		return d
	}
	cases := []struct {
		name      string
		situation any
		decisions []flow.Decision[string]
		opts      []schemaflux.Options
		kind      schemaflux.Kind
	}{
		{"no decisions", "s", nil, nil, schemaflux.ErrInvalidRequest},
		{"an empty list", "s", []flow.Decision[string]{}, nil, schemaflux.ErrInvalidRequest},
		{"no decisions and a bad option", "s", nil, []schemaflux.Options{{MaxOutputTokens: -1}}, schemaflux.ErrInvalidRequest},
		{"two decisions marked as the fallback", "s", []flow.Decision[string]{{Fallback: true}, {Fallback: true}}, nil, schemaflux.ErrConfiguration},
		{"three marked", "s", []flow.Decision[string]{{Fallback: true}, {Fallback: true}, {Fallback: true}}, nil, schemaflux.ErrConfiguration},
		{"a negative token ceiling", "s", triage(), []schemaflux.Options{{MaxOutputTokens: -1}}, schemaflux.ErrConfiguration},
		{"an unknown tier", "s", triage(), []schemaflux.Options{{Tier: 99}}, schemaflux.ErrConfiguration},
		{"an unknown mode", "s", triage(), []schemaflux.Options{{Mode: 99}}, schemaflux.ErrConfiguration},
		{"a bad option in the second value", "s", triage(), []schemaflux.Options{{Model: "m"}, {MaxOutputTokens: -5}}, schemaflux.ErrConfiguration},
		{"more decisions than one answer can be declared for", "s", make([]flow.Decision[string], 5000), nil, schemaflux.ErrConfiguration},
		{"a situation JSON cannot hold", make(chan int), triage(), nil, schemaflux.ErrInvalidRequest},
		{"a situation JSON cannot hold, and a fallback", func() {}, one(func(*flow.Decision[string]) {}), nil, schemaflux.ErrInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := modelThatSays(choice(0))
			got, err := flow.Decide(clientOn(model), tc.situation, tc.decisions, tc.opts...)
			e := wantKind(t, err, tc.kind)
			if e.Op != "flow.decide" {
				t.Errorf("Op = %q, want flow.decide", e.Op)
			}
			if !zeroResult(got) || model.Calls() != 0 {
				t.Errorf("Decide = %+v after %d requests", got, model.Calls())
			}
		})
	}
}

func TestDecideRefusesANilContext(t *testing.T) {
	t.Parallel()
	got, err := flow.Decide[string](nil, "s", triage()) //nolint:staticcheck // the nil context is the case
	e := wantKind(t, err, schemaflux.ErrInvalidRequest)
	if e.Op != "flow.decide" || !zeroResult(got) {
		t.Errorf("Decide = %+v, %v", got, err)
	}
}

func TestDecideTakesANumberOfAnySizeInItsOptionsAndSizesNothingByIt(t *testing.T) {
	t.Parallel()
	for _, n := range extremes {
		t.Run(sizeName(n), func(t *testing.T) {
			noPanic(t, "Decide", func() {
				model := modelThatSays(choice(1))
				var err error
				var got flow.DecideResult[string]
				bytes := allocatedBy(func() {
					got, err = flow.Decide(clientOn(model), "s", triage(), schemaflux.Options{MaxOutputTokens: n})
				})
				if n < 0 {
					wantKind(t, err, schemaflux.ErrConfiguration)
					if !zeroResult(got) || model.Calls() != 0 {
						t.Errorf("Decide = %+v after %d requests", got, model.Calls())
					}
					return
				}
				if err != nil || got.Value != "technical" {
					t.Fatalf("Decide = %+v, %v", got, err)
				}
				if n != 0 && model.Last().MaxTokens != n {
					t.Errorf("the request asked for %d tokens, want %d", model.Last().MaxTokens, n)
				}
				if bytes > 1<<20 {
					t.Errorf("a ceiling of %d made the call allocate %d bytes", n, bytes)
				}
			})
		})
	}
}

func TestDecideTakesAnyNumberOfDecisionsUpToWhatOneAnswerCanDeclare(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 2, 3, 50, 200} {
		t.Run(sizeName(n), func(t *testing.T) {
			t.Parallel()
			decisions := make([]flow.Decision[int], n)
			for i := range decisions {
				decisions[i] = flow.Decision[int]{Value: i * 10, Description: fmt.Sprint("way ", i)}
			}
			got, err := flow.Decide(clientOn(modelThatSays(choice(n-1))), "s", decisions)
			if err != nil || got.Value != (n-1)*10 || got.Index != n-1 {
				t.Fatalf("Decide of %d decisions = %+v, %v", n, got, err)
			}
		})
	}
	// Past the limit nothing is sent and the work is not sized by the count.
	model := modelThatSays(choice(0))
	huge := make([]flow.Decision[int], 1<<16)
	var err error
	bytes := allocatedBy(func() { _, err = flow.Decide(clientOn(model), "s", huge) })
	wantKind(t, err, schemaflux.ErrConfiguration)
	if model.Calls() != 0 {
		t.Errorf("%d requests for a list too long to ask about", model.Calls())
	}
	if bytes > 1<<22 {
		t.Errorf("refusing %d decisions allocated %d bytes", len(huge), bytes)
	}
}

func TestDecideRunsTheCallersConditionsOnTheCallersGoroutineAndLeavesNoGoroutine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		done := noLeak(t)
		var callers atomic.Int32
		decisions := triage()
		decisions[0].Condition = func(any) bool { callers.Add(1); return false }
		for range 3 {
			if _, err := flow.Decide(clientOn(modelThatSays(choice(1))), "s", decisions); err != nil {
				t.Fatal(err)
			}
			if _, err := flow.Decide(clientOn(modelThatFails(nil)), "s", decisions); err != nil {
				t.Fatal(err)
			}
		}
		if callers.Load() != 6 {
			t.Errorf("the condition ran %d times, want 6", callers.Load())
		}
		done()
	})
}

func TestDecideLetsAConditionsPanicThroughAndAsksNothing(t *testing.T) {
	t.Parallel()
	for _, recorded := range []bool{false, true} {
		t.Run(fmt.Sprint("recorded ", recorded), func(t *testing.T) {
			t.Parallel()
			model := modelThatSays(choice(0))
			var recorder schemaflux.Recorder
			var settings []func(*schemaflux.Client)
			if recorded {
				settings = append(settings, recordedBy(&recorder))
			}
			decisions := triage()
			decisions[0].Condition = func(any) bool { panic("the condition panicked") }
			var thrown any
			func() {
				defer func() { thrown = recover() }()
				_, _ = flow.Decide(clientOn(model, settings...), "s", decisions)
			}()
			if thrown != "the condition panicked" {
				t.Errorf("recovered %v: a panic of the caller's own function is the caller's", thrown)
			}
			if model.Calls() != 0 || len(recorder.Records()) != 0 {
				t.Errorf("%d requests and %d Records after a panic", model.Calls(), len(recorder.Records()))
			}
		})
	}
}

func TestDecideCommonOptionsChangeTheRequestAsTheyAreDocumented(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  []schemaflux.Options
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"no options: the Smart tier in Transform mode", nil, func(t *testing.T, r schemaflux.Request) {
			if r.Model != "smart-model" || !near(r.Temperature, 0.3) || r.MaxTokens == 0 {
				t.Errorf("model %q temperature %v tokens %d", r.Model, r.Temperature, r.MaxTokens)
			}
		}},
		{"the Fast tier", []schemaflux.Options{{Tier: schemaflux.TierFast}}, func(t *testing.T, r schemaflux.Request) {
			if r.Model != "fast-model" {
				t.Errorf("model %q", r.Model)
			}
		}},
		{"the Quick tier", []schemaflux.Options{{Tier: schemaflux.TierQuick}}, func(t *testing.T, r schemaflux.Request) {
			if r.Model != "quick-model" {
				t.Errorf("model %q", r.Model)
			}
		}},
		{"a pinned model", []schemaflux.Options{{Model: "pinned"}}, func(t *testing.T, r schemaflux.Request) {
			if r.Model != "pinned" {
				t.Errorf("model %q", r.Model)
			}
		}},
		{"a pinned model beats a tier", []schemaflux.Options{{Model: "pinned", Tier: schemaflux.TierQuick}}, func(t *testing.T, r schemaflux.Request) {
			if r.Model != "pinned" {
				t.Errorf("model %q", r.Model)
			}
		}},
		{"strict mode", []schemaflux.Options{{Mode: schemaflux.ModeStrict}}, func(t *testing.T, r schemaflux.Request) {
			if !near(r.Temperature, 0.1) {
				t.Errorf("temperature %v", r.Temperature)
			}
		}},
		{"creative mode", []schemaflux.Options{{Mode: schemaflux.ModeCreative}}, func(t *testing.T, r schemaflux.Request) {
			if !near(r.Temperature, 0.7) {
				t.Errorf("temperature %v", r.Temperature)
			}
		}},
		{"effort", []schemaflux.Options{{Effort: schemaflux.EffortLow}}, func(t *testing.T, r schemaflux.Request) {
			if r.Effort != schemaflux.EffortLow {
				t.Errorf("effort %q", r.Effort)
			}
		}},
		{"steering", []schemaflux.Options{{Steering: "STEER-TEXT"}}, func(t *testing.T, r schemaflux.Request) {
			if !strings.Contains(r.System+r.User, "STEER-TEXT") {
				t.Error("the steering was not sent")
			}
		}},
		{"a token ceiling", []schemaflux.Options{{MaxOutputTokens: 321}}, func(t *testing.T, r schemaflux.Request) {
			if r.MaxTokens != 321 {
				t.Errorf("tokens %d", r.MaxTokens)
			}
		}},
		{"web search", []schemaflux.Options{{WebSearch: true}}, func(t *testing.T, r schemaflux.Request) {
			if !r.WebSearch {
				t.Error("web search was not declared")
			}
		}},
		{"several values: a later field replaces an earlier one", []schemaflux.Options{{Model: "early", MaxOutputTokens: 100}, {Model: "late"}}, func(t *testing.T, r schemaflux.Request) {
			if r.Model != "late" || r.MaxTokens != 100 {
				t.Errorf("model %q tokens %d: the later model replaces, the zero ceiling leaves the earlier", r.Model, r.MaxTokens)
			}
		}},
		{"several values: a later zero never overwrites", []schemaflux.Options{
			{Model: "m", Tier: schemaflux.TierQuick, Effort: schemaflux.EffortLow, Mode: schemaflux.ModeStrict, Steering: "KEEP-STEERING", MaxOutputTokens: 77, WebSearch: true},
			{},
			{WebSearch: false},
		}, func(t *testing.T, r schemaflux.Request) {
			if r.Model != "m" || r.Effort != schemaflux.EffortLow || !near(r.Temperature, 0.1) || r.MaxTokens != 77 || !r.WebSearch || !strings.Contains(r.System+r.User, "KEEP-STEERING") {
				t.Errorf("an earlier setting was lost: %+v", r)
			}
		}},
		{"several values: every field of the later one counts", []schemaflux.Options{
			{Model: "a", Tier: schemaflux.TierFast, Effort: schemaflux.EffortLow, Mode: schemaflux.ModeStrict, Steering: "EARLY", MaxOutputTokens: 1},
			{Model: "b", Tier: schemaflux.TierQuick, Effort: schemaflux.EffortHigh, Mode: schemaflux.ModeCreative, Steering: "LATE", MaxOutputTokens: 2, WebSearch: true},
		}, func(t *testing.T, r schemaflux.Request) {
			text := r.System + r.User
			if r.Model != "b" || r.Effort != schemaflux.EffortHigh || !near(r.Temperature, 0.7) || r.MaxTokens != 2 || !r.WebSearch || !strings.Contains(text, "LATE") || strings.Contains(text, "EARLY") {
				t.Errorf("the later values did not all replace the earlier: %+v", r)
			}
		}},
		{"three values merge left to right", []schemaflux.Options{{Model: "a"}, {Model: "b", Steering: "S2"}, {Steering: "S3"}}, func(t *testing.T, r schemaflux.Request) {
			text := r.System + r.User
			if r.Model != "b" || !strings.Contains(text, "S3") || strings.Contains(text, "S2") {
				t.Errorf("model %q", r.Model)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := modelThatSays(choice(0))
			if _, err := flow.Decide(clientOn(model), "s", triage(), tc.opts...); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestDecideDoesNotChangeTheOptionsOrTheDecisionsItIsGiven(t *testing.T) {
	t.Parallel()
	first, second := schemaflux.Options{Model: "a", MaxOutputTokens: 5}, schemaflux.Options{Model: "b"}
	decisions := triage()
	conditionsBefore := fmt.Sprintf("%v", decisions)
	if _, err := flow.Decide(clientOn(modelThatSays(choice(0))), "s", decisions, first, second); err != nil {
		t.Fatal(err)
	}
	if first != (schemaflux.Options{Model: "a", MaxOutputTokens: 5}) || second != (schemaflux.Options{Model: "b"}) {
		t.Errorf("the options were changed: %+v %+v", first, second)
	}
	if fmt.Sprintf("%v", decisions) != conditionsBefore || !decisions[2].Fallback || decisions[0].Value != "billing" {
		t.Errorf("the decisions were changed: %+v", decisions)
	}
}

func TestDecideResultIsWrittenAsJSONTheWayTheOtherResultsAre(t *testing.T) {
	t.Parallel()
	got, err := flow.Decide(clientOn(modelThatSays(choice(1, 0))), "s", triage())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"value":"technical","index":1,"explanation":"the model's own reason","confidence":0.8,"alternatives":[0],"fallback":false,"by_condition":false}`
	if string(encoded) != want {
		t.Errorf("the result is written as %s, want %s", encoded, want)
	}
	byCondition, err := flow.Decide(clientOn(modelThatSays(choice(1))), "s", []flow.Decision[int]{{Value: 4, Description: "four", Condition: func(any) bool { return true }}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(byCondition)
	if want := `{"value":4,"index":0,"explanation":"Condition met for: four","confidence":0,"fallback":false,"by_condition":true}`; string(encoded) != want {
		t.Errorf("the result is written as %s, want %s", encoded, want)
	}
}
