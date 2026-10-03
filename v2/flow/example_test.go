package flow_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"
)

// Escalate tries the cheap route first and the strong one only when it has to: the
// first answer failed, or the caller's own test turned it down.
func ExampleEscalate() {
	ctx := context.Background()

	cheap := func(context.Context) (string, error) { return "", nil }
	strong := func(context.Context) (string, error) { return "A-17", nil }

	number, record, err := flow.Escalate(ctx, cheap, strong, func(number string) bool { return number != "" })
	fmt.Println(number, record.Escalated, record.Reason, err)
	// Output: A-17 true rejected <nil>
}

// Until repeats a step until its answer satisfies the condition. Running out of
// attempts is an error, and the answer that failed the condition is not returned.
func ExampleUntil() {
	ctx := context.Background()

	var words atomic.Int32
	shorter := func(context.Context) (int, error) { return 400 - 100*int(words.Add(1)), nil }

	draft, attempts, err := flow.Until(ctx, shorter, func(words int) bool { return words <= 200 }, 5)
	fmt.Println(draft, attempts, err)

	_, attempts, err = flow.Until(ctx, shorter, func(words int) bool { return words > 1000 }, 2)
	fmt.Println(attempts, errors.Is(err, schemaflux.ErrInvalidAnswer))
	// Output:
	// 200 2 <nil>
	// 2 true
}

// Vote samples a step several times and lets a reconciler of the caller's choice
// decide between the answers. When the reconciler abstains the result is a review
// request that holds the candidate, and the answer itself is not returned.
func ExampleVote() {
	ctx := context.Background()

	var n atomic.Int32
	answers := []string{"A-17", "A-17", "A-71"}
	sample := func(context.Context) (string, error) { return answers[n.Add(1)-1], nil }

	number, votes, err := flow.Vote(ctx, 3, sample, flow.ExactAgreement[string](2), flow.VoteOptions{Concurrency: 1})
	fmt.Println(number, votes.Agree, votes.Succeeded, err)

	n.Store(0)
	_, _, err = flow.Vote(ctx, 3, sample, flow.ExactAgreement[string](3), flow.VoteOptions{Concurrency: 1})
	var review *flow.ReviewError[string]
	if errors.As(err, &review) {
		fmt.Println(errors.Is(err, schemaflux.ErrReviewRequired), review.Review.Candidate, review.Review.FailedChecks)
	}
	// Output:
	// A-17 2 3 <nil>
	// true A-17 [agreement below minimum]
}

// Checkpoint keeps a step's result under a run and a step name, together with a
// fingerprint of the input, so that a run that stalled resumes without repeating
// the step and a run id reused for other input does not return the old answer.
func ExampleCheckpoint() {
	ctx := context.Background()
	var store flow.MemoryStore

	var runs atomic.Int32
	extract := func(context.Context) (string, error) {
		runs.Add(1)
		return "A-17", nil
	}

	for _, text := range []string{"invoice one", "invoice one", "invoice two"} {
		number, outcome, err := flow.Checkpoint(ctx, &store, "run-7", "extract", text, extract)
		fmt.Println(number, outcome.Resumed, outcome.InputChanged, err)
	}
	fmt.Println("the step ran", runs.Load(), "times")
	// Output:
	// A-17 false false <nil>
	// A-17 true false <nil>
	// A-17 false true <nil>
	// the step ran 2 times
}

// Approve lets a gate decide whether an answer may be used. A gate that declines
// stops the call with a review request, which holds the candidate for whoever
// handles it.
func ExampleApprove() {
	ctx := context.Background()

	extract := func(context.Context) (int, error) { return 40_000, nil }
	limit := func(_ context.Context, total int) (flow.ApprovalOutcome, error) {
		if total > 10_000 {
			return flow.ApprovalOutcome{FailedChecks: []string{"total over the limit"}, SuggestedAction: "confirm the total"}, nil
		}
		return flow.ApprovalOutcome{Approved: true}, nil
	}

	_, err := flow.Approve(ctx, extract, limit, []string{"invoice 4471"})
	var review *flow.ReviewError[int]
	if errors.As(err, &review) {
		fmt.Println(err)
		fmt.Println(review.Review.Candidate, review.Review.InputRefs, review.Review.SuggestedAction)
	}
	// Output:
	// flow.approve: review required
	// 40000 [invoice 4471] confirm the total
}

