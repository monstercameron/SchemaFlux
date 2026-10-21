package schemaflux_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/mw"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// whetherSteps returns two steps that count how often each runs and return the
// word that names it, and the two counters.
func whetherSteps() (then, otherwise schemaflux.Step[string], thens, otherwises *atomic.Int32) {
	thens, otherwises = new(atomic.Int32), new(atomic.Int32)
	then = func(context.Context) (string, error) { thens.Add(1); return "then", nil }
	otherwise = func(context.Context) (string, error) { otherwises.Add(1); return "otherwise", nil }
	return then, otherwise, thens, otherwises
}

// routeSteps returns a route for each label that counts how often it runs and
// returns its own label.
func routeSteps(labels ...string) ([]schemaflux.Route[string], map[string]*atomic.Int32) {
	counts := make(map[string]*atomic.Int32, len(labels))
	routes := make([]schemaflux.Route[string], len(labels))
	for i, label := range labels {
		counter := new(atomic.Int32)
		counts[label] = counter
		routes[i] = schemaflux.To(label, func(context.Context) (string, error) { counter.Add(1); return label, nil })
	}
	return routes, counts
}

func TestWhetherRunsThenOnAYesAndOtherwiseOnANoAndNeverTheOther(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		body          string
		want          string
		thens, others int32
	}{{isYes, "then", 1, 0}, {isNo, "otherwise", 0, 1}} {
		then, otherwise, thens, others := whetherSteps()
		model := answeringWith(tc.body)
		got, err := schemaflux.Whether(operationClient(model), isRefundMsg, isRefundAsk, then, otherwise)
		if err != nil || got != tc.want {
			t.Fatalf("Whether = %q, %v, want %q", got, err, tc.want)
		}
		if thens.Load() != tc.thens || others.Load() != tc.others || model.Calls() != 1 {
			t.Errorf("then ran %d times, otherwise %d, %d requests were sent: want the chosen step once and one request", thens.Load(), others.Load(), model.Calls())
		}
	}
}

func TestWhetherAsksTheRequestIsAsksAndAddsNothingToIt(t *testing.T) {
	t.Parallel()
	opts := schemaflux.Options{Model: "pinned", Steering: "refunds only", Tier: schemaflux.TierSmart}
	asked := answeringWith(isYes)
	if _, err := schemaflux.Is(operationClient(asked), isRefundMsg, isRefundAsk, opts); err != nil {
		t.Fatal(err)
	}
	then, otherwise, _, _ := whetherSteps()
	routed := answeringWith(isYes)
	if _, err := schemaflux.Whether(operationClient(routed), isRefundMsg, isRefundAsk, then, otherwise, opts); err != nil {
		t.Fatal(err)
	}
	a, b := asked.Last(), routed.Last()
	if requestText(a) != requestText(b) || a.SchemaName != b.SchemaName || a.Model != b.Model || a.MaxTokens != b.MaxTokens || a.Temperature != b.Temperature || a.Effort != b.Effort || a.Format != b.Format {
		t.Error("Whether asked another request than Is asks")
	}
}

func TestWhetherAddsTheDecisionsRecordBeforeTheStepRuns(t *testing.T) {
	t.Parallel()
	var order []string
	var mu sync.Mutex
	note := func(what string) { mu.Lock(); order = append(order, what); mu.Unlock() }
	recorder := &schemaflux.Recorder{Handle: func(_ context.Context, r schemaflux.Record) { note("record " + r.Op) }}
	step := func(context.Context) (string, error) {
		note("step")
		return "done", nil
	}
	got, err := schemaflux.Whether(operationClient(answeringWith(isYes), recordingTo(recorder)), isRefundMsg, isRefundAsk, step, step)
	if err != nil || got != "done" {
		t.Fatalf("Whether = %q, %v", got, err)
	}
	if strings.Join(order, ",") != "record is,step" {
		t.Errorf("order %v: the decision's Record comes before the step", order)
	}
}

func TestWhetherHasTheDecisionsRecordInAKeepingRecorderWhenTheStepRuns(t *testing.T) {
	t.Parallel()
	recorder := &schemaflux.Recorder{}
	var held []schemaflux.Record
	step := func(context.Context) (string, error) {
		held = recorder.Records()
		return "done", nil
	}
	if _, err := schemaflux.Whether(operationClient(answeringWith(isNo), recordingTo(recorder)), isRefundMsg, isRefundAsk, step, step); err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0].Op != "is" || held[0].Err != nil {
		t.Errorf("the recorder held %+v when the step ran, want the decision's Record", held)
	}
}

func TestWhetherAddsOneRecordForTheDecisionAndNoneOfItsOwn(t *testing.T) {
	t.Parallel()
	recorder := &schemaflux.Recorder{Decisions: true}
	then, otherwise, _, _ := whetherSteps()
	if _, err := schemaflux.Whether(operationClient(answeringWith(isNo), recordingTo(recorder)), isRefundMsg, isRefundAsk, then, otherwise); err != nil {
		t.Fatal(err)
	}
	r := onlyRecord(t, recorder)
	if r.Op != "is" || r.Err != nil || r.Requests != 1 || r.AnsweredBy != "test" || r.Typed {
		t.Errorf("record = %q err %v requests %d answered by %q typed %v", r.Op, r.Err, r.Requests, r.AnsweredBy, r.Typed)
	}
	if len(r.Asked) != 1 || r.Asked[0].Question != isRefundAsk || r.Asked[0].Answer != false {
		t.Errorf("a recorder that asked for decisions was given %+v", r.Asked)
	}
	if r.Requested != schemaflux.ContractSchemaAndInvariantChecked || r.Delivered != schemaflux.ContractSchemaAndInvariantChecked {
		t.Errorf("levels = %v and %v", r.Requested, r.Delivered)
	}
}

