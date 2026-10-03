package schemaflux

import (
	"context"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/ops"
)

// The allocation tests of the operation functions (extract_alloc_test.go and the
// files beside it) hold the rule of C3: a call in the PLAIN state (no recorder,
// no observer, no floor) allocates what the internal call it wraps allocates and
// nothing more, and on the default client one more, which is the pin of the
// client the call runs on (R3). They are not parallel: AllocsPerRun counts every
// allocation of the process, so a test running beside them would be counted.

// allocProvider is the scripted model of the allocation tests. It answers every
// request with one body and keeps nothing: a list of the requests it was sent
// would grow, and each growth is an allocation of the fixture's own.
type allocProvider struct{ reply string }

func (p *allocProvider) Name() string { return "alloc-test" }

func (p *allocProvider) Complete(context.Context, Request) (Response, error) {
	return Response{
		Content:      p.reply,
		Model:        "alloc-model",
		FinishReason: "stop",
		Usage:        Usage{Input: 11, Output: 3, Total: 14},
	}, nil
}

// ModelFor gives every tier one model, so that a call needs no setting to be made.
func (p *allocProvider) ModelFor(Tier) string { return "alloc-model" }

// RetryPolicy is never: the failure of a request is seen once.
func (p *allocProvider) RetryPolicy() (int, time.Duration) { return -1, 0 }

// allocCase is one call of an operation function, in the plain state, beside the
// internal call it wraps.
type allocCase struct {
	// reply is the body the scripted model answers every request with.
	reply string

	// call is the v2 function, called once and returning its error. internal is
	// the internal call the function wraps, with its internal options built as
	// the function builds them (the xInternal function of its file), returning its
	// error. Both are given the context that carries the client.
	call     func(ctx context.Context) error
	internal func(ctx context.Context) error
}

// allocRuns is how many calls a measurement averages over.
const allocRuns = 50

// requireAllocsLikeTheInternalCall measures the function and the internal call
// on a client carried by the context, where the two must allocate the same, and
// on the default client, where the function must allocate one more than the
// internal call does on a context that already carries the default client's
// snapshot: the pin of the client the call runs on. The internal call on a
// context with no snapshot pins it itself, so it is measured too and has to
// agree with the function.
//
// The two clients are not compared with each other: a client carried by a
// context and the default client are built by different code and do not cost the
// same, and the rule is about what the function adds to the internal call.
func requireAllocsLikeTheInternalCall(t *testing.T, c allocCase) {
	t.Helper()
	model := &allocProvider{reply: c.reply}

	client := WithClient(context.Background(), Client{Provider: model})
	if err := c.call(client); err != nil {
		t.Fatalf("the function failed on the scripted model: %v", err)
	}
	if err := c.internal(client); err != nil {
		t.Fatalf("the internal call failed on the scripted model: %v", err)
	}
	wrapped := testing.AllocsPerRun(allocRuns, func() { _ = c.call(client) })
	direct := testing.AllocsPerRun(allocRuns, func() { _ = c.internal(client) })
	t.Logf("under WithClient: the function %v allocations, the internal call %v", wrapped, direct)
	if wrapped != direct {
		t.Errorf("under a client the function allocates %v times and the internal call it wraps %v", wrapped, direct)
	}

	previous := Default()
	SetDefault(Client{Provider: model})
	t.Cleanup(func() { SetDefault(previous) })
	background := context.Background()
	pinned, _ := ops.PinExec(background)
	wrappedDefault := testing.AllocsPerRun(allocRuns, func() { _ = c.call(background) })
	directPinned := testing.AllocsPerRun(allocRuns, func() { _ = c.internal(pinned) })
	directDefault := testing.AllocsPerRun(allocRuns, func() { _ = c.internal(background) })
	t.Logf("on the default client: the function %v allocations, the internal call %v on a pinned context and %v on none", wrappedDefault, directPinned, directDefault)
	if wrappedDefault != directPinned+1 {
		t.Errorf("on the default client the function allocates %v times, want the internal call's %v plus the pin", wrappedDefault, directPinned)
	}
	if wrappedDefault != directDefault {
		t.Errorf("on the default client the function allocates %v times and the internal call %v: the pin is paid once by either, so they are equal", wrappedDefault, directDefault)
	}
}
