package ops

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// LX-060. These option types embed both CommonOptions and types.OpOptions, and
// their toOpOptions returned the CommonOptions half alone. Whatever a caller
// set on the other half was dropped without a word: a context (so a client's
// provider, a cancellation and a deadline), a model pin, steering, a token
// ceiling, a schema, web search. MaxOutputTokens, WebSearch and JSONSchema
// exist only on that half, so setting them compiled and did nothing.
var bothHalves = []struct {
	name    string
	options interface{ toOpOptions() types.OpOptions }
}{
	{"AnnotateOptions", AnnotateOptions{}},
	// What ConvertOpOptions returns for an operation it has no type for.
	{"convertedOptions", convertedOptions{}},
	{"ClusterOptions", ClusterOptions{}},
	{"CompressOptions", CompressOptions{}},
	{"CritiqueOptions", CritiqueOptions{}},
	{"DecomposeOptions", DecomposeOptions{}},
	{"EnrichOptions", EnrichOptions{}},
	{"ValidateOptions", ValidateOptions{}},
	{"QuestionOptions", QuestionOptions{}},
	{"MatchOptions", MatchOptions{}},
	{"NormalizeOptions", NormalizeOptions{}},
	{"PredictOptions", PredictOptions{}},
	{"RankOptions", RankOptions{}},
	{"SynthesizeOptions", SynthesizeOptions{}},
	{"VerifyOptions", VerifyOptions{}},
	// Already merged before this task; here so they cannot regress.
	{"SummarizeOptions", SummarizeOptions{}},
	{"RewriteOptions", RewriteOptions{}},
	{"TranslateOptions", TranslateOptions{}},
	{"ExpandOptions", ExpandOptions{}},
	{"ChooseOptions", ChooseOptions{}},
	{"FilterOptions", FilterOptions{}},
	{"SortOptions", SortOptions{}},
	{"ClassifyOptions", ClassifyOptions{}},
	{"CompareOptions", CompareOptions{}},
	{"ScoreOptions", ScoreOptions{}},
	{"ExtractOptions", ExtractOptions{}},
	{"GenerateOptions", GenerateOptions{}},
	{"TransformOptions", TransformOptions{}},
	{"BatchOptions", BatchOptions{}},
}

type optionsCarrier interface{ toOpOptions() types.OpOptions }

// withHalves returns a copy of an options value with its two embedded halves
// set.
func withHalves(t *testing.T, options optionsCarrier, common CommonOptions, embedded types.OpOptions) optionsCarrier {
	t.Helper()
	value := reflect.New(reflect.TypeOf(options)).Elem()
	if field := value.FieldByName("CommonOptions"); field.IsValid() {
		field.Set(reflect.ValueOf(common))
	} else if common != (CommonOptions{}) {
		t.Fatalf("%T has no CommonOptions half", options)
	}
	field := value.FieldByName("OpOptions")
	if !field.IsValid() {
		t.Fatalf("%T has no OpOptions half", options)
	}
	field.Set(reflect.ValueOf(embedded))
	return value.Interface().(optionsCarrier)
}

type contextMark struct{}

func TestEveryOptionsTypeCarriesItsEmbeddedHalf(t *testing.T) {
	schema := map[string]any{"type": "object"}
	embedded := types.OpOptions{
		Steering:        "from the embedded half",
		Threshold:       0.25,
		Mode:            types.Creative,
		Intelligence:    types.Quick,
		Model:           "a-pinned-model",
		Effort:          types.EffortLow,
		MaxOutputTokens: 321,
		WebSearch:       true,
		JSONSchema:      schema,
		SchemaName:      "a_schema",
		Context:         context.WithValue(context.Background(), contextMark{}, "embedded"),
	}

	for _, tc := range bothHalves {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.ValueOf(tc.options).FieldByName("CommonOptions").IsValid() {
				t.Skip("this type embeds OpOptions alone")
			}
			got := withHalves(t, tc.options, CommonOptions{}, embedded).toOpOptions()

			checks := []struct {
				field     string
				got, want any
			}{
				{"Steering", got.Steering, embedded.Steering},
				{"Threshold", got.Threshold, embedded.Threshold},
				{"Mode", got.Mode, embedded.Mode},
				{"Intelligence", got.Intelligence, embedded.Intelligence},
				{"Model", got.Model, embedded.Model},
				{"Effort", got.Effort, embedded.Effort},
				{"MaxOutputTokens", got.MaxOutputTokens, embedded.MaxOutputTokens},
				{"WebSearch", got.WebSearch, embedded.WebSearch},
				{"SchemaName", got.SchemaName, embedded.SchemaName},
			}
			for _, check := range checks {
				if check.got != check.want {
					t.Errorf("%s = %v, want %v from the embedded half", check.field, check.got, check.want)
				}
			}
			if got.JSONSchema == nil {
				t.Error("JSONSchema was dropped")
			}
			if got.Context == nil || got.Context.Value(contextMark{}) != "embedded" {
				t.Error("the context on the embedded half was dropped")
			}
		})
	}
}

