package bind

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// These tests prove the shape of an operation function. The three functions
// below are the template written out over real internal operations: a Recorded
// row (ops.Classify), a twin row (ops.ExtractResult) and a plain decision
// (ops.Is), with IsDetail as the Detail form. The family lanes write the same
// three shapes over their own operations. Each is run in the four states of
// the call-path table: plain, a recorder, an observer, a floor.

// templateOptions stands in for a v2 options type: the seven common settings
// and one setting of the operation's own.
type templateOptions struct {
	Common
	MinConfidence float64

	// seen, when set, receives the context the internal call was made on, so
	// that a test can tell the plain path (the caller's own context) from the
	// recorded path (a context carrying the call log).
	seen *context.Context
}

func mergeTemplateOptions(opts []templateOptions) templateOptions {
	if len(opts) == 0 {
		return templateOptions{}
	}
	return opts[0]
}

func (o templateOptions) note(ctx context.Context) {
	if o.seen != nil {
		*o.seen = ctx
	}
}

// templateClassifyOptions builds the internal options from the context it is
// given: the constructor's value, then each setting that is not zero.
func templateClassifyOptions(ctx context.Context, categories []string, o templateOptions) ops.ClassifyOptions {
	o.note(ctx)
	opts := ops.NewClassifyOptions()
	opts.Categories = categories
	if o.MinConfidence != 0 {
		opts.MinConfidence = o.MinConfidence
	}
	o.Common.ApplyHalves(&opts.CommonOptions, &opts.OpOptions)
	opts.CommonOptions.Context = ctx
	return opts
}

func templateExtractOptions(ctx context.Context, o templateOptions) ops.ExtractOptions {
	o.note(ctx)
	opts := ops.NewExtractOptions()
	o.Common.ApplyHalves(&opts.CommonOptions, &opts.OpOptions)
	opts.CommonOptions.Context = ctx
	return opts
}

func templateDecisionOptions(o templateOptions) types.OpOptions {
	var opts types.OpOptions
	o.Common.Apply(&opts)
	return opts
}

// templateClassify is the Recorded row.
func templateClassify(ctx context.Context, input string, categories []string, opts ...templateOptions) (ops.ClassifyResult[string], error) {
	var zero ops.ClassifyResult[string]
	call, err := Begin(ctx, "classify")
	if err != nil {
		return zero, err
	}
	o := mergeTemplateOptions(opts)
	if len(categories) == 0 {
		return zero, call.Refuse(types.KindInvalidRequest, "categories is empty")
	}
	if err := o.Common.Check("classify"); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Classify[string, string](input, templateClassifyOptions(call.Context(), categories, o))
		return res, call.End(nil, err)
	}
	return Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.ClassifyResult[string], error) {
		return ops.Classify[string, string](input, templateClassifyOptions(ctx, categories, o))
	})
}

// templateExtract is the twin row.
func templateExtract[T any](ctx context.Context, input any, opts ...templateOptions) (T, error) {
	var zero T
	call, err := Begin(ctx, "extract")
	if err != nil {
		return zero, err
	}
	o := mergeTemplateOptions(opts)
	if err := o.Common.Check("extract"); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		value, err := ops.Extract[T](input, templateExtractOptions(call.Context(), o))
		return value, call.End(nil, err)
	}
	res, err := ops.ExtractResult[T](input, templateExtractOptions(call.Context(), o))
	return res.Value, call.End(&res.Meta, err)
}

// templateIs is the plain decision: Recorded, and never the twin.
func templateIs(ctx context.Context, subject any, question string, opts ...templateOptions) (bool, error) {
	call, err := Begin(ctx, "is")
	if err != nil {
		return false, err
	}
	o := mergeTemplateOptions(opts)
	if strings.TrimSpace(question) == "" {
		return false, call.Refuse(types.KindInvalidRequest, "question is blank")
	}
	if err := o.Common.Check("is"); err != nil {
		return false, call.End(nil, err)
	}
	if call.Plain() {
		o.note(call.Context())
		answer, err := ops.Is(call.Context(), subject, question, templateDecisionOptions(o))
		return answer, call.End(nil, err)
	}
	return Recorded(&call, types.ContractSchemaAndInvariantChecked, func(ctx context.Context) (bool, error) {
		o.note(ctx)
		return ops.Is(ctx, subject, question, templateDecisionOptions(o))
	})
}