func TestWhetherReturnsTheStepsErrorAsItWasGivenAndTheDecisionsRecordHasNone(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("the step failed")
	custom := &schemaflux.Error{Kind: schemaflux.ErrRateLimited, Op: "mine", Message: "the step's own"}
	type shaped struct{ error }
	wrapped := shaped{sentinel}
	for name, fail := range map[string]error{"a plain error": sentinel, "an *Error of the library's type": custom, "an error of the caller's type": wrapped} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			failing := func(context.Context) (string, error) { return "a value beside the error", fail }
			other, _, _, _ := whetherSteps()
			got, err := schemaflux.Whether(operationClient(answeringWith(isYes), recordingTo(&recorder)), isRefundMsg, isRefundAsk, failing, other)
			if err != fail {
				t.Errorf("the error is %v (%T), want the same value the step returned", err, err)
			}
			if got != "" {
				t.Errorf("a failed call returned %q beside the error", got)
			}
			if r := onlyRecord(t, &recorder); r.Err != nil || r.Op != "is" {
				t.Errorf("record = %q err %v: the decision was made, and the step's error is not its own", r.Op, r.Err)
			}
		})
	}
}

func TestWhetherRefusesANilStepBeforeAskingTheModelWhicheverStepTheAnswerWouldChoose(t *testing.T) {
	t.Parallel()
	step := func(context.Context) (string, error) { return "ran", nil }
	cases := []struct {
		name            string
		then, otherwise schemaflux.Step[string]
		message         string
	}{
		{"then", nil, step, "then is nil"},
		{"otherwise", step, nil, "otherwise is nil"},
		{"both", nil, nil, "then is nil"},
	}
	for _, tc := range cases {
		for _, body := range []string{isYes, isNo} {
			t.Run(tc.name+" "+body, func(t *testing.T) {
				t.Parallel()
				var recorder schemaflux.Recorder
				model := answeringWith(body)
				got, err := schemaflux.Whether(operationClient(model, recordingTo(&recorder)), isRefundMsg, isRefundAsk, tc.then, tc.otherwise)
				e := wantError(t, err, schemaflux.ErrInvalidRequest, "whether")
				if e.Message != tc.message || got != "" || model.Calls() != 0 || len(recorder.Records()) != 0 {
					t.Errorf("message %q, answer %q after %d requests with %d Records: a nil step is refused before the model is asked", e.Message, got, model.Calls(), len(recorder.Records()))
				}
			})
		}
	}
}

func TestWhetherNeverReadsAFailureAsANo(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*schemafluxtest.Provider){
		"an answer that is text":     func(p *schemafluxtest.Provider) { p.Reply("Yes.") },
		"an answer that is a string": func(p *schemafluxtest.Provider) { p.Reply(`{"q1":"no"}`) },
		"a rate limit":               func(p *schemafluxtest.Provider) { p.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited}) },
		"an unavailable provider":    func(p *schemafluxtest.Provider) { p.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable}) },
	}
	kinds := map[string]schemaflux.Kind{
		"an answer that is text":     schemaflux.ErrInvalidAnswer,
		"an answer that is a string": schemaflux.ErrInvalidAnswer,
		"a rate limit":               schemaflux.ErrRateLimited,
		"an unavailable provider":    schemaflux.ErrUnavailable,
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			setup(&model)
			then, otherwise, thens, others := whetherSteps()
			var recorder schemaflux.Recorder
			got, err := schemaflux.Whether(operationClient(&model, recordingTo(&recorder)), isRefundMsg, isRefundAsk, then, otherwise)
			wantError(t, err, kinds[name], "is")
			if got != "" || thens.Load() != 0 || others.Load() != 0 {
				t.Errorf("answer %q, then ran %d times and otherwise %d: neither runs when the question cannot be answered", got, thens.Load(), others.Load())
			}
			if r := onlyRecord(t, &recorder); r.Op != "is" || r.Err != err {
				t.Errorf("record = %q err %v", r.Op, r.Err)
			}
		})
	}
}

func TestWhetherRefusesABlankQuestionABadOptionAndANilContextAndRunsNoStep(t *testing.T) {
	t.Parallel()
	then, otherwise, thens, others := whetherSteps()
	var recorder schemaflux.Recorder
	model := answeringWith(isYes)
	ctx := operationClient(model, recordingTo(&recorder))

	_, err := schemaflux.Whether(ctx, isRefundMsg, " \t", then, otherwise)
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "is"); e.Message != "question is blank" {
		t.Errorf("message = %q", e.Message)
	}
	_, err = schemaflux.Whether(ctx, isRefundMsg, isRefundAsk, then, otherwise, schemaflux.Options{MaxOutputTokens: -1})
	wantError(t, err, schemaflux.ErrConfiguration, "is")
	records := recorder.Records()
	if len(records) != 2 || records[0].Requests != 0 || records[1].Requests != 0 {
		t.Errorf("the recorder holds %+v: a refused decision is still a Record with no request", records)
	}

	var nilContext context.Context
	_, err = schemaflux.Whether(nilContext, isRefundMsg, isRefundAsk, then, otherwise)
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "whether"); e.Message != "the context is nil" {
		t.Errorf("message = %q", e.Message)
	}
	if thens.Load() != 0 || others.Load() != 0 || model.Calls() != 0 {
		t.Errorf("then ran %d times, otherwise %d, %d requests were sent", thens.Load(), others.Load(), model.Calls())
	}
}

func TestWhetherWithACancelledContextSendsNothingAndRunsNoStep(t *testing.T) {
	t.Parallel()
	then, otherwise, thens, others := whetherSteps()
	model := answeringWith(isYes)
	ctx, cancel := context.WithCancel(operationClient(model))
	cancel()
	_, err := schemaflux.Whether(ctx, isRefundMsg, isRefundAsk, then, otherwise)
	wantError(t, err, schemaflux.ErrCanceled, "is")
	if !errors.Is(err, context.Canceled) || thens.Load() != 0 || others.Load() != 0 || model.Calls() != 0 {
		t.Errorf("err %v, then ran %d times, otherwise %d, %d requests", err, thens.Load(), others.Load(), model.Calls())
	}
}

func TestWhetherTakesItsOptionsAsIsDoesAndMergesSeveralFromLeftToRight(t *testing.T) {
	t.Parallel()
	model := answeringWith(isYes)
	then, otherwise, _, _ := whetherSteps()
	_, err := schemaflux.Whether(operationClient(model), isRefundMsg, isRefundAsk, then, otherwise,
		schemaflux.Options{Model: "first", Steering: "keep this", MaxOutputTokens: 9},
		schemaflux.Options{Model: "second", WebSearch: true},
		schemaflux.Options{})
	if err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if req.Model != "second" || req.MaxTokens != 9 || !req.WebSearch {
		t.Errorf("model %q ceiling %d web %v", req.Model, req.MaxTokens, req.WebSearch)
	}
	wantText(t, req, "keep this")
}