// When both halves say something, CommonOptions wins, as it does everywhere a
// setting has these two homes.
func TestCommonOptionsWinsOverTheEmbeddedHalf(t *testing.T) {
	common := CommonOptions{
		Steering:     "from CommonOptions",
		Mode:         types.Strict,
		Intelligence: types.Smart,
		Model:        "the-common-model",
		Effort:       types.EffortHigh,
		Context:      context.WithValue(context.Background(), contextMark{}, "common"),
	}
	embedded := types.OpOptions{
		Steering:        "from the embedded half",
		Mode:            types.Creative,
		Intelligence:    types.Quick,
		Model:           "the-embedded-model",
		Effort:          types.EffortLow,
		MaxOutputTokens: 321,
		Context:         context.WithValue(context.Background(), contextMark{}, "embedded"),
	}
	for _, tc := range bothHalves {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.ValueOf(tc.options).FieldByName("CommonOptions").IsValid() {
				t.Skip("this type embeds OpOptions alone")
			}
			got := withHalves(t, tc.options, common, embedded).toOpOptions()
			if got.Steering != common.Steering || got.Mode != common.Mode || got.Intelligence != common.Intelligence ||
				got.Model != common.Model || got.Effort != common.Effort {
				t.Errorf("a CommonOptions setting lost to the embedded half: %+v", got)
			}
			if got.Context == nil || got.Context.Value(contextMark{}) != "common" {
				t.Error("the CommonOptions context lost to the embedded one")
			}
			// What only the embedded half can say still comes through.
			if got.MaxOutputTokens != 321 {
				t.Errorf("MaxOutputTokens = %d, want the embedded half's", got.MaxOutputTokens)
			}
		})
	}
}

// The same defect seen from outside: an operation reached through the embedded
// half's context goes to the provider on that context, with the model, token
// ceiling and steering set there.
func TestTheEmbeddedHalfReachesTheProvider(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "")
	ops := []struct {
		name  string
		reply string
		run   func(embedded types.OpOptions) error
	}{
		{"Question", `{"answer":"42","confidence":0.9,"reasoning":"r","evidence":[]}`, func(embedded types.OpOptions) error {
			opts := NewQuestionOptions("What is the total?")
			opts.OpOptions = embedded
			_, err := Question[string, string]("data", opts)
			return err
		}},
		{"Verify", verifyAnswer("[]", "[]", "[]"), func(embedded types.OpOptions) error {
			opts := NewVerifyOptions().WithMinConfidence(0)
			opts.OpOptions = embedded
			_, err := Verify("content", opts)
			return err
		}},
		{"Filter", `{"ids":["i-000001"]}`, func(embedded types.OpOptions) error {
			opts := NewFilterOptions().WithCriteria("keep the first")
			opts.OpOptions = embedded
			_, err := Filter([]string{"a", "b"}, opts)
			return err
		}},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			provider := &captureProvider{resp: llm.CompletionResponse{Content: op.reply}}
			err := op.run(types.OpOptions{
				Context:         WithProvider(context.Background(), provider),
				Model:           "a-pinned-model",
				MaxOutputTokens: 321,
				Steering:        "Answer as an auditor would",
			})
			if err != nil {
				t.Fatalf("%s: %v", op.name, err)
			}
			if provider.attempts != 1 {
				t.Fatalf("the provider on the embedded context was called %d times, want 1", provider.attempts)
			}
			if provider.req.Model != "a-pinned-model" {
				t.Errorf("Model = %q, want the pin", provider.req.Model)
			}
			if provider.req.MaxTokens != 321 {
				t.Errorf("MaxTokens = %d, want the ceiling", provider.req.MaxTokens)
			}
			if !strings.Contains(provider.req.UserPrompt, "Answer as an auditor would") {
				t.Errorf("the steering did not reach the request:\n%s", provider.req.UserPrompt)
			}
		})
	}
}

