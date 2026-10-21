package schemaflux_test

import (
	"context"
	"errors"
	"fmt"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// A decision is made once, and the program that follows it is plain Go. Here the
// earlier step is stood in for by a struct holding what an operation such as
// Assess would have returned.
func ExampleBranch() {
	ctx := context.Background()

	triage := struct{ Spam bool }{Spam: true}

	discard := func(context.Context) (string, error) { return "discarded", nil }
	handle := func(context.Context) (string, error) { return "handled", nil }

	reply, err := schemaflux.Branch(ctx, triage.Spam, discard, handle)
	fmt.Println(reply, err)
	// Output: discarded <nil>
}

// Select runs the route whose label was decided earlier. A label that no route
// has is an error, and nothing runs.
func ExampleSelect() {
	ctx := context.Background()

	triage := struct{ Team string }{Team: "technical"}

	reply, err := schemaflux.Select(ctx, triage.Team,
		schemaflux.To("billing", func(context.Context) (string, error) { return "to billing", nil }),
		schemaflux.To("technical", func(context.Context) (string, error) { return "to technical", nil }))
	fmt.Println(reply, err)

	_, err = schemaflux.Select(ctx, "returns",
		schemaflux.To("billing", func(context.Context) (string, error) { return "to billing", nil }))
	fmt.Println(err, errors.Is(err, schemaflux.ErrInvalidRequest))
	// Output:
	// to technical <nil>
	// select: invalid request: the label is not the label of any of the 1 routes true
}

// A decision, a branch and three steps at once: the decision picks the branch, and
// the branch that handles the message runs its three steps together and waits as
// long as the slowest of them.
func ExampleGather() {
	ctx := context.Background()

	message := "Where is my refund for order 4471?"
	urgent := strings.Contains(message, "refund") // what a decision would have said

	summarize := func(context.Context) (string, error) { return "asks about a refund", nil }
	translate := func(context.Context) (string, error) { return "¿Dónde está mi reembolso?", nil }
	tag := func(context.Context) (string, error) { return "billing", nil }

	handle := func(ctx context.Context) (string, error) {
		parts, err := schemaflux.Gather(ctx, summarize, translate, tag)
		if err != nil {
			return "", err
		}
		return strings.Join(parts, " | "), nil
	}
	ignore := func(context.Context) (string, error) { return "ignored", nil }

	reply, err := schemaflux.Branch(ctx, urgent, handle, ignore)
	fmt.Println(reply, err)
	// Output: asks about a refund | ¿Dónde está mi reembolso? | billing <nil>
}

// Gather2 is Gather for steps whose results are of different types.
func ExampleGather2() {
	ctx := context.Background()

	summary, count, err := schemaflux.Gather2(ctx,
		func(context.Context) (string, error) { return "a short summary", nil },
		func(context.Context) (int, error) { return 3, nil })
	fmt.Println(summary, count, err)
	// Output: a short summary 3 <nil>
}

// The first step to fail ends the call: the others are cancelled, and the error
// is the one the step returned.
func ExampleGather_failure() {
	ctx := context.Background()
	errDown := errors.New("the service is down")

	_, err := schemaflux.Gather(ctx,
		func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() },
		func(context.Context) (string, error) { return "", errDown })
	fmt.Println(err == errDown)
	// Output: true
}

// Each runs a step for every item, up to 64 at once, and the results come back in
// the order of the items. One item failing stops the rest, and its error is the
// one returned.
func ExampleEach() {
	ctx := context.Background()
	errBad := errors.New("cannot read ticket 3")

	lengths, err := schemaflux.Each(ctx, []string{"printer", "login", "refund"}, func(_ context.Context, ticket string) (int, error) {
		return len(ticket), nil
	})
	fmt.Println(lengths, err)

	_, err = schemaflux.Each(ctx, []int{1, 2, 3, 4}, func(_ context.Context, id int) (string, error) {
		if id == 3 {
			return "", errBad
		}
		return fmt.Sprint("ticket ", id), nil
	})
	fmt.Println(err == errBad)
	// Output:
	// [7 5 6] <nil>
	// true
}

// To work through every item whatever fails, the step returns its failure inside
// its result and a nil error.
func ExampleEach_keepGoing() {
	ctx := context.Background()

	type outcome struct {
		id  int
		err error
	}
	outcomes, _ := schemaflux.Each(ctx, []int{1, 2, 3}, func(_ context.Context, id int) (outcome, error) {
		if id == 2 {
			return outcome{id: id, err: errors.New("unreadable")}, nil
		}
		return outcome{id: id}, nil
	})
	for _, o := range outcomes {
		fmt.Println(o.id, o.err)
	}
	// Output:
	// 1 <nil>
	// 2 unreadable
	// 3 <nil>
}