func TestWhetherHandsTheStepTheContextItWasGivenAndTheStepsOwnCallsAreRecordedToo(t *testing.T) {
	t.Parallel()
	recorder := &schemaflux.Recorder{}
	model := answeringWith(isYes)
	step := func(ctx context.Context) (bool, error) { return schemaflux.Is(ctx, isRefundMsg, isRefundAsk) }
	got, err := schemaflux.Whether(operationClient(model, recordingTo(recorder)), isRefundMsg, isRefundAsk, step, step)
	if err != nil || !got {
		t.Fatalf("Whether = %v, %v", got, err)
	}
	records := recorder.Records()
	if len(records) != 2 || records[0].Op != "is" || records[1].Op != "is" || model.Calls() != 2 {
		t.Errorf("the recorder holds %d Records after %d requests, want the decision's and the step's own", len(records), model.Calls())
	}
}

func TestWhetherLetsAPanicInTheStepReachTheCallerWithTheDecisionAlreadyRecorded(t *testing.T) {
	t.Parallel()
	var recorder schemaflux.Recorder
	panicking := func(context.Context) (string, error) { panic("the step's own") }
	other, _, _, _ := whetherSteps()
	func() {
		defer func() {
			if recovered := recover(); recovered != "the step's own" {
				t.Errorf("recovered %v, want the step's own panic", recovered)
			}
		}()
		_, _ = schemaflux.Whether(operationClient(answeringWith(isYes), recordingTo(&recorder)), isRefundMsg, isRefundAsk, panicking, other)
	}()
	if r := onlyRecord(t, &recorder); r.Op != "is" || r.Err != nil {
		t.Errorf("record = %q err %v", r.Op, r.Err)
	}
}

func TestWhetherGoesThroughTheDecisionProviderAndTheRecordSaysWhoAnswered(t *testing.T) {
	t.Parallel()
	t.Run("the decision provider answers", func(t *testing.T) {
		t.Parallel()
		generator := answeringWith(isNo)
		decider := schemafluxtest.NewDecider(schemafluxtest.Yes(0.97))
		ctx, recorder := decidingPair(mw.DecisionOptions{}, generator, decider)
		then, otherwise, thens, others := whetherSteps()
		got, err := schemaflux.Whether(ctx, isRefundMsg, isRefundAsk, then, otherwise)
		if err != nil || got != "then" || thens.Load() != 1 || others.Load() != 0 {
			t.Fatalf("Whether = %q, %v (then %d, otherwise %d), want the decider's yes", got, err, thens.Load(), others.Load())
		}
		r := onlyRecord(t, recorder)
		if r.Op != "is" || r.AnsweredBy != "test-decider" || !r.Typed || r.AskedFirst != "" || generator.Calls() != 0 {
			t.Errorf("record = %q answered by %q typed %v asked first %q, generator asked %d times", r.Op, r.AnsweredBy, r.Typed, r.AskedFirst, generator.Calls())
		}
		if len(r.Asked) != 1 || r.Asked[0].Question != isRefundAsk || r.Asked[0].Answer != true {
			t.Errorf("asked = %+v", r.Asked)
		}
		if q := decider.Last().Decision; q == nil || len(q.Questions) != 1 || q.Questions[0].Kind != schemaflux.DecisionYesNo {
			t.Errorf("the decider was sent %+v", q)
		}
	})
	t.Run("an answer below the floor is passed over and the Record says why", func(t *testing.T) {
		t.Parallel()
		generator := answeringWith(isNo)
		decider := schemafluxtest.NewDecider(schemafluxtest.Yes(0.4))
		ctx, recorder := decidingPair(mw.DecisionOptions{Floor: 0.9}, generator, decider)
		then, otherwise, thens, others := whetherSteps()
		got, err := schemaflux.Whether(ctx, isRefundMsg, isRefundAsk, then, otherwise)
		if err != nil || got != "otherwise" || thens.Load() != 0 || others.Load() != 1 {
			t.Fatalf("Whether = %q, %v, want the model's no", got, err)
		}
		r := onlyRecord(t, recorder)
		if r.AnsweredBy != "test" || r.Typed || r.AskedFirst != "test-decider" || r.Escalated != "below the floor" || r.Requests != 2 {
			t.Errorf("record = answered by %q typed %v asked first %q escalated %q requests %d", r.AnsweredBy, r.Typed, r.AskedFirst, r.Escalated, r.Requests)
		}
	})
	t.Run("a typed answer of the wrong kind runs no step when failover is off", func(t *testing.T) {
		t.Parallel()
		ctx, _ := decidingPair(mw.DecisionOptions{NoFailover: true}, answeringWith(isYes), schemafluxtest.NewDecider(schemafluxtest.Level(3, 0.9)))
		then, otherwise, thens, others := whetherSteps()
		_, err := schemaflux.Whether(ctx, isRefundMsg, isRefundAsk, then, otherwise)
		wantError(t, err, schemaflux.ErrInvalidAnswer, "is")
		if thens.Load() != 0 || others.Load() != 0 {
			t.Errorf("then ran %d times and otherwise %d", thens.Load(), others.Load())
		}
	})
}

func TestWhetherUnderAFloorIsHeldToTheLevelOfIs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		floor   schemaflux.ContractLevel
		refused bool
	}{
		{"a floor it meets", schemaflux.ContractSchemaAndInvariantChecked, false},
		{"a floor above it", schemaflux.ContractEvidenceChecked, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			model := namedModel(isYes)
			then, otherwise, thens, others := whetherSteps()
			got, err := schemaflux.Whether(operationClient(model, underFloor(tc.floor), recordingTo(&recorder)), isRefundMsg, isRefundAsk, then, otherwise)
			if tc.refused {
				wantError(t, err, schemaflux.ErrPolicy, "is")
				if got != "" || thens.Load() != 0 || others.Load() != 0 || model.Calls() != 0 {
					t.Errorf("answer %q, then ran %d times and otherwise %d after %d requests", got, thens.Load(), others.Load(), model.Calls())
				}
			} else if err != nil || got != "then" {
				t.Fatalf("Whether = %q, %v", got, err)
			}
			if r := onlyRecord(t, &recorder); r.Op != "is" || (r.Err != nil) != tc.refused {
				t.Errorf("record = %q err %v", r.Op, r.Err)
			}
		})
	}
}