// templateIsDetail is a Detail form: it calls its twin in every state and ends
// with the twin's meta.
func templateIsDetail(ctx context.Context, subject any, question string, opts ...templateOptions) (ops.Judged[bool], error) {
	var zero ops.Judged[bool]
	call, err := Begin(ctx, "isdetail")
	if err != nil {
		return zero, err
	}
	o := mergeTemplateOptions(opts)
	res, err := ops.IsResult(call.Context(), subject, question, templateDecisionOptions(o))
	return res.Value, call.End(&res.Meta, err)
}

// registerIdempotentStandIn registers a stand-in for the root's error
// conversion that, like the root's, returns an error it already converted
// unchanged: a refusal that went through Common.Check or Refuse is converted
// again by End, and must not be wrapped twice.
func registerIdempotentStandIn(t *testing.T) {
	t.Helper()
	unregister()
	t.Cleanup(unregister)
	Register(Hooks{Convert: func(op string, err error) error {
		if err == nil {
			return nil
		}
		var already *converted
		if errors.As(err, &already) {
			return err
		}
		return &converted{op: op, err: err}
	}})
}

type invoiceFields struct {
	Number string `json:"number"`
}

// shape is one row of the template, as a test runs it.
type shape struct {
	name  string
	op    string
	reply string
	// level is the contract level the row's Record carries, requested and
	// delivered.
	level types.ContractLevel
	// twin says the row calls its twin, which keeps the call log itself: it is
	// handed the call's own context in every state.
	twin bool
	// call is the v2 function, with a probe for the context the internal call
	// was made on; internal is the internal call it wraps.
	call     func(ctx context.Context, seen *context.Context) (any, error)
	internal func(ctx context.Context) (any, error)
	want     any
}

func templateShapes() []shape {
	categories := []string{"billing", "technical"}
	return []shape{
		{
			name: "a Recorded row, Classify", op: "classify", level: types.ContractJSONWellFormed,
			reply: `{"category":"billing","confidence":0.9,"alternatives":[],"reasoning":"x"}`,
			call: func(ctx context.Context, seen *context.Context) (any, error) {
				res, err := templateClassify(ctx, "my card was charged twice", categories, templateOptions{seen: seen})
				return res.Category, err
			},
			internal: func(ctx context.Context) (any, error) {
				res, err := ops.Classify[string, string]("my card was charged twice", templateClassifyOptions(ctx, categories, templateOptions{}))
				return res.Category, err
			},
			want: "billing",
		},
		{
			name: "a twin row, Extract", op: "extract", level: types.ContractSchemaConstrained, twin: true,
			reply: `{"number":"A-17"}`,
			call: func(ctx context.Context, seen *context.Context) (any, error) {
				return templateExtract[invoiceFields](ctx, "invoice A-17", templateOptions{seen: seen})
			},
			internal: func(ctx context.Context) (any, error) {
				return ops.Extract[invoiceFields]("invoice A-17", templateExtractOptions(ctx, templateOptions{}))
			},
			want: invoiceFields{Number: "A-17"},
		},
		{
			name: "a plain decision, Is", op: "is", level: types.ContractSchemaAndInvariantChecked,
			reply: `{"q1":true}`,
			call: func(ctx context.Context, seen *context.Context) (any, error) {
				return templateIs(ctx, "Please refund my order.", "Is the customer asking for a refund?", templateOptions{seen: seen})
			},
			internal: func(ctx context.Context) (any, error) {
				return ops.Is(ctx, "Please refund my order.", "Is the customer asking for a refund?", templateDecisionOptions(templateOptions{}))
			},
			want: true,
		},
	}
}

// requestOf is the part of a request the states must not change.
type requestOf struct {
	Model, System, User, Format, SchemaName, Effort string
	Temperature                                     float64
	MaxTokens                                       int
	Schema                                          map[string]any
}

func requestFrom(req llm.CompletionRequest) requestOf {
	return requestOf{
		Model: req.Model, System: req.SystemPrompt, User: req.UserPrompt, Format: req.ResponseFormat,
		SchemaName: req.SchemaName, Effort: req.ReasoningEffort, Temperature: req.Temperature,
		MaxTokens: req.MaxTokens, Schema: req.JSONSchema,
	}
}