// The Result wrappers attach a recorder to the context and read the envelope
// from it. They wrote that context to the embedded half, and the operation
// reads CommonOptions first, so with a context set there the recorder was
// never seen and the envelope came back empty.
func TestResultEnvelopesAreFilledWhicheverHalfHoldsTheContext(t *testing.T) {
	homes := []struct {
		name string
		set  func(common *CommonOptions, embedded *types.OpOptions, ctx context.Context)
	}{
		{"CommonOptions", func(common *CommonOptions, _ *types.OpOptions, ctx context.Context) { common.Context = ctx }},
		{"the embedded half", func(_ *CommonOptions, embedded *types.OpOptions, ctx context.Context) { embedded.Context = ctx }},
		{"both", func(common *CommonOptions, embedded *types.OpOptions, ctx context.Context) {
			common.Context, embedded.Context = ctx, ctx
		}},
	}
	wrappers := []struct {
		name string
		run  func(set func(*CommonOptions, *types.OpOptions, context.Context), ctx context.Context) (types.Meta, error)
	}{
		{"SummarizeResult", func(set func(*CommonOptions, *types.OpOptions, context.Context), ctx context.Context) (types.Meta, error) {
			opts := NewSummarizeOptions()
			set(&opts.CommonOptions, &opts.OpOptions, ctx)
			result, err := SummarizeResult("a long report", opts)
			return result.Meta, err
		}},
		{"RewriteResult", func(set func(*CommonOptions, *types.OpOptions, context.Context), ctx context.Context) (types.Meta, error) {
			opts := NewRewriteOptions()
			set(&opts.CommonOptions, &opts.OpOptions, ctx)
			result, err := RewriteResult("a long report", opts)
			return result.Meta, err
		}},
		{"TranslateResult", func(set func(*CommonOptions, *types.OpOptions, context.Context), ctx context.Context) (types.Meta, error) {
			opts := NewTranslateOptions().WithTargetLanguage("French")
			set(&opts.CommonOptions, &opts.OpOptions, ctx)
			result, err := TranslateResult("a long report", opts)
			return result.Meta, err
		}},
		{"ExpandResult", func(set func(*CommonOptions, *types.OpOptions, context.Context), ctx context.Context) (types.Meta, error) {
			opts := NewExpandOptions()
			set(&opts.CommonOptions, &opts.OpOptions, ctx)
			result, err := ExpandResult("a short note", opts)
			return result.Meta, err
		}},
		{"SortResult", func(set func(*CommonOptions, *types.OpOptions, context.Context), ctx context.Context) (types.Meta, error) {
			opts := NewSortOptions().WithCriteria("alphabetical")
			set(&opts.CommonOptions, &opts.OpOptions, ctx)
			result, err := SortResult([]string{"b", "a"}, opts)
			return result.Meta, err
		}},
	}
	for _, wrapper := range wrappers {
		for _, home := range homes {
			t.Run(wrapper.name+"/"+home.name, func(t *testing.T) {
				reply := `{"text": "a short text", "key_points": [], "changes_made": [], "tone_achieved": "neutral", "source_language_detected": "English", "added_content": [], "confidence": 0.9}`
				if wrapper.name == "SortResult" {
					reply = `{"ids":["i-000002","i-000001"]}`
				}
				provider := &captureProvider{resp: llm.CompletionResponse{Content: reply, Model: "the-model-that-answered", Provider: "local"}}
				meta, err := wrapper.run(home.set, WithProvider(context.Background(), provider))
				if err != nil {
					t.Fatalf("%s: %v", wrapper.name, err)
				}
				if meta.Attempts < 1 || meta.Model != "the-model-that-answered" {
					t.Errorf("the envelope is empty: Attempts = %d, Model = %q", meta.Attempts, meta.Model)
				}
			})
		}
	}
}

// Filter, Sort and Suggest compose their own instructions and put the caller's
// steering in front. They read it from CommonOptions alone, so steering set on
// the embedded half, which is what this repository's own examples do, was
// overwritten by the composed instructions.
func TestComposedInstructionsKeepSteeringFromEitherHalf(t *testing.T) {
	const steering = "Treat prices as excluding tax"
	ops := []struct {
		name  string
		reply string
		run   func(ctx context.Context, set func(*CommonOptions, *types.OpOptions)) error
	}{
		{"Filter", `{"ids":["i-000001"]}`, func(ctx context.Context, set func(*CommonOptions, *types.OpOptions)) error {
			opts := NewFilterOptions().WithCriteria("under ten dollars")
			opts.CommonOptions.Context = ctx
			set(&opts.CommonOptions, &opts.OpOptions)
			_, err := Filter([]string{"a", "b"}, opts)
			return err
		}},
		{"Sort", `{"ids":["i-000002","i-000001"]}`, func(ctx context.Context, set func(*CommonOptions, *types.OpOptions)) error {
			opts := NewSortOptions().WithCriteria("cheapest first")
			opts.CommonOptions.Context = ctx
			set(&opts.CommonOptions, &opts.OpOptions)
			_, err := Sort([]string{"b", "a"}, opts)
			return err
		}},
	}
	homes := []struct {
		name string
		set  func(*CommonOptions, *types.OpOptions)
	}{
		{"CommonOptions", func(common *CommonOptions, _ *types.OpOptions) { common.Steering = steering }},
		{"the embedded half", func(_ *CommonOptions, embedded *types.OpOptions) { embedded.Steering = steering }},
	}
	for _, op := range ops {
		for _, home := range homes {
			t.Run(op.name+"/"+home.name, func(t *testing.T) {
				provider := &captureProvider{resp: llm.CompletionResponse{Content: op.reply}}
				if err := op.run(WithProvider(context.Background(), provider), home.set); err != nil {
					t.Fatalf("%s: %v", op.name, err)
				}
				if !strings.Contains(provider.req.UserPrompt, steering) {
					t.Errorf("the caller's steering did not reach the request:\n%s", provider.req.UserPrompt)
				}
			})
		}
	}
}