func TestWhetherUnderAnObserverIsOneOperationAndRunsTheStepOnce(t *testing.T) {
	// Not parallel: an observer is process state.
	var seen []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			seen = append(seen, r.Operation)
		}
	}})
	defer restore()
	then, otherwise, thens, others := whetherSteps()
	got, err := schemaflux.Whether(operationClient(answeringWith(isYes)), isRefundMsg, isRefundAsk, then, otherwise)
	if err != nil || got != "then" || thens.Load() != 1 || others.Load() != 0 {
		t.Fatalf("Whether = %q, %v", got, err)
	}
	if len(seen) != 1 || seen[0] != "is" {
		t.Errorf("the observer saw %v, want one is", seen)
	}
}

func TestWhetherKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-5d0e7a"
	opts := schemaflux.Options{Steering: "steer " + canary}
	subject := map[string]string{canary + "-key": "text " + canary}
	then, otherwise, _, _ := whetherSteps()
	run := func(ctx context.Context) error {
		_, err := schemaflux.Whether(ctx, subject, "Is it "+canary+"?", then, otherwise, opts)
		return err
	}
	for name, setup := range map[string]func(*schemafluxtest.Provider){
		"a call that succeeds":      func(p *schemafluxtest.Provider) { p.Reply(isYes) },
		"an answer that is refused": func(p *schemafluxtest.Provider) { p.Reply(`{"q1":"` + canary + `"}`) },
		"an answer that is text":    func(p *schemafluxtest.Provider) { p.Reply("the answer is " + canary) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			requireNoPayload(t, canary, setup, true, run)
		})
	}
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, run)
	})
}

func TestRouteByRunsTheRouteTheModelChoseOnceAndNoOther(t *testing.T) {
	t.Parallel()
	for i, want := range []string{"billing", "technical", "shipping"} {
		routes, counts := routeSteps("billing", "technical", "shipping")
		model := answeringWith(fmt.Sprintf(`{"q1":%d}`, i+1))
		got, err := schemaflux.RouteBy(operationClient(model), whichMsg, whichAsk, routes)
		if err != nil || got != want {
			t.Fatalf("RouteBy = %q, %v, want %q", got, err, want)
		}
		for label, count := range counts {
			if wantRuns := map[bool]int32{true: 1, false: 0}[label == want]; count.Load() != wantRuns {
				t.Errorf("route %q ran %d times, want %d", label, count.Load(), wantRuns)
			}
		}
		if model.Calls() != 1 {
			t.Errorf("%d requests were sent", model.Calls())
		}
	}
}

func TestRouteByAsksTheRequestWhichAsksAndAddsNothingToIt(t *testing.T) {
	t.Parallel()
	opts := schemaflux.Options{Model: "pinned", Steering: "teams only", Tier: schemaflux.TierSmart}
	asked := answeringWith(whichSecond)
	if _, err := schemaflux.Which(operationClient(asked), whichMsg, whichAsk, whichTeams(), opts); err != nil {
		t.Fatal(err)
	}
	routes, _ := routeSteps(whichTeams()...)
	routed := answeringWith(whichSecond)
	if _, err := schemaflux.RouteBy(operationClient(routed), whichMsg, whichAsk, routes, opts); err != nil {
		t.Fatal(err)
	}
	a, b := asked.Last(), routed.Last()
	if requestText(a) != requestText(b) || a.SchemaName != b.SchemaName || a.Model != b.Model || a.MaxTokens != b.MaxTokens || a.Temperature != b.Temperature || a.Effort != b.Effort || a.Format != b.Format {
		t.Error("RouteBy asked another request than Which asks")
	}
}

func TestRouteByRunsTheRouteWhoseLabelHasSpaceAroundItAsItWasWritten(t *testing.T) {
	t.Parallel()
	routes, counts := routeSteps("billing", " technical support ")
	got, err := schemaflux.RouteBy(operationClient(answeringWith(`{"q1":2}`)), whichMsg, whichAsk, routes)
	if err != nil || got != " technical support " || counts[" technical support "].Load() != 1 {
		t.Errorf("RouteBy = %q, %v", got, err)
	}
}

func TestRouteByAddsTheDecisionsRecordBeforeTheRouteRuns(t *testing.T) {
	t.Parallel()
	var order []string
	var mu sync.Mutex
	note := func(what string) { mu.Lock(); order = append(order, what); mu.Unlock() }
	var captured schemaflux.Record
	recorder := &schemaflux.Recorder{Decisions: true, Handle: func(_ context.Context, r schemaflux.Record) { note("record " + r.Op); captured = r }}
	route := func(label string) schemaflux.Route[string] {
		return schemaflux.To(label, func(context.Context) (string, error) {
			note("route " + label)
			return label, nil
		})
	}
	routes := []schemaflux.Route[string]{route("billing"), route("technical")}
	got, err := schemaflux.RouteBy(operationClient(answeringWith(`{"q1":2}`), recordingTo(recorder)), whichMsg, whichAsk, routes)
	if err != nil || got != "technical" {
		t.Fatalf("RouteBy = %q, %v", got, err)
	}
	if strings.Join(order, ",") != "record which,route technical" {
		t.Errorf("order %v: the decision's Record comes before the route", order)
	}
	r := captured
	if r.Op != "which" || r.Err != nil || len(r.Asked) != 1 || r.Asked[0].Question != whichAsk || r.Asked[0].Answer != "technical" || len(r.Asked[0].Options) != 2 {
		t.Errorf("record = %q err %v asked %+v", r.Op, r.Err, r.Asked)
	}
}

