package op

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
)

// The allocation tests hold the rule of C3 for the runners: a call in the PLAIN
// state (no recorder, no observer, no floor) allocates what the internal call it
// wraps allocates, with the descriptor lowered as the function lowers it, and
// nothing more; on the default client one more, which is the pin of the client the
// call runs on. They are not parallel: AllocsPerRun counts every allocation of the
// process.

// allocProvider is the scripted model of these tests. It answers every request
// with one body and keeps nothing.
type allocProvider struct{ reply string }

func (p *allocProvider) Name() string { return "alloc-test" }

func (p *allocProvider) Complete(context.Context, schemaflux.Request) (schemaflux.Response, error) {
	return schemaflux.Response{
		Content: p.reply, Model: "alloc-model", FinishReason: "stop",
		Usage: schemaflux.Usage{Input: 11, Output: 3, Total: 14},
	}, nil
}

func (p *allocProvider) ModelFor(schemaflux.Tier) string { return "alloc-model" }

func (p *allocProvider) RetryPolicy() (int, time.Duration) { return -1, 0 }

type allocInvoice struct {
	Number string  `json:"number"`
	Total  float64 `json:"total"`
}

func allocDescriptor() Descriptor[string, allocInvoice] {
	return Descriptor[string, allocInvoice]{
		ID: ID{Name: "alloc-invoice", Version: "1"},
		BuildPrompt: func(text string, _ Options) (string, string) {
			return "Return a JSON object with number and total.", text
		},
		Contract: Contract[allocInvoice]{
			SchemaName: "invoice",
			Decode: func(body string) (allocInvoice, error) {
				var v allocInvoice
				err := json.Unmarshal([]byte(body), &v)
				if err == nil && v.Number == "" {
					err = errors.New("no number")
				}
				return v, err
			},
		},
	}
}

func allocBatchDescriptor() Descriptor[string, allocInvoice] {
	d := allocDescriptor()
	d.Batch = Batch[string, allocInvoice]{
		Class: BatchItemwise, Kind: AlgebraIndependent,
		Encode: func(items []string, _ Options) (string, string, error) { return "Return an array.", items[0], nil },
		Merge: func(string, []string) ([]allocInvoice, error) {
			return []allocInvoice{{Number: "A"}}, nil
		},
	}
	return d
}

// allocCase is one call of a runner, in the plain state, beside the internal call
// it wraps.
type allocCase struct {
	call     func(ctx context.Context) error
	internal func(ctx context.Context) error

	// extra is how many allocations the function makes that the internal call does
	// not, each for a reason the case names: the conversion of a list of results
	// into the list a caller is given, and the settings that pass through the
	// root's conversion as an interface value.
	extra float64
}

const allocRuns = 50

// requireAllocsLikeTheInternalCall measures the function and the internal call on
// a client carried by the context, where the two must allocate the same, and on
// the default client, where the function must allocate one more than the internal
// call does on a context that already carries the default client's snapshot: the
// pin of the client the call runs on.
func requireAllocsLikeTheInternalCall(t *testing.T, reply string, c allocCase) {
	t.Helper()
	model := &allocProvider{reply: reply}
	client := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	if err := c.call(client); err != nil {
		t.Fatalf("the function failed on the scripted model: %v", err)
	}
	if err := c.internal(client); err != nil {
		t.Fatalf("the internal call failed on the scripted model: %v", err)
	}
	wrapped := testing.AllocsPerRun(allocRuns, func() { _ = c.call(client) })
	direct := testing.AllocsPerRun(allocRuns, func() { _ = c.internal(client) })
	t.Logf("under WithClient: the function %v allocations, the internal call %v", wrapped, direct)
	if wrapped != direct+c.extra {
		t.Errorf("under a client the function allocates %v times and the internal call it wraps %v, plus %v the case allows", wrapped, direct, c.extra)
	}

	previous := schemaflux.Default()
	schemaflux.SetDefault(schemaflux.Client{Provider: model})
	t.Cleanup(func() { schemaflux.SetDefault(previous) })
	background := context.Background()
	pinned, _ := ops.PinExec(background)
	wrappedDefault := testing.AllocsPerRun(allocRuns, func() { _ = c.call(background) })
	directPinned := testing.AllocsPerRun(allocRuns, func() { _ = c.internal(pinned) })
	t.Logf("on the default client: the function %v allocations, the internal call %v on a pinned context", wrappedDefault, directPinned)
	if wrappedDefault != directPinned+1+c.extra {
		t.Errorf("on the default client the function allocates %v times, want the internal call's %v plus the pin and the %v the case allows", wrappedDefault, directPinned, c.extra)
	}
}

func TestRunAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	d := allocDescriptor()
	cases := map[string]struct {
		o     Options
		extra float64
	}{
		"no options": {},
		// The common settings pass through the root's conversion, which takes them
		// as an interface value: one allocation. No settings skip it.
		"every common setting": {Options{Options: schemaflux.Options{Model: "alloc-model", Tier: schemaflux.TierSmart, Effort: schemaflux.EffortLow, Mode: schemaflux.ModeStrict, Steering: "be exact", MaxOutputTokens: 300, WebSearch: true}}, 1},
		"the author's own":     {Options{JSONSchema: map[string]any{"type": "object"}, SchemaName: "inv", ResponseFormat: "json"}, 0},
	}
	for name, c := range cases {
		o := c.o
		t.Run(name, func(t *testing.T) {
			common, _ := bind.CommonOf(o.Options)
			requireAllocsLikeTheInternalCall(t, `{"number":"A-17","total":12.5}`, allocCase{
				call: func(ctx context.Context) error {
					_, err := Run(ctx, d, "Invoice A-17 for 12.50", o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, _, err := ops.RunOp(ctx, lowered(d, new(trap), "alloc-invoice"), "Invoice A-17 for 12.50", requestOptions(ctx, o, common))
					return err
				},
				extra: c.extra,
			})
		})
	}
}

func TestRunBatchAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	d := allocBatchDescriptor()
	requireAllocsLikeTheInternalCall(t, `[{"number":"A"}]`, allocCase{
		call: func(ctx context.Context) error {
			_, err := RunBatch(ctx, d, []string{"one"})
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.RunOpBatch(ctx, lowered(d, new(trap), "alloc-invoice"), []string{"one"}, requestOptions(ctx, Options{}, bind.Common{}))
			return err
		},
	})
}

func TestRunManyAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	d := allocDescriptor()
	items := []string{"one"}
	requireAllocsLikeTheInternalCall(t, `{"number":"A-17","total":12.5}`, allocCase{
		call: func(ctx context.Context) error {
			_, err := RunMany(ctx, d, items)
			return err
		},
		internal: func(ctx context.Context) error {
			_, _, err := ops.RunOpMany(ctx, lowered(d, new(trap), "alloc-invoice"), items, planRequest(requestOptions(ctx, Options{}, bind.Common{}), PlanOptions{}))
			return err
		},
		// The list of the results' values the caller is given is made from the list
		// of results the internal call returns: one allocation.
		extra: 1,
	})
}