// A new options type gets the same rule without anyone remembering to add it
// to the list above: every toOpOptions in this package, other than
// CommonOptions' own, is the merge of the two halves. This reads the source,
// so a method written the old way fails here before it ships.
func TestNoOptionsTypeConvertsOnlyOneHalf(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing the package: %v", err)
	}
	method := regexp.MustCompile(`(?s)func \((\w+) (\w+)\) toOpOptions\(\) types\.OpOptions \{(.*?)\n\}`)

	merged := 0
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("reading %s: %v", source, err)
		}
		for _, match := range method.FindAllStringSubmatch(string(raw), -1) {
			receiver, body := match[2], match[3]
			if receiver == "CommonOptions" {
				continue
			}
			// The defect, by name.
			if strings.Contains(body, ".CommonOptions.toOpOptions()") {
				t.Errorf("%s: %s.toOpOptions returns the CommonOptions half alone", source, receiver)
				continue
			}
			if strings.Contains(body, "mergeEmbeddedOpOptions(") {
				merged++
				continue
			}
			// What is left is a type with one half only, which has nothing to
			// merge. One that mentions CommonOptions has two.
			if strings.Contains(body, "CommonOptions") {
				t.Errorf("%s: %s.toOpOptions has two halves and does not merge them", source, receiver)
			}
		}
	}
	if merged != len(bothHalves) {
		t.Errorf("the package has %d options types that merge two halves and the list above names %d; keep them in step", merged, len(bothHalves))
	}
}

// resolvedContext is the context an options value runs on. It picked the right
// half and dropped the caller's Timeout, which lives on CommonOptions: an
// operation that built its context from this ran with the default deadline
// whatever the caller had set.
func TestResolvedContext(t *testing.T) {
	mark := func(name string) context.Context {
		return context.WithValue(context.Background(), contextMark{}, name)
	}
	cases := []struct {
		name     string
		common   CommonOptions
		embedded types.OpOptions
		want     any
		timeout  time.Duration // 0: no deadline expected
	}{
		{"nothing set", CommonOptions{}, types.OpOptions{}, nil, 0},
		{"CommonOptions", CommonOptions{Context: mark("common")}, types.OpOptions{}, "common", 0},
		{"the embedded half", CommonOptions{}, types.OpOptions{Context: mark("embedded")}, "embedded", 0},
		{"both: CommonOptions wins", CommonOptions{Context: mark("common")}, types.OpOptions{Context: mark("embedded")}, "common", 0},
		{"a timeout and no context", CommonOptions{Timeout: 5 * time.Second}, types.OpOptions{}, nil, 5 * time.Second},
		{"a timeout on CommonOptions' context", CommonOptions{Context: mark("common"), Timeout: 5 * time.Second}, types.OpOptions{}, "common", 5 * time.Second},
		{"a timeout over the embedded half's context", CommonOptions{Timeout: 5 * time.Second}, types.OpOptions{Context: mark("embedded")}, "embedded", 5 * time.Second},
		{"a zero timeout is none", CommonOptions{Context: mark("common"), Timeout: 0}, types.OpOptions{}, "common", 0},
		{"a negative timeout is none", CommonOptions{Context: mark("common"), Timeout: -time.Second}, types.OpOptions{}, "common", 0},
		{"a long timeout", CommonOptions{Timeout: 10 * time.Minute}, types.OpOptions{}, nil, 10 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := resolvedContext(tc.common, tc.embedded)
			if ctx == nil {
				t.Fatal("no context")
			}
			if got := ctx.Value(contextMark{}); got != tc.want {
				t.Errorf("the context is %v, want %v", got, tc.want)
			}
			deadline, bounded := ctx.Deadline()
			if bounded != (tc.timeout > 0) {
				t.Fatalf("deadline set = %v, want %v", bounded, tc.timeout > 0)
			}
			if bounded {
				if left := time.Until(deadline); left > tc.timeout || left < tc.timeout-5*time.Second {
					t.Errorf("%v left, want about %v", left, tc.timeout)
				}
			}
		})
	}

	// The caller's own earlier deadline still wins.
	early, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx := resolvedContext(CommonOptions{Context: early, Timeout: time.Hour}, types.OpOptions{})
	if deadline, _ := ctx.Deadline(); time.Until(deadline) > time.Second {
		t.Error("a timeout extended the caller's deadline")
	}
}