func TestRouteByHasTheDecisionsRecordInAKeepingRecorderWhenTheRouteRuns(t *testing.T) {
	t.Parallel()
	recorder := &schemaflux.Recorder{}
	var held []schemaflux.Record
	routes := []schemaflux.Route[string]{
		schemaflux.To("billing", func(context.Context) (string, error) { return "billing", nil }),
		schemaflux.To("technical", func(context.Context) (string, error) { held = recorder.Records(); return "technical", nil }),
	}
	if _, err := schemaflux.RouteBy(operationClient(answeringWith(`{"q1":2}`), recordingTo(recorder)), whichMsg, whichAsk, routes); err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0].Op != "which" || held[0].Err != nil {
		t.Errorf("the recorder held %+v when the route ran, want the decision's Record", held)
	}
}

func TestRouteByRefusesRoutesItCannotChooseBetweenBeforeAnythingIsAsked(t *testing.T) {
	t.Parallel()
	run := func(context.Context) (string, error) { return "ran", nil }
	cases := []struct {
		name    string
		routes  []schemaflux.Route[string]
		message string
	}{
		{"nil routes", nil, "there are no routes to choose between"},
		{"empty routes", []schemaflux.Route[string]{}, "there are no routes to choose between"},
		{"one route", []schemaflux.Route[string]{schemaflux.To("billing", run)}, "there have to be at least two routes to choose between"},
		{"a blank label", []schemaflux.Route[string]{schemaflux.To("billing", run), schemaflux.To("", run)}, "route 2 has no label"},
		{"a label of space", []schemaflux.Route[string]{schemaflux.To(" \t", run), schemaflux.To("billing", run)}, "route 1 has no label"},
		{"a repeated label", []schemaflux.Route[string]{schemaflux.To("a", run), schemaflux.To("b", run), schemaflux.To("a", run)}, "route 3 repeats the label of an earlier one"},
		{"a label repeated once the space is trimmed", []schemaflux.Route[string]{schemaflux.To("a", run), schemaflux.To("a ", run)}, "route 2 repeats the label of an earlier one"},
		{"a nil step", []schemaflux.Route[string]{schemaflux.To("a", run), schemaflux.To[string]("b", nil)}, "route 2 has nothing to run"},
		{"a nil step in the first route", []schemaflux.Route[string]{{Label: "a"}, schemaflux.To("b", run)}, "route 1 has nothing to run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			model := answeringWith(whichSecond)
			got, err := schemaflux.RouteBy(operationClient(model, recordingTo(&recorder)), whichMsg, whichAsk, tc.routes)
			e := wantError(t, err, schemaflux.ErrInvalidRequest, "routeby")
			if e.Message != tc.message || got != "" || model.Calls() != 0 || len(recorder.Records()) != 0 {
				t.Errorf("message %q, answer %q after %d requests with %d Records, want %q and nothing asked", e.Message, got, model.Calls(), len(recorder.Records()), tc.message)
			}
		})
	}
}

func TestRouteByRefusesTheRouteRulesBeforeABlankQuestionOrABadOption(t *testing.T) {
	t.Parallel()
	_, err := schemaflux.RouteBy(operationClient(answeringWith(whichSecond)), whichMsg, "", []schemaflux.Route[string]{}, schemaflux.Options{MaxOutputTokens: -1})
	wantError(t, err, schemaflux.ErrInvalidRequest, "routeby")
}

func TestRouteByNeverReadsAFailureAsAnyRoute(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		setup func(*schemafluxtest.Provider)
		kind  schemaflux.Kind
	}{
		"a label that is none of the routes": {func(p *schemafluxtest.Provider) { p.Reply(`{"q1":9}`) }, schemaflux.ErrInvalidAnswer},
		"an answer that is text":             {func(p *schemafluxtest.Provider) { p.Reply("billing") }, schemaflux.ErrInvalidAnswer},
		"a rate limit":                       {func(p *schemafluxtest.Provider) { p.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited}) }, schemaflux.ErrRateLimited},
		"an unavailable provider":            {func(p *schemafluxtest.Provider) { p.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable}) }, schemaflux.ErrUnavailable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			tc.setup(&model)
			var recorder schemaflux.Recorder
			routes, counts := routeSteps("billing", "technical")
			got, err := schemaflux.RouteBy(operationClient(&model, recordingTo(&recorder)), whichMsg, whichAsk, routes)
			wantError(t, err, tc.kind, "which")
			if got != "" || counts["billing"].Load() != 0 || counts["technical"].Load() != 0 {
				t.Errorf("answer %q, routes ran %d and %d times: no route runs when the question cannot be answered", got, counts["billing"].Load(), counts["technical"].Load())
			}
			if r := onlyRecord(t, &recorder); r.Op != "which" || r.Err != err {
				t.Errorf("record = %q err %v", r.Op, r.Err)
			}
		})
	}
}

func TestRouteByReturnsTheRoutesErrorAsItWasGiven(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("the route failed")
	custom := &schemaflux.Error{Kind: schemaflux.ErrBusy, Message: "the route's own"}
	for name, fail := range map[string]error{"a plain error": sentinel, "an *Error": custom} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			routes := []schemaflux.Route[string]{
				schemaflux.To("billing", func(context.Context) (string, error) { return "value", fail }),
				schemaflux.To("technical", func(context.Context) (string, error) { return "other", nil }),
			}
			got, err := schemaflux.RouteBy(operationClient(answeringWith(`{"q1":1}`), recordingTo(&recorder)), whichMsg, whichAsk, routes)
			if err != fail || got != "" {
				t.Errorf("RouteBy = %q, %v (%T), want the same error value the route returned and nothing beside it", got, err, err)
			}
			if r := onlyRecord(t, &recorder); r.Err != nil {
				t.Errorf("the decision's Record carries the route's error: %v", r.Err)
			}
		})
	}
}