// templateState is one of the four states of the call-path table, built for a
// shape on a provider.
type templateState struct {
	name string
	// plain says the call is taken to be the plain state.
	plain bool
	// build returns the context of the call and the sinks that were put on it.
	build func(t *testing.T, s shape, p llm.Provider) (context.Context, []*recordingSink, *watcher)
}

func templateStates() []templateState {
	return []templateState{
		{"plain", true, func(t *testing.T, s shape, p llm.Provider) (context.Context, []*recordingSink, *watcher) {
			return clientContext(p), nil, nil
		}},
		{"a recorder on the client", false, func(t *testing.T, s shape, p llm.Provider) (context.Context, []*recordingSink, *watcher) {
			sink := &recordingSink{}
			return clientContext(p, withClientSink(sink)), []*recordingSink{sink}, nil
		}},
		{"a recorder on the context", false, func(t *testing.T, s shape, p llm.Provider) (context.Context, []*recordingSink, *watcher) {
			sink := &recordingSink{}
			return ops.WithSink(clientContext(p), sink), []*recordingSink{sink}, nil
		}},
		{"two recorders, each asking for something else", false, func(t *testing.T, s shape, p llm.Provider) (context.Context, []*recordingSink, *watcher) {
			a, b := &recordingSink{decisions: true, bodies: true, maxBody: 100}, &recordingSink{}
			return ops.WithSink(clientContext(p, withClientSink(a)), b), []*recordingSink{a, b}, nil
		}},
		{"an observer", false, func(t *testing.T, s shape, p llm.Provider) (context.Context, []*recordingSink, *watcher) {
			return clientContext(p), nil, observe(t)
		}},
		{"an observer and a recorder", false, func(t *testing.T, s shape, p llm.Provider) (context.Context, []*recordingSink, *watcher) {
			sink := &recordingSink{}
			return clientContext(p, withClientSink(sink)), []*recordingSink{sink}, observe(t)
		}},
		{"a floor the row meets", false, func(t *testing.T, s shape, p llm.Provider) (context.Context, []*recordingSink, *watcher) {
			return clientContext(p, withFloor(s.level)), nil, nil
		}},
		{"a floor the row meets, and a recorder", false, func(t *testing.T, s shape, p llm.Provider) (context.Context, []*recordingSink, *watcher) {
			sink := &recordingSink{}
			return clientContext(p, withFloor(s.level), withClientSink(sink)), []*recordingSink{sink}, nil
		}},
	}
}

func TestTheTemplateInTheFourStatesDeliversTheAnswerSendsTheSameRequestAndRecordsOnce(t *testing.T) {
	registerIdempotentStandIn(t)
	for _, s := range templateShapes() {
		t.Run(s.name, func(t *testing.T) {
			var reference *requestOf
			for _, state := range templateStates() {
				t.Run(state.name, func(t *testing.T) {
					provider := newScripted(s.reply)
					ctx, sinks, seen := state.build(t, s, provider)
					var internalContext context.Context
					got, err := s.call(ctx, &internalContext)
					if err != nil {
						t.Fatalf("the call failed: %v", err)
					}
					if !reflect.DeepEqual(got, s.want) {
						t.Errorf("answer = %#v, want %#v", got, s.want)
					}
					if provider.sent() != 1 {
						t.Fatalf("%d requests were sent, want 1", provider.sent())
					}

					// The request is the same in every state.
					req := requestFrom(provider.requests[0])
					if reference == nil {
						reference = &req
					} else if !reflect.DeepEqual(*reference, req) {
						t.Errorf("the request differs from the plain state's\n got %+v\nwant %+v", req, *reference)
					}

					// The plain state calls the plain internal function on the caller's
					// own context; every other state goes through the recorded entry.
					if state.plain && internalContext != ctx {
						t.Error("the plain path made the internal call on a context of its own")
					}
					// A twin with no recorder keeps the call log itself and is handed the
					// call's own context; with a recorder the context carries what
					// collects the call's decisions and body, so it is another one.
					if loggedByTheCall := !s.twin || len(sinks) > 0; !state.plain && loggedByTheCall && internalContext == ctx {
						t.Error("a call that is not plain made the internal call on the caller's context: its requests are not logged")
					}

					for i, sink := range sinks {
						o := sink.only(t)
						if o.Meta.Operation != s.op {
							t.Errorf("sink %d: operation %q, want %q", i, o.Meta.Operation, s.op)
						}
						if o.Err != nil {
							t.Errorf("sink %d: err %v", i, o.Err)
						}
						if o.Meta.Attempts != 1 || o.Meta.Usage.TotalTokens != 14 || o.Meta.Provider != scriptedName || o.Meta.Model != scriptedModel {
							t.Errorf("sink %d: attempts %d tokens %d provider %q model %q", i, o.Meta.Attempts, o.Meta.Usage.TotalTokens, o.Meta.Provider, o.Meta.Model)
						}
						if o.Meta.RequestedContract != s.level || o.Meta.DeliveredContract != s.level {
							t.Errorf("sink %d: requested %v delivered %v, want %v", i, o.Meta.RequestedContract, o.Meta.DeliveredContract, s.level)
						}
						if o.Start.IsZero() {
							t.Errorf("sink %d: no start time", i)
						}
					}
					if seen != nil {
						if got := seen.operations(); len(got) != 1 || got[0] != s.op {
							t.Errorf("the observer saw operations %v finish, want exactly [%s]", got, s.op)
						}
					}
				})
			}
		})
	}
}

