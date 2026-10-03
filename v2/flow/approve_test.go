package flow_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"

	"github.com/monstercameron/schemaflux/internal/types"
)

func approves(context.Context, invoice) (flow.ApprovalOutcome, error) {
	return flow.ApprovalOutcome{Approved: true}, nil
}

func declines(evidence, failed []string, action string) flow.ApprovalGate[invoice] {
	return func(context.Context, invoice) (flow.ApprovalOutcome, error) {
		return flow.ApprovalOutcome{Evidence: evidence, FailedChecks: failed, SuggestedAction: action}, nil
	}
}

func TestApproveReturnsTheValueWhenTheGateApproves(t *testing.T) {
	var runs atomic.Int32
	got, err := flow.Approve(context.Background(), produceInvoice(&runs, "A-17", 40), approves, []string{"invoice 4471"})
	if err != nil || got != (invoice{"A-17", 40}) || runs.Load() != 1 {
		t.Errorf("Approve = %+v, %v after %d runs", got, err, runs.Load())
	}
}

func TestApproveReturnsAReviewRequestHoldingTheCandidateWhenTheGateDeclines(t *testing.T) {
	var runs atomic.Int32
	refs := []string{"invoice 4471", "3 pages"}
	gate := declines([]string{"total checked against the lines"}, []string{"total over the limit"}, "confirm the total")
	got, err := flow.Approve(context.Background(), produceInvoice(&runs, "A-17", 40_000), gate, refs)
	e := wantKind(t, err, schemaflux.ErrReviewRequired)
	if got != (invoice{}) || runs.Load() != 1 || e.Op != "flow.approve" {
		t.Errorf("got = %+v, runs = %d, Op = %q; a declined candidate is not returned as the value and the step is not run again", got, runs.Load(), e.Op)
	}
	var review *flow.ReviewError[invoice]
	if !errors.As(err, &review) {
		t.Fatalf("errors.As to *flow.ReviewError[invoice] fails for %v", err)
	}
	want := flow.Review[invoice]{
		Candidate:       invoice{"A-17", 40_000},
		InputRefs:       refs,
		Evidence:        []string{"total checked against the lines"},
		FailedChecks:    []string{"total over the limit"},
		SuggestedAction: "confirm the total",
	}
	if !reflect.DeepEqual(review.Review, want) {
		t.Errorf("review = %+v, want %+v", review.Review, want)
	}
	if review.Review.Attempts != 0 {
		t.Errorf("Attempts = %d; Approve runs its step once and cannot see what it did inside", review.Review.Attempts)
	}
}

func TestApproveTurnsAStepsExhaustedRecoveryIntoAReviewWithNoCandidate(t *testing.T) {
	cases := []struct {
		name     string
		internal types.ErrorKind
		reason   string
	}{
		{"repair exhausted", types.KindRepairExhausted, "the step failed with repair exhausted"},
		{"an invariant violation", types.KindInvariantViolation, "the step failed with an invariant violation"},
		{"an evidence violation", types.KindEvidenceViolation, "the step failed with an evidence violation"},
		{"a schema violation", types.KindSchemaViolation, "the step failed with a schema violation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			failure := &schemaflux.Error{Kind: schemaflux.ErrInvalidAnswer, Op: "extract", Err: types.Newf(tc.internal, "extract", "the answer failed")}
			var gateRuns atomic.Int32
			gate := func(context.Context, invoice) (flow.ApprovalOutcome, error) {
				gateRuns.Add(1)
				return flow.ApprovalOutcome{Approved: true}, nil
			}
			got, err := flow.Approve(context.Background(), fails[invoice](failure), gate, []string{"ref"})
			wantKind(t, err, schemaflux.ErrReviewRequired)
			var review *flow.ReviewError[invoice]
			if !errors.As(err, &review) {
				t.Fatalf("no review in %v", err)
			}
			r := review.Review
			if got != (invoice{}) || gateRuns.Load() != 0 || r.Candidate != (invoice{}) || !reflect.DeepEqual(r.FailedChecks, []string{tc.reason}) || !reflect.DeepEqual(r.InputRefs, []string{"ref"}) || r.SuggestedAction == "" {
				t.Errorf("got = %+v, gate runs = %d, review = %+v", got, gateRuns.Load(), r)
			}
		})
	}
}

func TestApproveReturnsAnyOtherFailureOfTheStepAsItWasGivenAndDoesNotAskTheGate(t *testing.T) {
	failures := []error{
		errStep,
		fmt.Errorf("wrapped: %w", errStep),
		&schemaflux.Error{Kind: schemaflux.ErrConfiguration},
		&schemaflux.Error{Kind: schemaflux.ErrUnauthorized},
		&schemaflux.Error{Kind: schemaflux.ErrBudgetExceeded},
		&schemaflux.Error{Kind: schemaflux.ErrInvalidAnswer},
		&schemaflux.Error{Kind: schemaflux.ErrInvalidAnswer, Err: types.Newf(types.KindMalformedOutput, "extract", "not JSON")},
		&schemaflux.Error{Kind: schemaflux.ErrRateLimited},
		context.Canceled,
	}
	for _, failure := range failures {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			var gateRuns atomic.Int32
			gate := func(context.Context, invoice) (flow.ApprovalOutcome, error) {
				gateRuns.Add(1)
				return flow.ApprovalOutcome{Approved: true}, nil
			}
			got, err := flow.Approve(context.Background(), fails[invoice](failure), gate, nil)
			if err != failure || got != (invoice{}) || gateRuns.Load() != 0 {
				t.Errorf("Approve = %+v, %v; gate ran %d times; want the step's error as it was given", got, err, gateRuns.Load())
			}
		})
	}
}