func TestRouteByRefusesABlankQuestionABadOptionAndANilContext(t *testing.T) {
	t.Parallel()
	routes, counts := routeSteps("billing", "technical")
	var recorder schemaflux.Recorder
	model := answeringWith(whichSecond)
	ctx := operationClient(model, recordingTo(&recorder))
	_, err := schemaflux.RouteBy(ctx, whichMsg, " ", routes)
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "which"); e.Message != "question is blank" {
		t.Errorf("message = %q", e.Message)
	}
	_, err = schemaflux.RouteBy(ctx, whichMsg, whichAsk, routes, schemaflux.Options{Mode: 9})
	wantError(t, err, schemaflux.ErrConfiguration, "which")
	if records := recorder.Records(); len(records) != 2 || records[0].Requests != 0 || records[1].Requests != 0 {
		t.Errorf("the recorder holds %+v", records)
	}
	var nilContext context.Context
	_, err = schemaflux.RouteBy(nilContext, whichMsg, whichAsk, routes)
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "routeby"); e.Message != "the context is nil" {
		t.Errorf("message = %q", e.Message)
	}
	if counts["billing"].Load() != 0 || counts["technical"].Load() != 0 || model.Calls() != 0 {
		t.Error("a route ran or a request was sent")
	}
}

func TestRouteByWithACancelledContextSendsNothingAndRunsNoRoute(t *testing.T) {
	t.Parallel()
	routes, counts := routeSteps("billing", "technical")
	model := answeringWith(whichSecond)
	ctx, cancel := context.WithCancel(operationClient(model))
	cancel()
	_, err := schemaflux.RouteBy(ctx, whichMsg, whichAsk, routes)
	wantError(t, err, schemaflux.ErrCanceled, "which")
	if counts["billing"].Load() != 0 || counts["technical"].Load() != 0 || model.Calls() != 0 {
		t.Error("a route ran or a request was sent")
	}
}

func TestRouteByDoesNotChangeTheRoutesItIsGiven(t *testing.T) {
	t.Parallel()
	routes, _ := routeSteps("billing", " technical")
	before := []string{routes[0].Label, routes[1].Label}
	if _, err := schemaflux.RouteBy(operationClient(answeringWith(whichSecond)), whichMsg, whichAsk, routes); err != nil {
		t.Fatal(err)
	}
	if routes[0].Label != before[0] || routes[1].Label != before[1] || routes[0].Run == nil || routes[1].Run == nil {
		t.Errorf("the routes became %+v", routes)
	}
}

func TestRouteByGoesThroughTheDecisionProviderAndTheRecordSaysWhoAnswered(t *testing.T) {
	t.Parallel()
	t.Run("the decision provider chooses", func(t *testing.T) {
		t.Parallel()
		generator := answeringWith(`{"q1":1}`)
		decider := schemafluxtest.NewDecider(schemafluxtest.Chose("technical", 0.9))
		ctx, recorder := decidingPair(mw.DecisionOptions{}, generator, decider)
		routes, counts := routeSteps("billing", "technical")
		got, err := schemaflux.RouteBy(ctx, whichMsg, whichAsk, routes)
		if err != nil || got != "technical" || counts["technical"].Load() != 1 || counts["billing"].Load() != 0 {
			t.Fatalf("RouteBy = %q, %v, want the decider's choice", got, err)
		}
		r := onlyRecord(t, recorder)
		if r.Op != "which" || r.AnsweredBy != "test-decider" || !r.Typed || generator.Calls() != 0 {
			t.Errorf("record = %q answered by %q typed %v", r.Op, r.AnsweredBy, r.Typed)
		}
		if q := decider.Last().Decision.Questions[0]; q.Kind != schemaflux.DecisionChoice || len(q.Options) != 2 || q.Options[1] != "technical" {
			t.Errorf("the decider was sent %+v", q)
		}
	})
	t.Run("an unavailable decision provider is passed over", func(t *testing.T) {
		t.Parallel()
		decider := schemafluxtest.NewDecider()
		decider.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
		ctx, recorder := decidingPair(mw.DecisionOptions{}, answeringWith(`{"q1":1}`), decider)
		routes, counts := routeSteps("billing", "technical")
		got, err := schemaflux.RouteBy(ctx, whichMsg, whichAsk, routes)
		if err != nil || got != "billing" || counts["billing"].Load() != 1 {
			t.Fatalf("RouteBy = %q, %v", got, err)
		}
		if r := onlyRecord(t, recorder); r.AnsweredBy != "test" || r.AskedFirst != "test-decider" || r.Escalated != "unavailable" || r.Typed {
			t.Errorf("record = answered by %q asked first %q escalated %q typed %v", r.AnsweredBy, r.AskedFirst, r.Escalated, r.Typed)
		}
	})
	t.Run("a label the decider was not offered runs no route when failover is off", func(t *testing.T) {
		t.Parallel()
		ctx, _ := decidingPair(mw.DecisionOptions{NoFailover: true}, answeringWith(`{"q1":1}`), schemafluxtest.NewDecider(schemafluxtest.Chose("returns", 0.9)))
		routes, counts := routeSteps("billing", "technical")
		_, err := schemaflux.RouteBy(ctx, whichMsg, whichAsk, routes)
		wantError(t, err, schemaflux.ErrInvalidAnswer, "which")
		if counts["billing"].Load() != 0 || counts["technical"].Load() != 0 {
			t.Error("a route ran")
		}
	})
}

func TestRouteByUnderAFloorIsHeldToTheLevelOfWhich(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		floor   schemaflux.ContractLevel
		refused bool
	}{
		{"a floor it meets", schemaflux.ContractSchemaAndInvariantChecked, false},
		{"a floor above it", schemaflux.ContractEvidenceChecked, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			model := namedModel(whichSecond)
			routes, counts := routeSteps("billing", "technical")
			got, err := schemaflux.RouteBy(operationClient(model, underFloor(tc.floor), recordingTo(&recorder)), whichMsg, whichAsk, routes)
			if tc.refused {
				wantError(t, err, schemaflux.ErrPolicy, "which")
				if got != "" || counts["billing"].Load()+counts["technical"].Load() != 0 || model.Calls() != 0 {
					t.Errorf("answer %q after %d requests", got, model.Calls())
				}
			} else if err != nil || got != "technical" {
				t.Fatalf("RouteBy = %q, %v", got, err)
			}
			if r := onlyRecord(t, &recorder); r.Op != "which" || (r.Err != nil) != tc.refused {
				t.Errorf("record = %q err %v", r.Op, r.Err)
			}
		})
	}
}