type lx060Widget struct {
	Name string `json:"name"`
}

// ExtractResult read the context from CommonOptions alone, so a context on the
// embedded half, with its client's provider, its cancellation and its deadline,
// was discarded.
func TestExtractResultReadsTheContextFromEitherHalf(t *testing.T) {
	homes := []struct {
		name string
		set  func(*ExtractOptions, context.Context)
	}{
		{"CommonOptions", func(o *ExtractOptions, ctx context.Context) { o.CommonOptions.Context = ctx }},
		{"the embedded half", func(o *ExtractOptions, ctx context.Context) { o.OpOptions.Context = ctx }},
	}
	for _, home := range homes {
		t.Run(home.name+"/the provider and the envelope", func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
			provider := &captureProvider{resp: llm.CompletionResponse{Content: `{"name":"widget-a"}`, Model: "the-model-that-answered", Provider: "local"}}
			opts := NewExtractOptions()
			home.set(&opts, WithProvider(context.Background(), provider))

			result, err := ExtractResult[lx060Widget]("a widget named widget-a", opts)
			if err != nil {
				t.Fatalf("ExtractResult: %v", err)
			}
			if provider.attempts != 1 || result.Value.Name != "widget-a" {
				t.Fatalf("the provider on the context was called %d times; Value = %+v", provider.attempts, result.Value)
			}
			if result.Meta.Attempts < 1 || result.Meta.Model != "the-model-that-answered" {
				t.Errorf("the envelope is empty: Attempts = %d, Model = %q", result.Meta.Attempts, result.Meta.Model)
			}
		})

		t.Run(home.name+"/a cancelled context", func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
			provider := &captureProvider{resp: llm.CompletionResponse{Content: `{"name":"widget-a"}`}}
			cancelled, cancel := context.WithCancel(WithProvider(context.Background(), provider))
			cancel()
			opts := NewExtractOptions()
			home.set(&opts, cancelled)

			_, err := ExtractResult[lx060Widget]("a widget named widget-a", opts)
			if err == nil || provider.attempts != 0 {
				t.Errorf("err = %v, provider calls = %d; want a refusal and no call", err, provider.attempts)
			}
		})
	}
}

// A client-scope lock travels on the context, and the check for it looked at
// CommonOptions' context alone: a lock on the embedded half was not seen and
// the limit it set was not applied.
func TestLockedLimitsAreSeenOnEitherHalf(t *testing.T) {
	locked := WithLockedLimits(context.Background(), LockedLimits{MaxOutputTokens: 100})
	cases := []struct {
		name    string
		set     func(*ExtractOptions)
		wantErr bool
	}{
		{"no lock", func(o *ExtractOptions) { o.OpOptions.MaxOutputTokens = 500 }, false},
		{"a lock on CommonOptions, over the ceiling", func(o *ExtractOptions) {
			o.CommonOptions.Context = locked
			o.OpOptions.MaxOutputTokens = 500
		}, true},
		{"a lock on the embedded half, over the ceiling", func(o *ExtractOptions) {
			o.OpOptions.Context = locked
			o.OpOptions.MaxOutputTokens = 500
		}, true},
		{"a lock on the embedded half, under the ceiling", func(o *ExtractOptions) {
			o.OpOptions.Context = locked
			o.OpOptions.MaxOutputTokens = 50
		}, false},
		{"a lock on CommonOptions, under the ceiling", func(o *ExtractOptions) {
			o.CommonOptions.Context = locked
			o.OpOptions.MaxOutputTokens = 50
		}, false},
		{"a lock and no ceiling asked for", func(o *ExtractOptions) { o.OpOptions.Context = locked }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := NewExtractOptions()
			tc.set(&opts)
			err := checkLockedLimits(opts)
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkLockedLimits = %v, want an error: %v", err, tc.wantErr)
			}
		})
	}
}