func TestApproveReturnsTheGatesErrorAsItWasGivenAndDoesNotCallItADecline(t *testing.T) {
	typed := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Op: "policy"}
	for _, failure := range []error{errStep, fmt.Errorf("the policy service: %w", errStep), typed} {
		var runs atomic.Int32
		gate := func(context.Context, invoice) (flow.ApprovalOutcome, error) {
			return flow.ApprovalOutcome{Approved: true}, failure
		}
		got, err := flow.Approve(context.Background(), produceInvoice(&runs, "A", 1), gate, nil)
		if err != failure || got != (invoice{}) {
			t.Errorf("Approve = %+v, %v; want the gate's error as it was given and no review", got, err)
		}
	}
}

func TestApproveHandsTheGateTheCandidateAndTheCallersContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), contextKey{}, "mine")
	var sawCandidate invoice
	var sawContext string
	gate := func(ctx context.Context, candidate invoice) (flow.ApprovalOutcome, error) {
		sawCandidate, sawContext = candidate, fmt.Sprint(ctx.Value(contextKey{}))
		return flow.ApprovalOutcome{Approved: true}, nil
	}
	var runs atomic.Int32
	if _, err := flow.Approve(ctx, produceInvoice(&runs, "A-17", 40), gate, nil); err != nil {
		t.Fatal(err)
	}
	if sawCandidate != (invoice{"A-17", 40}) || sawContext != "mine" {
		t.Errorf("the gate saw %+v and context %q", sawCandidate, sawContext)
	}
}

func TestApproveIgnoresWhatAGateSaysAboutEvidenceWhenItApproves(t *testing.T) {
	var runs atomic.Int32
	gate := func(context.Context, invoice) (flow.ApprovalOutcome, error) {
		return flow.ApprovalOutcome{Approved: true, FailedChecks: []string{"ignored"}}, nil
	}
	got, err := flow.Approve(context.Background(), produceInvoice(&runs, "A", 1), gate, nil)
	if err != nil || got.Number != "A" {
		t.Errorf("Approve = %+v, %v", got, err)
	}
}

func TestApproveRefusesAStepOrGateItCannotUseAndRunsNothing(t *testing.T) {
	var runs atomic.Int32
	cases := []struct {
		name string
		step schemaflux.Step[invoice]
		gate flow.ApprovalGate[invoice]
	}{
		{"no step", nil, approves},
		{"no gate", produceInvoice(&runs, "A", 1), nil},
		{"neither", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := flow.Approve(context.Background(), tc.step, tc.gate, nil)
			e := wantKind(t, err, schemaflux.ErrInvalidRequest)
			if e.Op != "flow.approve" || got != (invoice{}) || runs.Load() != 0 {
				t.Errorf("Op = %q, got = %+v, runs = %d", e.Op, got, runs.Load())
			}
		})
	}
}

func TestApproveRefusesANilContextAndAContextThatIsOverAndRunsNothing(t *testing.T) {
	var runs atomic.Int32
	var nilContext context.Context
	_, err := flow.Approve(nilContext, produceInvoice(&runs, "A", 1), approves, nil)
	wantKind(t, err, schemaflux.ErrInvalidRequest)
	for _, done := range doneContexts() {
		got, err := flow.Approve(done.ctx, produceInvoice(&runs, "A", 1), approves, nil)
		wantKind(t, err, done.kind)
		if !errors.Is(err, done.is) || got != (invoice{}) {
			t.Errorf("%s: err = %v, got = %+v", done.name, err, got)
		}
	}
	if runs.Load() != 0 {
		t.Errorf("%d steps ran", runs.Load())
	}
}

func TestApproveDoesNotRetryAfterADecline(t *testing.T) {
	var runs, gateRuns atomic.Int32
	gate := func(context.Context, invoice) (flow.ApprovalOutcome, error) {
		gateRuns.Add(1)
		return flow.ApprovalOutcome{FailedChecks: []string{"no"}}, nil
	}
	_, err := flow.Approve(context.Background(), produceInvoice(&runs, "A", 1), gate, nil)
	wantKind(t, err, schemaflux.ErrReviewRequired)
	if runs.Load() != 1 || gateRuns.Load() != 1 {
		t.Errorf("step ran %d times and gate %d; rejection is not retried here", runs.Load(), gateRuns.Load())
	}
}