func TestTheTemplateWhenAFloorIsAboveTheRowRefusesBeforeAnythingIsSent(t *testing.T) {
	registerIdempotentStandIn(t)
	for _, s := range templateShapes() {
		t.Run(s.name, func(t *testing.T) {
			for _, withRecorder := range []bool{false, true} {
				provider := newScripted(s.reply)
				sink := &recordingSink{}
				settings := []clientSetting{withFloor(s.level + 1)}
				if withRecorder {
					settings = append(settings, withClientSink(sink))
				}
				_, err := s.call(clientContext(provider, settings...), new(context.Context))
				if err == nil {
					t.Fatal("a call below the floor was delivered")
				}
				if c := asConverted(t, err); c.op != s.op {
					t.Errorf("the hook was given %q, want %q", c.op, s.op)
				}
				if types.KindOf(err) != types.KindPolicyViolation {
					t.Errorf("kind = %v, want the policy kind", types.KindOf(err))
				}
				if provider.sent() != 0 {
					t.Errorf("%d requests were sent under a floor the row cannot meet", provider.sent())
				}
				if withRecorder {
					o := sink.only(t)
					if o.Err != err || o.Meta.Operation != s.op || o.Meta.Attempts != 0 {
						t.Errorf("record: err %v operation %q attempts %d", o.Err, o.Meta.Operation, o.Meta.Attempts)
					}
				}
			}
		})
	}
}

func TestTheTemplateWhenTheProviderFailsReturnsAConvertedErrorAndRecordsIt(t *testing.T) {
	registerIdempotentStandIn(t)
	for _, s := range templateShapes() {
		t.Run(s.name, func(t *testing.T) {
			for _, state := range templateStates() {
				t.Run(state.name, func(t *testing.T) {
					provider := newScripted()
					provider.fail = types.Newf(types.KindProviderUnavailable, "provider", "down")
					ctx, sinks, _ := state.build(t, s, provider)
					_, err := s.call(ctx, new(context.Context))
					if err == nil {
						t.Fatal("a failed provider gave an answer")
					}
					c := asConverted(t, err)
					if c.op != s.op || types.KindOf(err) != types.KindProviderUnavailable {
						t.Errorf("op %q kind %v", c.op, types.KindOf(err))
					}
					for i, sink := range sinks {
						o := sink.only(t)
						if o.Err != err || o.Meta.Operation != s.op {
							t.Errorf("sink %d: err %v operation %q", i, o.Err, o.Meta.Operation)
						}
						if o.Meta.DeliveredContract != types.ContractPromptOnly {
							t.Errorf("sink %d: a failed call delivered %v", i, o.Meta.DeliveredContract)
						}
					}
				})
			}
		})
	}
}