func TestRouteByUnderAnObserverIsOneOperationAndRunsTheRouteOnce(t *testing.T) {
	// Not parallel: an observer is process state.
	var seen []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			seen = append(seen, r.Operation)
		}
	}})
	defer restore()
	routes, counts := routeSteps("billing", "technical")
	got, err := schemaflux.RouteBy(operationClient(answeringWith(whichSecond)), whichMsg, whichAsk, routes)
	if err != nil || got != "technical" || counts["technical"].Load() != 1 {
		t.Fatalf("RouteBy = %q, %v", got, err)
	}
	if len(seen) != 1 || seen[0] != "which" {
		t.Errorf("the observer saw %v, want one which", seen)
	}
}

func TestRouteByKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-91c4be"
	opts := schemaflux.Options{Steering: "steer " + canary}
	subject := map[string]string{canary + "-key": "text " + canary}
	routes, _ := routeSteps("billing-"+canary, "technical")
	run := func(ctx context.Context) error {
		_, err := schemaflux.RouteBy(ctx, subject, "Which is it "+canary+"?", routes, opts)
		return err
	}
	for name, setup := range map[string]func(*schemafluxtest.Provider){
		"a call that succeeds":      func(p *schemafluxtest.Provider) { p.Reply(`{"q1":1}`) },
		"an answer that is refused": func(p *schemafluxtest.Provider) { p.Reply(`{"q1":"` + canary + `"}`) },
		"an answer that is text":    func(p *schemafluxtest.Provider) { p.Reply("billing-" + canary) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			requireNoPayload(t, canary, setup, true, run)
		})
	}
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, run)
	})
	t.Run("a refused set of routes names no label", func(t *testing.T) {
		t.Parallel()
		repeated := []schemaflux.Route[string]{routes[0], routes[0]}
		var model schemafluxtest.Provider
		err := func() error {
			_, err := schemaflux.RouteBy(operationClient(&model), subject, "Which "+canary+"?", repeated, opts)
			return err
		}()
		for _, text := range chainText(err) {
			if strings.Contains(text, canary) {
				t.Errorf("the canary reached %q", excerpt(text))
			}
		}
	})
}

// markedAnswer is the scripted answer to a request of any of the decision
// functions, chosen by the marker its question carries, with a confidence when
// the request asks for one.
func markedAnswer(user string) string {
	var body string
	switch {
	case strings.Contains(user, "Q-IS"):
		body = `"q1":true`
	case strings.Contains(user, "Q-WHICH"):
		body = `"q1":2`
	case strings.Contains(user, "Q-NONE"):
		body = `"q1":2`
	case strings.Contains(user, "Q-RATE"):
		body = `"q1":4`
	default:
		body = `"q1":true,"q2":2,"q3":4`
	}
	if strings.Contains(user, `"confidence"`) {
		body += `,"confidence":0.8`
	}
	return "{" + body + "}"
}

// assessMarked is the struct Assess is asked about under concurrency: its
// questions carry the marker markedAnswer looks for.
type assessMarked struct {
	Spam    bool   `ask:"Q-ASSESS Is this message spam?"`
	Team    string `ask:"Q-ASSESS Which team should handle it?" oneof:"billing,technical,shipping"`
	Urgency int    `ask:"Q-ASSESS How urgent is it?" scale:"1-5"`
}

// everyDecisionCall is one call of each of the ten functions, as a step that
// returns the Record Op it must have added.
func everyDecisionCall() map[string]func(ctx context.Context) error {
	teams := func() []string { return []string{"billing", "technical", "shipping"} }
	then := func(context.Context) (int, error) { return 1, nil }
	routes := func() []schemaflux.Route[int] {
		return []schemaflux.Route[int]{schemaflux.To("billing", then), schemaflux.To("technical", then)}
	}
	return map[string]func(ctx context.Context) error{
		"which": func(ctx context.Context) error { _, err := schemaflux.Which(ctx, "m", "Q-WHICH?", teams()); return err },
		"whichdetail": func(ctx context.Context) error {
			_, err := schemaflux.WhichDetail(ctx, "m", "Q-WHICH?", teams())
			return err
		},
		"whichornone": func(ctx context.Context) error {
			_, _, err := schemaflux.WhichOrNone(ctx, "m", "Q-NONE?", teams())
			return err
		},
		"whichornonedetail": func(ctx context.Context) error {
			_, _, err := schemaflux.WhichOrNoneDetail(ctx, "m", "Q-NONE?", teams())
			return err
		},
		"rate": func(ctx context.Context) error { _, err := schemaflux.Rate(ctx, "m", "Q-RATE?", 1, 5); return err },
		"ratedetail": func(ctx context.Context) error {
			_, err := schemaflux.RateDetail(ctx, "m", "Q-RATE?", 1, 5)
			return err
		},
		"assess":       func(ctx context.Context) error { _, err := schemaflux.Assess[assessMarked](ctx, "m"); return err },
		"assessdetail": func(ctx context.Context) error { _, err := schemaflux.AssessDetail[assessMarked](ctx, "m"); return err },
		"is":           func(ctx context.Context) error { _, err := schemaflux.Is(ctx, "m", "Q-IS?"); return err },
		"isdetail":     func(ctx context.Context) error { _, err := schemaflux.IsDetail(ctx, "m", "Q-IS?"); return err },
		"is (whether)": func(ctx context.Context) error {
			_, err := schemaflux.Whether(ctx, "m", "Q-IS?", then, then)
			return err
		},
		"which (routeby)": func(ctx context.Context) error {
			_, err := schemaflux.RouteBy(ctx, "m", "Q-WHICH?", routes())
			return err
		},
	}
}

// markedModel is a fake that answers every decision function by its marker.
func markedModel() *schemafluxtest.Provider {
	var model schemafluxtest.Provider
	model.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
		return schemaflux.Response{Content: markedAnswer(req.User), Model: "test-model", FinishReason: "stop", Usage: schemaflux.Usage{Input: 5, Output: 2, Total: 7}}, nil
	})
	return &model
}