func TestApproveReviewsAStepThatRanOutOfAttemptsInsideUntil(t *testing.T) {
	step := func(ctx context.Context) (invoice, error) {
		got, _, err := flow.Until(ctx, returns(invoice{"A", 1}), func(invoice) bool { return false }, 2)
		return got, err
	}
	var gateRuns atomic.Int32
	gate := func(context.Context, invoice) (flow.ApprovalOutcome, error) {
		gateRuns.Add(1)
		return flow.ApprovalOutcome{Approved: true}, nil
	}
	_, err := flow.Approve(context.Background(), step, gate, []string{"invoice"})
	wantKind(t, err, schemaflux.ErrReviewRequired)
	var review *flow.ReviewError[invoice]
	if !errors.As(err, &review) || gateRuns.Load() != 0 || !reflect.DeepEqual(review.Review.FailedChecks, []string{"the step failed with repair exhausted"}) {
		t.Errorf("err = %v, review = %+v, gate runs = %d; the exhausted attempts of Until become a review", err, review, gateRuns.Load())
	}
}

func TestApproveReturnsAReviewRequestThatAStepRaisedAsItWasGiven(t *testing.T) {
	step, _ := sequence("a", "b")
	voted := func(ctx context.Context) (string, error) {
		got, _, err := flow.Vote(ctx, 2, step, flow.ExactAgreement[string](2), flow.VoteOptions{Concurrency: 1})
		return got, err
	}
	gate := func(context.Context, string) (flow.ApprovalOutcome, error) {
		return flow.ApprovalOutcome{Approved: true}, nil
	}
	_, err := flow.Approve(context.Background(), voted, gate, nil)
	e := wantKind(t, err, schemaflux.ErrReviewRequired)
	if e.Op != "flow.vote" {
		t.Errorf("Op = %q; a review the step already raised is not packaged again by Approve", e.Op)
	}
}

func TestApproveRunsTheStepAndTheGateOnTheCallersGoroutineSoAPanicReachesTheCaller(t *testing.T) {
	for _, name := range []string{"step", "gate"} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != "from the "+name {
					t.Errorf("recovered %v", recovered)
				}
			}()
			step, gate := returns(invoice{}), flow.ApprovalGate[invoice](approves)
			if name == "step" {
				step = panics[invoice]("from the step")
			} else {
				gate = func(context.Context, invoice) (flow.ApprovalOutcome, error) { panic("from the gate") }
			}
			_, _ = flow.Approve(context.Background(), step, gate, nil)
			t.Error("Approve returned after a panic")
		})
	}
}

func TestApproveNestsAndKeepsTheInnermostErrorAsItWasGiven(t *testing.T) {
	inner := func(ctx context.Context) (invoice, error) {
		return flow.Approve(ctx, fails[invoice](errStep), approves, nil)
	}
	_, err := flow.Approve(context.Background(), inner, approves, nil)
	if err != errStep {
		t.Errorf("err = %v, want the innermost error as it was given", err)
	}
}

func TestReviewErrorsMessageNamesTheFailedChecksAndNothingOfTheCandidateOrTheInputs(t *testing.T) {
	const canary = "candidate-4471-canary"
	cases := []struct {
		name   string
		review flow.Review[string]
		want   string
	}{
		{"no failed checks", flow.Review[string]{Candidate: canary}, "review required"},
		{"one failed check", flow.Review[string]{Candidate: canary, FailedChecks: []string{"total over the limit"}}, "review required: total over the limit"},
		{"several", flow.Review[string]{Candidate: canary, FailedChecks: []string{"a", "b", "c"}}, "review required: a, b, c"},
		{"input references", flow.Review[string]{Candidate: canary, InputRefs: []string{canary}, Evidence: []string{"e"}}, "review required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &flow.ReviewError[string]{Review: tc.review}
			if got := e.Error(); got != tc.want || strings.Contains(got, canary) {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
			if !errors.Is(e, schemaflux.ErrReviewRequired) || errors.Is(e, schemaflux.ErrInternal) {
				t.Errorf("errors.Is: review required %v, internal %v", errors.Is(e, schemaflux.ErrReviewRequired), errors.Is(e, schemaflux.ErrInternal))
			}
		})
	}
	var none *flow.ReviewError[string]
	if got := none.Error(); got != "review required" {
		t.Errorf("a nil *ReviewError prints %q", got)
	}
}

func TestReviewRequestErrorCarriesNoPayloadThroughLoggingOrJSON(t *testing.T) {
	const canary = "candidate-4471-canary"
	gate := func(context.Context, string) (flow.ApprovalOutcome, error) {
		return flow.ApprovalOutcome{FailedChecks: []string{"rule 7"}}, nil
	}
	_, err := flow.Approve(context.Background(), returns(canary), gate, []string{canary + "-ref"})
	wantKind(t, err, schemaflux.ErrReviewRequired)
	var e *schemaflux.Error
	errors.As(err, &e)
	encoded, jsonErr := e.MarshalJSON()
	if jsonErr != nil {
		t.Fatal(jsonErr)
	}
	for _, text := range []string{err.Error(), string(encoded), fmt.Sprint(e.LogValue()), fmt.Sprintf("%+v", err)} {
		if strings.Contains(text, canary) {
			t.Errorf("the candidate is in %q", text)
		}
	}
}