func TestTheTemplateRefusesAnUnusableArgumentOrOptionBeforeAnythingIsSentAndStillRecords(t *testing.T) {
	registerIdempotentStandIn(t)
	cases := []struct {
		name string
		kind types.ErrorKind
		op   string
		call func(ctx context.Context) error
	}{
		{"no categories", types.KindInvalidRequest, "classify", func(ctx context.Context) error {
			_, err := templateClassify(ctx, "x", nil)
			return err
		}},
		{"a negative output ceiling on classify", types.KindConfiguration, "classify", func(ctx context.Context) error {
			_, err := templateClassify(ctx, "x", []string{"a"}, templateOptions{Common: Common{MaxOutputTokens: -1}})
			return err
		}},
		{"a tier that is not one", types.KindConfiguration, "extract", func(ctx context.Context) error {
			_, err := templateExtract[invoiceFields](ctx, "x", templateOptions{Common: Common{Tier: types.Speed(99)}})
			return err
		}},
		{"a mode that is not one", types.KindConfiguration, "is", func(ctx context.Context) error {
			_, err := templateIs(ctx, "x", "Is it?", templateOptions{Common: Common{Mode: types.Mode(99)}})
			return err
		}},
		{"a blank question", types.KindInvalidRequest, "is", func(ctx context.Context) error {
			_, err := templateIs(ctx, "x", "   ")
			return err
		}},
		{"a negative output ceiling on extract", types.KindConfiguration, "extract", func(ctx context.Context) error {
			_, err := templateExtract[invoiceFields](ctx, "x", templateOptions{Common: Common{MaxOutputTokens: -5}})
			return err
		}},
	}
	for _, tc := range cases {
		for _, withRecorder := range []bool{false, true} {
			name := tc.name
			if withRecorder {
				name += ", with a recorder"
			}
			t.Run(name, func(t *testing.T) {
				provider := newScripted(`{}`)
				sink := &recordingSink{}
				ctx := clientContext(provider)
				if withRecorder {
					ctx = ops.WithSink(ctx, sink)
				}
				err := tc.call(ctx)
				if err == nil || types.KindOf(err) != tc.kind {
					t.Fatalf("err = %v, kind %v, want kind %v", err, types.KindOf(err), tc.kind)
				}
				if asConverted(t, err).op != tc.op {
					t.Errorf("op = %q, want %q", asConverted(t, err).op, tc.op)
				}
				if provider.sent() != 0 {
					t.Errorf("%d requests were sent for a refused call", provider.sent())
				}
				if withRecorder {
					o := sink.only(t)
					if o.Err != err || o.Meta.Operation != tc.op || o.Meta.Attempts != 0 {
						t.Errorf("record = err %v op %q attempts %d", o.Err, o.Meta.Operation, o.Meta.Attempts)
					}
				}
			})
		}
	}
}

func TestTheTemplateRefusesANilContextForEveryShape(t *testing.T) {
	registerIdempotentStandIn(t)
	var nilContext context.Context
	for _, s := range templateShapes() {
		_, err := s.call(nilContext, new(context.Context))
		if err == nil || types.KindOf(err) != types.KindInvalidRequest || asConverted(t, err).op != s.op {
			t.Errorf("%s: err = %v", s.name, err)
		}
	}
	if _, err := templateIsDetail(nilContext, "x", "Is it?"); types.KindOf(err) != types.KindInvalidRequest {
		t.Errorf("IsDetail: err = %v", err)
	}
}

func TestTheDetailFormCallsItsTwinInEveryStateAndEndsWithItsMeta(t *testing.T) {
	registerIdempotentStandIn(t)
	for _, state := range templateStates() {
		t.Run(state.name, func(t *testing.T) {
			provider := newScripted(`{"q1":true,"confidence":0.8}`)
			s := shape{op: "isdetail", level: types.ContractSchemaAndInvariantChecked}
			ctx, sinks, seen := state.build(t, s, provider)
			judged, err := templateIsDetail(ctx, "Please refund my order.", "Is the customer asking for a refund?")
			if err != nil || !judged.Answer {
				t.Fatalf("IsDetail = %+v, %v", judged, err)
			}
			for _, sink := range sinks {
				o := sink.only(t)
				if o.Meta.Operation != "isdetail" || o.Meta.Attempts != 1 || o.Meta.Usage.TotalTokens != 14 {
					t.Errorf("record = %q attempts %d tokens %d: the twin's meta must be the Record's", o.Meta.Operation, o.Meta.Attempts, o.Meta.Usage.TotalTokens)
				}
			}
			if seen != nil {
				if got := seen.operations(); len(got) != 1 {
					t.Errorf("the observer saw %v finish, want one operation", got)
				}
			}
		})
	}
}