// MapReduce runs an operation over chunks of a list, a few at a time, and returns
// the per-chunk results in the order of the list.
func ExampleMapReduce() {
	ctx := context.Background()

	words := strings.Fields("one two three four five six seven")
	joined, run, err := flow.MapReduce(ctx, words, func(_ context.Context, chunk []string) (string, error) {
		return strings.Join(chunk, "+"), nil
	}, flow.MapReduceOptions{ChunkSize: 3})
	fmt.Println(joined, run.Chunks, run.Complete(), err)
	// Output: [one+two+three four+five+six seven] 3 true <nil>
}

// With ContinueOnError the results hold the chunks that succeeded and the run says
// which did not, by index and by the offset of the chunk's first item.
func ExampleMapReduce_continueOnError() {
	ctx := context.Background()
	errBad := errors.New("unreadable")

	got, run, err := flow.MapReduce(ctx, []int{1, 2, 3, 4, 5, 6}, func(_ context.Context, chunk []int) (int, error) {
		if chunk[0] == 3 {
			return 0, errBad
		}
		return chunk[0], nil
	}, flow.MapReduceOptions{ChunkSize: 2, Concurrency: 1, ContinueOnError: true})
	fmt.Println(got, err)
	for _, failed := range run.Failed {
		fmt.Println("chunk", failed.Index, "from item", failed.Offset, failed.Err == errBad)
	}
	// Output:
	// [1 5] <nil>
	// chunk 1 from item 2 true
}

// Guard runs every check and lists the ones that failed. It asks no model.
func ExampleGuard() {
	type order struct {
		Total   int
		Address string
	}
	verdict := flow.Guard(order{Total: 0, Address: ""},
		func(o order) (bool, string) { return o.Total > 0, "the total is not positive" },
		func(o order) (bool, string) { return o.Address != "", "there is no address" })
	fmt.Println(verdict.CanProceed, verdict.FailedChecks)
	// Output: false [the total is not positive there is no address]
}

// BranchAhead starts the likely branch beside the decision, so that a program that
// decides and then acts waits once and not twice.
func ExampleBranchAhead() {
	ctx := context.Background()

	isSpam := func(context.Context) (bool, error) { return true, nil }
	discard := func(context.Context) (string, error) { return "discarded", nil }
	handle := func(context.Context) (string, error) { return "handled", nil }

	reply, err := flow.BranchAhead(ctx, isSpam, discard, handle)
	fmt.Println(reply, err)
	// Output: discarded <nil>
}

// BranchBoth starts both branches and keeps the one the decision picks. Use it for
// branches that only read or compute: the one that is not kept may still finish what
// it was doing.
func ExampleBranchBoth() {
	ctx := context.Background()

	isSpam := func(context.Context) (bool, error) { return false, nil }
	discard := func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() }
	handle := func(context.Context) (string, error) { return "handled", nil }

	reply, err := flow.BranchBoth(ctx, isSpam, discard, handle)
	fmt.Println(reply, err)
	// Output: handled <nil>
}

// RouteAhead starts the route that is likely beside the decision of which route to
// take.
func ExampleRouteAhead() {
	ctx := context.Background()

	which := func(context.Context) (string, error) { return "technical", nil }

	reply, err := flow.RouteAhead(ctx, which, "billing",
		schemaflux.To("billing", func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() }),
		schemaflux.To("technical", func(context.Context) (string, error) { return "to technical", nil }))
	fmt.Println(reply, err)
	// Output: to technical <nil>
}

// When and Otherwise build the cases of a Match.
func ExampleWhen() {
	var handled string
	cases := []flow.Case{
		flow.When("a billing question", func() { handled = "billing" }),
		flow.Otherwise(func() { handled = "general" }),
	}
	fmt.Println(len(cases), cases[0].Condition, cases[1].Condition, handled == "")
	// Output: 2 a billing question otherwise true
}