func TestRecorderUnderGatherAndEachOverTheDecisionFunctionsHoldsOneRecordPerCall(t *testing.T) {
	t.Parallel()
	calls := everyDecisionCall()
	const perFunction = 8

	t.Run("Each over one function", func(t *testing.T) {
		t.Parallel()
		for name, call := range calls {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				recorder := &schemaflux.Recorder{Decisions: true}
				model := markedModel()
				ctx := operationClient(model, recordingTo(recorder))
				items := make([]int, perFunction)
				if _, err := schemaflux.Each(ctx, items, func(ctx context.Context, _ int) (struct{}, error) { return struct{}{}, call(ctx) }); err != nil {
					t.Fatal(err)
				}
				records := recorder.Records()
				op := strings.Fields(name)[0]
				if len(records) != perFunction || model.Calls() != perFunction || recorder.Total().Requests != perFunction {
					t.Fatalf("the recorder holds %d Records after %d requests (total %d), want %d of each", len(records), model.Calls(), recorder.Total().Requests, perFunction)
				}
				for _, r := range records {
					if r.Op != op || r.Err != nil || r.Requests != 1 || len(r.Asked) == 0 {
						t.Errorf("record = %q err %v requests %d asked %d, want %q", r.Op, r.Err, r.Requests, len(r.Asked), op)
					}
				}
			})
		}
	})

	t.Run("Gather over all of them at once on one recorder", func(t *testing.T) {
		t.Parallel()
		for round := 0; round < 25; round++ {
			recorder := &schemaflux.Recorder{}
			model := markedModel()
			ctx := operationClient(model, recordingTo(recorder))
			var steps []schemaflux.Step[struct{}]
			wantOps := map[string]int{}
			for name, call := range calls {
				for i := 0; i < 3; i++ {
					steps = append(steps, func(ctx context.Context) (struct{}, error) { return struct{}{}, call(ctx) })
					wantOps[strings.Fields(name)[0]]++
				}
			}
			if _, err := schemaflux.Gather(ctx, steps...); err != nil {
				t.Fatal(err)
			}
			got := map[string]int{}
			for _, r := range recorder.Records() {
				got[r.Op]++
			}
			if len(recorder.Records()) != len(steps) || fmt.Sprint(got) != fmt.Sprint(wantOps) || recorder.Total().Requests != len(steps) {
				t.Fatalf("round %d: the recorder holds %d Records for %d calls: %v, want %v", round, len(recorder.Records()), len(steps), got, wantOps)
			}
		}
	})

	t.Run("a recorder on the context and one on the client each hold one Record per call", func(t *testing.T) {
		t.Parallel()
		onClient, onContext := &schemaflux.Recorder{}, &schemaflux.Recorder{}
		ctx := schemaflux.WithRecorder(operationClient(markedModel(), recordingTo(onClient)), onContext)
		items := make([]int, 20)
		if _, err := schemaflux.Each(ctx, items, func(ctx context.Context, _ int) (struct{}, error) {
			_, err := schemaflux.Rate(ctx, "m", "Q-RATE?", 1, 5)
			return struct{}{}, err
		}); err != nil {
			t.Fatal(err)
		}
		if len(onClient.Records()) != 20 || len(onContext.Records()) != 20 {
			t.Errorf("the recorders hold %d and %d Records, want 20 each", len(onClient.Records()), len(onContext.Records()))
		}
	})
}

func TestAPanickingStepInsideWhetherAndRouteByUnderGatherAndEachIsTheInternalKind(t *testing.T) {
	t.Parallel()
	const secret = "PANIC-VALUE-4471"
	panicking := func(context.Context) (string, error) { panic(secret) }
	fine := func(context.Context) (string, error) { return "fine", nil }
	whether := func(ctx context.Context) (string, error) {
		return schemaflux.Whether(ctx, "m", "Q-IS?", panicking, fine)
	}
	routeBy := func(ctx context.Context) (string, error) {
		return schemaflux.RouteBy(ctx, "m", "Q-WHICH?", []schemaflux.Route[string]{schemaflux.To("billing", fine), schemaflux.To("technical", panicking)})
	}
	for name, step := range map[string]schemaflux.Step[string]{"Whether": whether, "RouteBy": routeBy} {
		t.Run(name+" under Gather", func(t *testing.T) {
			t.Parallel()
			recorder := &schemaflux.Recorder{}
			_, err := schemaflux.Gather(operationClient(markedModel(), recordingTo(recorder)), step, fine)
			e := wantError(t, err, schemaflux.ErrInternal, "gather")
			for _, text := range chainText(err) {
				if strings.Contains(text, secret) {
					t.Errorf("the panic's value reached %q", text)
				}
			}
			if e.Message == "" || len(recorder.Records()) != 1 {
				t.Errorf("message %q, %d Records: the decision was made before the step panicked", e.Message, len(recorder.Records()))
			}
		})
		t.Run(name+" under Each", func(t *testing.T) {
			t.Parallel()
			recorder := &schemaflux.Recorder{}
			_, err := schemaflux.Each(operationClient(markedModel(), recordingTo(recorder)), []int{1, 2, 3}, func(ctx context.Context, _ int) (string, error) { return step(ctx) })
			wantError(t, err, schemaflux.ErrInternal, "each")
			for _, text := range chainText(err) {
				if strings.Contains(text, secret) {
					t.Errorf("the panic's value reached %q", text)
				}
			}
		})
	}
}

func TestWhetherAndRouteByUnderGatherAndEachReturnTheStepsErrorUnchanged(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("the step failed")
	failing := func(context.Context) (string, error) { return "", sentinel }
	fine := func(context.Context) (string, error) { return "fine", nil }
	_, err := schemaflux.Each(operationClient(markedModel()), []int{1, 2, 3, 4}, func(ctx context.Context, _ int) (string, error) {
		return schemaflux.Whether(ctx, "m", "Q-IS?", failing, fine)
	})
	if err != sentinel {
		t.Errorf("Each returned %v (%T), want the step's own error value", err, err)
	}
	_, err = schemaflux.Gather(operationClient(markedModel()),
		func(ctx context.Context) (string, error) {
			return schemaflux.RouteBy(ctx, "m", "Q-WHICH?", []schemaflux.Route[string]{schemaflux.To("billing", fine), schemaflux.To("technical", failing)})
		})
	if err != sentinel {
		t.Errorf("Gather returned %v (%T), want the route's own error value", err, err)
	}
}