func TestTheTemplatesCommonSettingsReachTheRequestInEveryState(t *testing.T) {
	registerIdempotentStandIn(t)
	llm.DeclareRoute(scriptedName, "pinned-model", []string{"eu"}, 0)
	common := Common{Model: "pinned-model", Tier: types.Smart, Mode: types.Strict, Steering: "be brief", MaxOutputTokens: 321, WebSearch: true}
	for _, state := range templateStates() {
		t.Run(state.name, func(t *testing.T) {
			provider := newScripted(`{"category":"billing","confidence":0.9,"alternatives":[],"reasoning":"x"}`)
			s := shape{op: "classify", level: types.ContractJSONWellFormed}
			ctx, _, _ := state.build(t, s, provider)
			if _, err := templateClassify(ctx, "x", []string{"billing"}, templateOptions{Common: common}); err != nil {
				t.Fatal(err)
			}
			req := provider.requests[0]
			if req.Model != "pinned-model" || req.MaxTokens != 321 || !req.WebSearch || !strings.Contains(req.UserPrompt+req.SystemPrompt, "be brief") {
				t.Errorf("the request did not carry the common settings: model %q max %d web %v", req.Model, req.MaxTokens, req.WebSearch)
			}
		})
	}
}

func TestTheTemplateAllocatesAsTheInternalCallItWrapsInThePlainState(t *testing.T) {
	registerIdempotentStandIn(t)
	for _, s := range templateShapes() {
		t.Run(s.name, func(t *testing.T) {
			provider := newScripted(s.reply)
			provider.keep = false

			withDefaultClient(t, nil)
			ctx := clientContext(provider)
			wrapped := testing.AllocsPerRun(50, func() { _, _ = s.call(ctx, nil) })
			direct := testing.AllocsPerRun(50, func() { _, _ = s.internal(ctx) })
			t.Logf("under WithClient: the template %v allocations, the internal call %v", wrapped, direct)
			if wrapped != direct {
				t.Errorf("under a client the template allocates %v times and the internal call it wraps %v", wrapped, direct)
			}

			withDefaultClient(t, clientConfig(provider))
			background := context.Background()
			wrappedDefault := testing.AllocsPerRun(50, func() { _, _ = s.call(background, nil) })
			directDefault := testing.AllocsPerRun(50, func() { _, _ = s.internal(background) })
			t.Logf("on the default client: the template %v allocations, the internal call %v", wrappedDefault, directDefault)
			if wrappedDefault != directDefault {
				t.Errorf("on the default client the template allocates %v times and the internal call %v: the pin is one allocation either way, so they are equal", wrappedDefault, directDefault)
			}
		})
	}
}

func TestTheTemplateWhenTheClientCannotMakeACallReturnsItsErrorWithTheOperationAndStillRecords(t *testing.T) {
	registerIdempotentStandIn(t)
	why := types.Newf(types.KindConfiguration, "client", "Client.Timeout must not be negative")
	for _, s := range templateShapes() {
		t.Run(s.name, func(t *testing.T) {
			for _, withRecorder := range []bool{false, true} {
				sink := &recordingSink{}
				cfg := &ops.ExecConfig{Err: why}
				if withRecorder {
					cfg.Sink = sink
				}
				ctx := ops.WithSharedExecConfig(context.Background(), cfg)
				_, err := s.call(ctx, new(context.Context))
				if err == nil {
					t.Fatal("a client that cannot make a call made one")
				}
				if c := asConverted(t, err); c.op != s.op || !errors.Is(err, why) {
					t.Errorf("op %q, chain holds the snapshot's error: %v", c.op, errors.Is(err, why))
				}
				if types.KindOf(err) != types.KindConfiguration {
					t.Errorf("kind = %v, want configuration", types.KindOf(err))
				}
				if withRecorder {
					o := sink.only(t)
					if o.Err != err || o.Meta.Operation != s.op || o.Meta.Attempts != 0 {
						t.Errorf("record = err %v op %q attempts %d", o.Err, o.Meta.Operation, o.Meta.Attempts)
					}
				}
			}
		})
	}
}
