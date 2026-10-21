package schemaflux

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

type allocInvoice struct {
	Number string  `json:"number"`
	Total  float64 `json:"total"`
}

func TestExtractAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	const reply = `{"number":"A-17","total":12.5}`
	cases := map[string]ExtractOptions{
		"no options":                {},
		"one options value":         {StrictSchema: true, SchemaHints: map[string]string{"total": "euros"}, Model: "alloc-model", MaxOutputTokens: 300},
		"every common setting":      {Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "be exact", MaxOutputTokens: 300, WebSearch: true},
		"the checks of Strict mode": {ExactFields: true, CompleteFields: true},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: reply,
				call: func(ctx context.Context) error {
					_, err := Extract[allocInvoice](ctx, "Invoice A-17 for 12.50", o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Extract[allocInvoice]("Invoice A-17 for 12.50", extractInternal(ctx, o))
					return err
				},
			})
		})
	}
	t.Run("two options values are merged without allocating", func(t *testing.T) {
		first, second := ExtractOptions{Model: "alloc-model"}, ExtractOptions{StrictSchema: true}
		merged := mergeExtractOptions([]ExtractOptions{first, second})
		requireAllocsLikeTheInternalCall(t, allocCase{
			reply: reply,
			call: func(ctx context.Context) error {
				_, err := Extract[allocInvoice](ctx, "Invoice A-17 for 12.50", first, second)
				return err
			},
			internal: func(ctx context.Context) error {
				_, err := ops.Extract[allocInvoice]("Invoice A-17 for 12.50", extractInternal(ctx, merged))
				return err
			},
		})
	})
}

type allocSource struct {
	First string `json:"first"`
	Last  string `json:"last"`
}

type allocTarget struct {
	Name string `json:"name"`
}

func TestTransformAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	const reply = `{"name":"Ada Lovelace"}`
	input := allocSource{First: "Ada", Last: "Lovelace"}
	cases := map[string]TransformOptions{
		"no options":           {},
		"one options value":    {MappingRules: map[string]string{"name": "first + last"}, PreserveFields: []string{"first"}, MergeStrategy: "merge", Logic: "join", Model: "alloc-model"},
		"every common setting": {Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "be exact", MaxOutputTokens: 300, WebSearch: true},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: reply,
				call: func(ctx context.Context) error {
					_, err := Transform[allocTarget](ctx, input, o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Transform[allocSource, allocTarget](input, transformInternal(ctx, o))
					return err
				},
			})
		})
	}
	t.Run("two options values are merged without allocating", func(t *testing.T) {
		first, second := TransformOptions{Model: "alloc-model"}, TransformOptions{Logic: "join"}
		merged := mergeTransformOptions([]TransformOptions{first, second})
		requireAllocsLikeTheInternalCall(t, allocCase{
			reply: reply,
			call: func(ctx context.Context) error {
				_, err := Transform[allocTarget](ctx, input, first, second)
				return err
			},
			internal: func(ctx context.Context) error {
				_, err := ops.Transform[allocSource, allocTarget](input, transformInternal(ctx, merged))
				return err
			},
		})
	})
}

func TestGenerateAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	cases := map[string]struct {
		reply string
		o     GenerateOptions
		run   func(ctx context.Context, o GenerateOptions) error
		inner func(ctx context.Context, o GenerateOptions) error
	}{
		"a struct, no options": {
			`{"name":"Lentil soup"}`, GenerateOptions{},
			func(ctx context.Context, o GenerateOptions) error {
				_, err := Generate[allocTarget](ctx, "A dinner", o)
				return err
			},
			func(ctx context.Context, o GenerateOptions) error {
				_, err := ops.Generate[allocTarget]("A dinner", generateInternal(ctx, o))
				return err
			},
		},
		"a struct, one options value": {
			`{"name":"Lentil soup"}`, GenerateOptions{Template: "name: <dish>", Style: "brief", Constraints: map[string]any{"vegan": true}, EnsureUnique: true, Model: "alloc-model"},
			func(ctx context.Context, o GenerateOptions) error {
				_, err := Generate[allocTarget](ctx, "A dinner", o)
				return err
			},
			func(ctx context.Context, o GenerateOptions) error {
				_, err := ops.Generate[allocTarget]("A dinner", generateInternal(ctx, o))
				return err
			},
		},
		"a string, every common setting": {
			"A short bio.", GenerateOptions{Options: Options{Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "be exact", MaxOutputTokens: 300, WebSearch: true}},
			func(ctx context.Context, o GenerateOptions) error {
				_, err := Generate[string](ctx, "A bio", o)
				return err
			},
			func(ctx context.Context, o GenerateOptions) error {
				_, err := ops.Generate[string]("A bio", generateInternal(ctx, o))
				return err
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply:    tc.reply,
				call:     func(ctx context.Context) error { return tc.run(ctx, tc.o) },
				internal: func(ctx context.Context) error { return tc.inner(ctx, tc.o) },
			})
		})
	}
}

type allocContact struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func TestInferAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	const reply = `{"name":"Ada","email":"ada@example.com"}`
	input := allocContact{Email: "ada@example.com"}
	cases := map[string]InferOptions{
		"no options":           {},
		"one options value":    {Background: "UK customers only", Model: "alloc-model"},
		"every common setting": {Options: Options{Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "be exact", MaxOutputTokens: 300, WebSearch: true}},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: reply,
				call: func(ctx context.Context) error {
					_, err := Infer(ctx, input, o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Infer(input, inferInternal(ctx, o))
					return err
				},
			})
		})
	}
}

func TestParseAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	cases := map[string]struct {
		input string
		reply string
		o     ParseOptions
		local bool
	}{
		"JSON that the Go parsers read": {`{"name":"Ada","email":"ada@example.com"}`, `{}`, ParseOptions{}, true},
		"YAML that the Go parsers read": {"name: Ada\nemail: ada@example.com", `{}`, ParseOptions{}, true},
		"input only the fallback reads": {"Ada is reachable at ada@example.com", `{"name":"Ada","email":"ada@example.com"}`,
			ParseOptions{AllowLLMFallback: true, AutoFix: true, FormatHints: []string{"a ledger"}, Options: Options{Model: "alloc-model", Tier: TierSmart, MaxOutputTokens: 300}}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := allocCase{
				reply: tc.reply,
				call: func(ctx context.Context) error {
					_, err := Parse[allocContact](ctx, tc.input, tc.o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Parse[allocContact](tc.input, parseInternal(ctx, tc.o))
					return err
				},
			}
			if tc.local {
				requireAllocsLikeTheInternalCallThatAsksNoModel(t, c)
				return
			}
			requireAllocsLikeTheInternalCall(t, c)
		})
	}
}

// requireAllocsLikeTheInternalCallThatAsksNoModel is requireAllocsLikeTheInternalCall
// for a call that never reaches a model. The internal call then never needs the
// client and so never pins it, while the function pins it first thing, whether
// or not the call goes on to ask anything: its Record and its floor are the
// client's. The function is held to the internal call under WithClient, and on
// the default client to the internal call plus the pin, which the internal call
// does not pay here.
func requireAllocsLikeTheInternalCallThatAsksNoModel(t *testing.T, c allocCase) {
	t.Helper()
	model := &allocProvider{reply: c.reply}

	client := WithClient(context.Background(), Client{Provider: model})
	if err := c.call(client); err != nil {
		t.Fatalf("the function failed: %v", err)
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
	wrappedDefault := testing.AllocsPerRun(allocRuns, func() { _ = c.call(background) })
	directDefault := testing.AllocsPerRun(allocRuns, func() { _ = c.internal(background) })
	t.Logf("on the default client: the function %v allocations, the internal call %v", wrappedDefault, directDefault)
	if wrappedDefault != directDefault+1 {
		t.Errorf("on the default client the function allocates %v times, want the internal call's %v plus the pin", wrappedDefault, directDefault)
	}
}
