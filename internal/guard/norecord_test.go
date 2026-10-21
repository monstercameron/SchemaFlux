package guard_test

import (
	"context"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"
	"github.com/monstercameron/schemaflux/v2/op"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// A plainRow is a function that takes a context and asks no model itself, so it
// adds no Record (table A3, "ADD NO RECORD"). ok is a call that succeeds; bad is
// a call that fails as the function documents, with the Kind and Op of A3.
type plainRow struct {
	// key is the name the coverage test matches: "Branch", "flow.Until",
	// "Client.Warm".
	key string
	// op is the Op of the *Error bad returns; prefix says that only the start of
	// the Op is fixed by the documents.
	op     string
	prefix bool
	ok     func(ctx context.Context) (any, error)
	bad    func(ctx context.Context) (any, error)
}

var none schemaflux.Step[string]

func plainRows() []plainRow {
	yes := func(context.Context) (bool, error) { return true, nil }
	pass := func(context.Context, string) (string, error) { return "a", nil }
	return []plainRow{
		{key: "Branch", op: "branch",
			ok:  func(ctx context.Context) (any, error) { return ret(schemaflux.Branch(ctx, true, step("a"), step("b"))) },
			bad: func(ctx context.Context) (any, error) { return ret(schemaflux.Branch(ctx, true, none, step("b"))) }},
		{key: "Select", op: "select",
			ok: func(ctx context.Context) (any, error) {
				return ret(schemaflux.Select(ctx, "a", schemaflux.To("a", step("a")), schemaflux.To("b", step("b"))))
			},
			bad: func(ctx context.Context) (any, error) {
				return ret(schemaflux.Select(ctx, "zzz", schemaflux.To("a", step("a")), schemaflux.To("b", step("b"))))
			}},
		{key: "Gather", op: "gather",
			ok:  func(ctx context.Context) (any, error) { return ret(schemaflux.Gather(ctx, step("a"), step("b"))) },
			bad: func(ctx context.Context) (any, error) { return ret(schemaflux.Gather(ctx, none, step("b"))) }},
		{key: "Gather2", op: "gather", prefix: true,
			ok: func(ctx context.Context) (any, error) {
				a, b, err := schemaflux.Gather2(ctx, step("a"), step("b"))
				return struct{ A, B string }{a, b}, err
			},
			bad: func(ctx context.Context) (any, error) {
				a, b, err := schemaflux.Gather2(ctx, none, step("b"))
				return struct{ A, B string }{a, b}, err
			}},
		{key: "Gather3", op: "gather", prefix: true,
			ok: func(ctx context.Context) (any, error) {
				a, b, c, err := schemaflux.Gather3(ctx, step("a"), step("b"), step("c"))
				return struct{ A, B, C string }{a, b, c}, err
			},
			bad: func(ctx context.Context) (any, error) {
				a, b, c, err := schemaflux.Gather3(ctx, none, step("b"), step("c"))
				return struct{ A, B, C string }{a, b, c}, err
			}},
		{key: "Gather4", op: "gather", prefix: true,
			ok: func(ctx context.Context) (any, error) {
				a, b, c, d, err := schemaflux.Gather4(ctx, step("a"), step("b"), step("c"), step("d"))
				return struct{ A, B, C, D string }{a, b, c, d}, err
			},
			bad: func(ctx context.Context) (any, error) {
				a, b, c, d, err := schemaflux.Gather4(ctx, none, step("b"), step("c"), step("d"))
				return struct{ A, B, C, D string }{a, b, c, d}, err
			}},
		{key: "Each", op: "each",
			ok: func(ctx context.Context) (any, error) {
				return ret(schemaflux.Each(ctx, []string{"a", "b"}, pass))
			},
			bad: func(ctx context.Context) (any, error) {
				return ret(schemaflux.Each[string, string](ctx, []string{"a"}, nil))
			}},
		{key: "flow.BranchAhead", op: "flow.branchahead", prefix: true,
			ok: func(ctx context.Context) (any, error) {
				return ret(flow.BranchAhead(ctx, yes, step("a"), step("b")))
			},
			bad: func(ctx context.Context) (any, error) {
				return ret(flow.BranchAhead(ctx, nil, step("a"), step("b")))
			}},
		{key: "flow.BranchBoth", op: "flow.branchboth", prefix: true,
			ok: func(ctx context.Context) (any, error) {
				return ret(flow.BranchBoth(ctx, yes, step("a"), step("b")))
			},
			bad: func(ctx context.Context) (any, error) {
				return ret(flow.BranchBoth(ctx, nil, step("a"), step("b")))
			}},
		{key: "flow.RouteAhead", op: "flow.routeahead", prefix: true,
			ok: func(ctx context.Context) (any, error) {
				return ret(flow.RouteAhead(ctx, step("a"), "a", schemaflux.To("a", step("a")), schemaflux.To("b", step("b"))))
			},
			bad: func(ctx context.Context) (any, error) {
				return ret(flow.RouteAhead(ctx, none, "a", schemaflux.To("a", step("a")), schemaflux.To("b", step("b"))))
			}},
		{key: "flow.Escalate", op: "flow.escalate",
			ok: func(ctx context.Context) (any, error) {
				v, rec, err := flow.Escalate(ctx, step("a"), step("b"), func(string) bool { return true })
				return struct {
					V string
					R flow.EscalationRecord
				}{v, rec}, err
			},
			bad: func(ctx context.Context) (any, error) {
				v, rec, err := flow.Escalate(ctx, none, step("b"), func(string) bool { return true })
				return struct {
					V string
					R flow.EscalationRecord
				}{v, rec}, err
			}},
		{key: "flow.Until", op: "flow.until",
			ok: func(ctx context.Context) (any, error) {
				v, n, err := flow.Until(ctx, step("a"), func(string) bool { return true }, 3)
				return struct {
					V string
					N int
				}{v, n}, err
			},
			bad: func(ctx context.Context) (any, error) {
				v, n, err := flow.Until(ctx, step("a"), func(string) bool { return true }, 0)
				return struct {
					V string
					N int
				}{v, n}, err
			}},
		{key: "flow.Vote", op: "flow.vote",
			ok: func(ctx context.Context) (any, error) {
				v, rec, err := flow.Vote(ctx, 3, step("a"), flow.ExactAgreement[string](2))
				return struct {
					V string
					R flow.VoteRecord
				}{v, rec}, err
			},
			bad: func(ctx context.Context) (any, error) {
				v, rec, err := flow.Vote(ctx, 0, step("a"), flow.ExactAgreement[string](2))
				return struct {
					V string
					R flow.VoteRecord
				}{v, rec}, err
			}},
		{key: "flow.Checkpoint", op: "flow.checkpoint",
			ok: func(ctx context.Context) (any, error) {
				v, out, err := flow.Checkpoint(ctx, &flow.MemoryStore{}, "run", "step", "input", step("a"))
				return struct {
					V string
					O flow.CheckpointOutcome
				}{v, out}, err
			},
			bad: func(ctx context.Context) (any, error) {
				v, out, err := flow.Checkpoint(ctx, nil, "run", "step", "input", step("a"))
				return struct {
					V string
					O flow.CheckpointOutcome
				}{v, out}, err
			}},
		{key: "flow.Approve", op: "flow.approve",
			ok: func(ctx context.Context) (any, error) {
				gate := func(context.Context, string) (flow.ApprovalOutcome, error) {
					return flow.ApprovalOutcome{Approved: true}, nil
				}
				return ret(flow.Approve(ctx, step("a"), gate, nil))
			},
			bad: func(ctx context.Context) (any, error) { return ret(flow.Approve(ctx, step("a"), nil, nil)) }},
		{key: "flow.MapReduce", op: "flow.mapreduce",
			ok: func(ctx context.Context) (any, error) {
				v, res, err := flow.MapReduce(ctx, []string{"a", "b"}, func(_ context.Context, items []string) (int, error) { return len(items), nil })
				return struct {
					V []int
					R flow.MapReduceResult
				}{v, res}, err
			},
			bad: func(ctx context.Context) (any, error) {
				v, res, err := flow.MapReduce[string, int](ctx, []string{"a", "b"}, nil)
				return struct {
					V []int
					R flow.MapReduceResult
				}{v, res}, err
			}},
		{key: "op.Preflight", op: "op.preflight",
			ok: func(ctx context.Context) (any, error) {
				return ret(op.Preflight(ctx, invoiceDescriptor(), []string{"a", "b"}))
			},
			bad: func(ctx context.Context) (any, error) {
				var nilContext context.Context
				return ret(op.Preflight(nilContext, invoiceDescriptor(), []string{"a", "b"}))
			}},
		{key: "op.PreflightStages", op: "op.preflightstages",
			ok: func(ctx context.Context) (any, error) {
				plan, stages, err := op.PreflightStages(ctx, "datum", []op.Stage[string]{{Descriptor: stringDescriptor()}})
				return struct {
					P op.Plan
					S []op.Plan
				}{plan, stages}, err
			},
			bad: func(context.Context) (any, error) {
				var nilContext context.Context
				plan, stages, err := op.PreflightStages(nilContext, "datum", []op.Stage[string]{{Descriptor: stringDescriptor()}})
				return struct {
					P op.Plan
					S []op.Plan
				}{plan, stages}, err
			}},
		{key: "Client.Warm", op: "warm",
			ok: func(ctx context.Context) (any, error) {
				return nil, schemaflux.Client{Provider: &schemafluxtest.Provider{}}.Warm(ctx)
			},
			bad: func(context.Context) (any, error) {
				var nilContext context.Context
				return nil, schemaflux.Client{Provider: &schemafluxtest.Provider{}}.Warm(nilContext)
			}},
	}
}

// TestFunctionsThatAskNoModelAddNoRecord holds table A3's "ADD NO RECORD" list: a
// call that succeeds and a call that fails each leave no Record, and the failure
// is an *Error of kind ErrInvalidRequest with the Op A3 gives it and no value
// beside it.
func TestFunctionsThatAskNoModelAddNoRecord(t *testing.T) {
	for _, r := range plainRows() {
		for _, st := range recordedStates {
			t.Run(r.key+"/"+st.name, func(t *testing.T) {
				g := newRig(t, st, 0, nil)
				ctx := g.ctx
				if _, err := r.ok(ctx); err != nil {
					t.Fatalf("a valid call failed: %v", err)
				}
				res, err := r.bad(ctx)
				if n := len(g.records()); n != 0 {
					t.Errorf("%d Records were added by a function that asks no model", n)
				}
				if err == nil {
					t.Fatal("the failing call returned no error")
				}
				e := failureOf(err)
				if e == nil {
					t.Fatalf("the error is %T, want an *Error", err)
				}
				if e.Kind != schemaflux.ErrInvalidRequest {
					t.Errorf("the error is of kind %v, want invalid request", e.Kind)
				}
				switch {
				case e.Op == "":
					t.Errorf("the error has no Op")
				case r.prefix && len(e.Op) < len(r.op):
					t.Errorf("the error's Op is %q, want it to start with %q", e.Op, r.op)
				case r.prefix && e.Op[:len(r.op)] != r.op:
					t.Errorf("the error's Op is %q, want it to start with %q", e.Op, r.op)
				case !r.prefix && e.Op != r.op:
					t.Errorf("the error's Op is %q, A3 says %q", e.Op, r.op)
				}
				if !isZeroResult(res) {
					t.Errorf("a value of type %s came back beside the error", describeValue(res))
				}
			})
		}
	}
}
